package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/folder"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/localprojects"
	"codeberg.org/mfiers/mus/internal/pikahook"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

func newIRODSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "irods",
		Aliases: []string{"mango"},
		Short:   "iRODS sync via the IRON CLI",
	}
	cmd.AddCommand(
		newIRODSLoginCmd(),
		newIRODSUploadCmd(),
		newIRODSCheckCmd(),
		newIRODSGetCmd(),
		newIRODSScanCmd(),
		newIRODSFindCmd(),
		newIRODSProjectsCmd(),
		newIRODSProjectCmd(),
	)
	return cmd
}

// resolveIRODSCollection computes where on iRODS uploads from this folder
// land. The layout is now MANDATORY:
//
//	<irods_home>/projects/<YEAR>/<data_project>/<safe_experiment_name>/
//
// The YEAR segment (4 digits, extracted from the data_project's mandatory
// year suffix) splinters the top-level /projects/ collection so it doesn't
// accumulate thousands of children — iRODS catalog scans on a single huge
// collection degrade noticeably above ~5000 entries.
//
// Components:
//   - irods_home          required, from .env cascade
//   - YEAR                derived from data_project (last 4 chars; guaranteed
//                         by the regex). Not configurable.
//   - data_project        required; must pass dataproject.ValidateName.
//     Override via --data-project flag.
//   - safe_experiment_name reproducible sanitisation of eln_experiment_name.
//     Override via --remote-name flag.
//
// Refuses (no fallbacks, no auto-derivation) if any piece is missing or
// invalid. Users see a clear message pointing at `mus eln tag <id>` /
// `mus config set data_project ...` as remediations.
// parseMangoFile extracts the iRODS path from a legacy *.mango sidecar's
// raw bytes. Accepts:
//
//   - JSON: `{"url": "https://mango.kuleuven.be/data-object/view/<path>"}`
//   - bare iRODS URL on one or more lines
//
// In both cases the iRODS path is "everything after the literal substring
// `data-object/view`". Returns an error if the marker isn't found or if the
// extracted path is empty.
//
// Pure function — no I/O, no client. Unit-testable.
func parseMangoFile(raw []byte) (string, error) {
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return "", fmt.Errorf("mango file is empty")
	}

	var remoteURL string
	// Try JSON first.
	if strings.HasPrefix(content, "{") {
		var obj struct {
			URL string `json:"url"`
		}
		if jerr := json.Unmarshal([]byte(content), &obj); jerr == nil && obj.URL != "" {
			remoteURL = obj.URL
		}
	}
	if remoteURL == "" {
		// Plain URL form.
		remoteURL = content
	}

	const marker = "data-object/view"
	idx := strings.Index(remoteURL, marker)
	if idx < 0 {
		return "", fmt.Errorf(
			"cannot extract iRODS path from %q (expected URL containing %q)",
			remoteURL, marker)
	}
	remotePath := remoteURL[idx+len(marker):]
	if remotePath == "" || remotePath == "/" {
		return "", fmt.Errorf("extracted iRODS path is empty")
	}
	return remotePath, nil
}

// firstPathSegment returns the first non-empty path component of an absolute
// iRODS path. For `/gbiomed/home/BADS/file.csv` it returns `gbiomed`. Used to
// derive the zone for PURL construction.
func firstPathSegment(p string) string {
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if seg != "" {
			return seg
		}
	}
	return ""
}

func resolveIRODSCollection(env *config.Env, overrideDataProject, overrideRemoteName string) (string, error) {
	home := env.String("irods_home")
	if home == "" {
		home = defaults.IRODSHome // baked-in fallback; overridable via -ldflags
	}
	if home == "" {
		return "", fmt.Errorf("irods_home not set — `mus config set irods_home /zone/home/...`")
	}
	home = strings.TrimRight(home, "/")

	dp := overrideDataProject
	if dp == "" {
		dp = env.String("data_project")
	}
	if dp == "" {
		return "", fmt.Errorf("data_project not set.\n" +
			"  - `mus config set data_project Fiers2025` (or pick your own NameYear), OR\n" +
			"  - pass --data-project NAME on the command line, OR\n" +
			"  - if you use ELN: `mus eln tag <experiment_id>` (prompts for it).")
	}
	if err := dataproject.ValidateName(dp); err != nil {
		return "", err
	}

	expName := overrideRemoteName
	if expName == "" {
		expName = dataproject.SanitizeForPath(env.String("eln_experiment_name"))
	}
	if expName == "" {
		return "", fmt.Errorf("no experiment-name component for remote path.\n" +
			"  - `mus config set eln_experiment_name MyPilot` (any path-safe label), OR\n" +
			"  - pass --remote-name NAME on the command line, OR\n" +
			"  - if you use ELN: `mus eln tag <experiment_id>` (auto-fills it).")
	}

	year := dataproject.Year(dp)
	if year == "" {
		// Should be impossible — ValidateName guarantees a 4-digit suffix.
		// Fail loud rather than silently dropping the year segment.
		return "", fmt.Errorf("could not extract year from data_project %q (internal bug)", dp)
	}
	return home + "/projects/" + year + "/" + dp + "/" + expName, nil
}

func newIRODSUploadCmd() *cobra.Command {
	// IRON's upload is idempotent by default: matching size+modtime → skip;
	// different → overwrite. There is no "force" concept to expose.
	var exclusive, newer, dryRun, noELNComment, forceH5adUpload, archive bool
	var forceOverwrite bool
	var dataProject, remoteName, packRaw, message string
	verify := true // verify-checksum on by default; opt-out below
	cmd := &cobra.Command{
		Use:   "upload -m MESSAGE FILE_OR_FOLDER [FILE_OR_FOLDER...]",
		Short: "upload via IRON; writes/refreshes *.mus sidecars",
		Long: "Uploads land at <irods_home>/projects/<YEAR>/<data_project>/<exp_name>/.\n" +
			"data_project and a non-empty experiment-name are MANDATORY.\n" +
			"data_project comes from .env (set via `mus config set` or `mus eln tag`);\n" +
			"the experiment-name component is a sanitised eln_experiment_name unless\n" +
			"overridden with --remote-name.\n\n" +
			"A -m / --message is REQUIRED. Like a git commit message — describes\n" +
			"why this upload is happening. The message is stamped on the local\n" +
			"sidecar (irods_message), on the iRODS object as a mus_upload_message\n" +
			"AVU, and prepended to the post-upload ELN comment.\n\n" +
			"Folder uploads: each folder gets ONE sidecar at <folder>.mus (sibling of\n" +
			"the folder) — no per-file sidecars are written inside the folder. mus\n" +
			"profiles the folder and, if it has too many small files (density check),\n" +
			"prompts to bundle into <folder>.tar.gz before upload. Force the choice\n" +
			"with --archive / -A (== --pack tar.gz) to always archive, or --pack\n" +
			"none to always upload as-is. The folder sidecar also carries a\n" +
			"recursive_sha256 Merkle hash so `mus check <folder>.mus` can detect\n" +
			"local drift later.\n\n" +
			"IRON handles file vs directory automatically; --verify-checksum (default\n" +
			"on) re-hashes both sides after transfer.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := strings.TrimSpace(message)
			if msg == "" {
				return fmt.Errorf("`mus irods upload` requires a -m / --message describing why this upload is happening")
			}
			pack, err := mergeArchiveAndPack(archive, packRaw)
			if err != nil {
				return err
			}
			return runIRODSUpload(cmd, args, iron.UploadOpts{
				VerifyChecksum: verify,
				Exclusive:      exclusive,
				Newer:          newer,
				DryRun:         dryRun,
			}, dataProject, remoteName, pack, !noELNComment, forceH5adUpload, forceOverwrite, msg)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"upload message describing what / why (REQUIRED)")
	_ = cmd.MarkFlagRequired("message")
	cmd.Flags().BoolVar(&verify, "verify-checksum", true, "re-hash both sides after upload (default: on)")
	cmd.Flags().BoolVar(&exclusive, "exclusive", false, "refuse to overwrite existing remote objects")
	cmd.Flags().BoolVar(&newer, "newer", false, "only upload files newer than the remote copy")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without uploading")
	cmd.Flags().StringVar(&dataProject, "data-project", "",
		"override the data_project component (NameYear format, e.g. Fiers2025)")
	cmd.Flags().StringVar(&remoteName, "remote-name", "",
		"override the experiment-name component of the remote path")
	cmd.Flags().StringVar(&packRaw, "pack", "auto",
		"folder packing: auto (prompt on TTY) | tar.gz | none")
	cmd.Flags().BoolVarP(&archive, "archive", "A", false,
		"force-archive folder args into <folder>.tar.gz before upload — "+
			"shorthand for --pack tar.gz; skips the density check + prompt")
	cmd.Flags().Bool("cleanup-archive", false,
		"after a successful tar.gz upload, delete the local archive (default: keep + warn)")
	cmd.Flags().Bool("cleanup-source", false,
		"after a successful upload, delete the source folder — sidecar becomes a "+
			"tombstone (irods_status=local_deleted). Combine with --cleanup-archive "+
			"for the equivalent of the interactive [X] letter. Deletes on trust "+
			"(no re-verification) — use --verify-and-clean for the safe variant.")
	cmd.Flags().Bool("verify-and-clean", false,
		"full pipeline for folders: upload, register in the ELN, RE-VERIFY every "+
			"uploaded byte against iRODS (checksum), and only on a complete match "+
			"delete the local source (+ the .tar.gz if --pack/--archive). Any "+
			"mismatch keeps all local data and exits non-zero. The safe way to "+
			"free local space after archiving.")
	cmd.Flags().Bool("no-metadata", false,
		"skip applying mus_* AVU metadata to the uploaded iRODS object")
	cmd.Flags().BoolVar(&noELNComment, "no-eln", false,
		"don't touch the linked ELN experiment (skip auto-sample-registration)")
	// Backwards-compat: --no-eln-comment used to control the (now removed)
	// HTML COMMENT summary. Kept as an alias so old scripts still work.
	cmd.Flags().BoolVar(&noELNComment, "no-eln-comment", false,
		"deprecated alias for --no-eln")
	_ = cmd.Flags().MarkHidden("no-eln-comment")
	cmd.Flags().BoolVar(&forceH5adUpload, "force-h5ad-upload", false,
		"bypass the .h5ad sanity-check gate (NOT recommended — depositor's responsibility)")
	cmd.Flags().BoolVar(&forceOverwrite, "force", false,
		"overwrite existing remote objects without prompting (default: prompt on overwrite, "+
			"skip on identical bytes, abort on non-TTY)")
	return cmd
}

// existingIRODSPathFromSidecar returns the irods_path field from
// the sidecar matching `abs`, or "" when no sidecar exists / no
// irods_path is recorded / read fails. Used by the upload loop to
// honour a file's previously-recorded location instead of imposing
// a freshly-computed path on every re-upload.
//
// Failure modes are silent: caller falls back to the computed path
// rather than aborting on a sidecar read error. The skip-if-same
// gate immediately downstream will tell the depositor whether the
// chosen path actually holds the bytes they expect.
func existingIRODSPathFromSidecar(abs string) string {
	scPath := sidecar.SidecarPath(abs)
	doc, err := readMaybe(scPath)
	if err != nil || doc == nil || doc.IRODS == nil {
		return ""
	}
	return strings.TrimSpace(doc.IRODS.Path)
}

// remoteUploadState classifies the pre-upload relationship between
// our local file and whatever's currently at the iRODS path. Three
// states — different downstream actions for each:
type remoteUploadState int

const (
	// remoteAbsent — no object at `remote` (or unreachable / decode
	// failure). Caller proceeds with a normal upload.
	remoteAbsent remoteUploadState = iota
	// remoteSameBytes — object exists AND its catalog-recorded
	// checksum matches our local sha256. Caller skips iron entirely.
	remoteSameBytes
	// remoteDiffers — object exists AND its sha256 differs from
	// ours. Caller would OVERWRITE — must prompt first unless --force.
	remoteDiffers
)

// inspectRemoteForUpload classifies the iRODS path's current state
// relative to our local file. Best-effort: any iron error returns
// remoteAbsent so the caller falls through to a real upload (we
// never block a first upload on a stale precheck). Comparison is
// case-insensitive — Mango sometimes records sha256 prefixed with
// `sha2:` or in base64; iron.Checksum normalises but we play
// defensively to never claim a match on borderline output.
func inspectRemoteForUpload(ctx context.Context, client *iron.Client,
	remote, localSum string) remoteUploadState {
	if localSum == "" {
		return remoteAbsent
	}
	remoteSum, err := client.Checksum(ctx, remote)
	if err != nil || remoteSum == "" {
		return remoteAbsent
	}
	if strings.EqualFold(localSum, remoteSum) {
		return remoteSameBytes
	}
	return remoteDiffers
}

// promptOverwrite asks the user whether to proceed when an upload
// would overwrite an existing remote object whose bytes differ from
// ours. Returns true on explicit y/yes, false otherwise. Always
// returns false on non-TTY stdin — there's no safe default for an
// unattended caller. The --force flag bypasses this entirely upstream;
// non-TTY callers who genuinely want to overwrite pass --force.
func promptOverwrite(stdin io.Reader, stdout io.Writer, remote string) bool {
	if !isStdinTerminal(stdin) {
		return false
	}
	fmt.Fprintf(stdout,
		"   ⚠ remote object exists at %s with DIFFERENT bytes — overwrite? [y/N] ",
		remote)
	var answer string
	if _, err := fmt.Fscanln(stdin, &answer); err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}

// isStdinTerminal reports whether stdin is a TTY (interactive). We
// only prompt in interactive sessions; non-TTY callers (CI, mus
// serve, scripted batches) must pass --force to overwrite.
func isStdinTerminal(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	// A TTY is a character device, not a regular file or pipe.
	return (st.Mode() & os.ModeCharDevice) != 0
}

// applyIRODSGroupACL grants `own` to the configured iRODS group on
// the uploaded object. Best-effort — if chmod fails (e.g. group
// doesn't exist, or current user doesn't have own), we WARN on
// stderr but don't fail the upload. The bytes are already on iRODS;
// a missing ACL stamp is recoverable, a rolled-back upload isn't.
//
// Source order for the group name:
//
//  1. mus.cfg `irods_group=<name>` (any cascade level)
//  2. defaults.IRODSGroup (baked-in fallback, "BADS")
//
// Set `irods_group=` (empty) in mus.cfg to opt out — applyIRODSGroupACL
// silently no-ops.
func applyIRODSGroupACL(ctx context.Context, client *iron.Client,
	env *config.Env, remote string, stderr io.Writer) {
	group := env.String("irods_group")
	if group == "" {
		// Distinguish "explicit empty (opt-out)" from "unset (use default)"
		// by checking Has(): explicit empty stays empty.
		if !env.Has("irods_group") {
			group = defaults.IRODSGroup
		}
	}
	if group == "" {
		return // opted out
	}
	if err := client.Chmod(ctx, "own", group, remote, false); err != nil {
		fmt.Fprintf(stderr,
			"  ⚠ iron chmod own %s %s failed: %v\n"+
				"     (upload succeeded but group ACL not stamped — "+
				"fix manually with `iron chmod own %s %s`)\n",
			group, remote, err, group, remote)
	}
}

func runIRODSUpload(cmd *cobra.Command, args []string, opts iron.UploadOpts, overrideDataProject, overrideRemoteName string, pack packMode, postELNComment, forceH5adUpload, forceOverwrite bool, message string) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	if err := requireELNDecision(env); err != nil {
		return err
	}
	remoteCollection, err := resolveIRODSCollection(env, overrideDataProject, overrideRemoteName)
	if err != nil {
		return err
	}

	client, err := iron.New()
	if err != nil {
		return err
	}
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// h5ad gate — refuse the whole batch BEFORE any bytes leave the
	// machine if any .h5ad in args (or inside a folder arg) lacks a
	// fresh PASS. --force-h5ad-upload bypasses.
	failures, err := gateH5adForUpload(ctx, cache, args, forceH5adUpload)
	if err != nil {
		return fmt.Errorf("h5ad gate: %w", err)
	}
	if len(failures) > 0 {
		return formatH5adGateError(failures)
	}
	if forceH5adUpload {
		fmt.Fprintln(os.Stderr,
			"⚠ --force-h5ad-upload set — h5ad sanity-check gate BYPASSED for this batch")
	}

	// Refuse multi-source-folder FILE invocations up-front: each
	// file's source folder produces a `mus_metadata_<stamp>.tar.gz`
	// with the same basename, and they'd collide in the single
	// iRODS collection target. Folder args use a different code
	// path (uploadFolderDirect / Archived) and are exempt.
	var fileArgs []string
	for _, a := range args {
		abs, absErr := filepath.Abs(a)
		if absErr != nil {
			continue
		}
		st, statErr := os.Stat(abs)
		if statErr != nil || st.IsDir() || sidecar.IsSidecar(abs) {
			continue
		}
		fileArgs = append(fileArgs, abs)
	}
	if err := requireSingleSourceFolder(fileArgs); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "→ remote collection: %s\n", remoteCollection)
	if err := client.Mkdir(ctx, remoteCollection); err != nil {
		return fmt.Errorf("imkdir %s: %w", remoteCollection, err)
	}

	var uploads []uploadedItem
	// One invocation = one timestamp on every metadata archive built
	// this run, so a batch of files across multiple folders produces
	// a coherent dated set of archives.
	invocationStamp := time.Now().UTC()
	// Track primaries per source folder so we can build one
	// metadata archive per folder AFTER all primaries from that
	// folder have uploaded (sidecars refreshed with irods_* block
	// first).
	primariesByFolder := map[string][]string{}

	for _, raw := range args {
		if sidecar.IsSidecar(raw) {
			fmt.Fprintf(os.Stderr, "mus: skipping sidecar %s\n", raw)
			continue
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return err
		}
		// Folder args go through the dedicated folder-upload path
		// (density check + optional tar.gz + folder sidecar).
		if st.IsDir() {
			if err := runIRODSUploadFolder(cmd, abs, opts, remoteCollection, env, pack, message, &uploads); err != nil {
				return err
			}
			continue
		}
		// File arg: existing flat code path below.
		//
		// Path resolution: if this file already has a sidecar with
		// irods_path recorded (a prior upload location), we REUSE
		// that path rather than the freshly-computed one. Sticky
		// per-file location — option B from the layout-migration
		// discussion. Without this, the v0.2.33 path change would
		// fork the namespace: every file previously uploaded under
		// the legacy /project/<dp>/ tree would silently get
		// re-uploaded to /projects/<YEAR>/<dp>/, leaving two iRODS
		// copies. The sidecar's recorded path is authoritative —
		// it's where this file actually lives, even if the cascade
		// would now prefer a different location.
		computedRemote := remoteCollection + "/" + filepath.Base(abs)
		remote := computedRemote
		if existing := existingIRODSPathFromSidecar(abs); existing != "" && existing != computedRemote {
			remote = existing
			fmt.Fprintf(cmd.OutOrStdout(),
				"   ↺ reusing recorded location %s (sidecar's irods_path; would otherwise compute %s)\n",
				existing, computedRemote)
		}
		fmt.Printf("→ %s\n", remote)

		// Compute the local sha256 BEFORE invoking iron so we can
		// short-circuit when the remote already holds identical bytes.
		// hashcache makes this stat-fast on the warm path.
		sum, err := cache.Sum(abs)
		if err != nil {
			return err
		}

		// Pre-upload state inspection. Three branches:
		//   - remoteSameBytes → skip iron entirely; sidecar still refreshes
		//   - remoteDiffers   → real overwrite; prompt user (unless --force)
		//   - remoteAbsent    → first upload; proceed normally
		// Skipping when the bytes match avoids both wasted bandwidth AND
		// the UNIX_FILE_OPEN_ERR that fires when a colleague tries to
		// overwrite a replica owned by a different user.
		state := inspectRemoteForUpload(ctx, client, remote, sum)
		switch state {
		case remoteSameBytes:
			fmt.Fprintf(cmd.OutOrStdout(),
				"   ✓ sha256 matches remote — skipping upload (sidecar will be refreshed)\n")
		case remoteDiffers:
			if !forceOverwrite && !promptOverwrite(os.Stdin, cmd.OutOrStdout(), remote) {
				return fmt.Errorf(
					"remote object %s exists with different bytes — overwrite refused.\n"+
						"  Pass --force to overwrite without prompt, or use a different remote name.",
					remote)
			}
			if err := client.Upload(ctx, abs, remote, opts); err != nil {
				return err
			}
		default: // remoteAbsent
			if err := client.Upload(ctx, abs, remote, opts); err != nil {
				return err
			}
		}

		// write/update sidecar
		scPath := sidecar.SidecarPath(abs)
		doc, err := readMaybe(scPath)
		if err != nil {
			return err
		}
		if doc == nil {
			doc = &sidecar.Doc{}
		}
		if !st.IsDir() {
			doc.File = sidecar.FileInfo{
				Sha256:  sum,
				Size:    st.Size(),
				Mtime:   st.ModTime().UTC().Truncate(time.Second),
				Hashed:  time.Now().UTC().Truncate(time.Second),
				AbsPath: abs,
			}
		}
		if doc.IRODS == nil {
			doc.IRODS = &sidecar.IRODS{}
		}
		doc.IRODS.Path = remote
		doc.IRODS.Status = "uploaded"
		doc.IRODS.UploadedAt = time.Now().UTC().Truncate(time.Second)
		doc.IRODS.Message = message

		// Path-based browse URL (`irods_url`). Convenient but does NOT
		// survive moves — see irods_purl for the location-independent one.
		web := env.String("irods_web")
		if web == "" {
			web = defaults.IRODSWeb // baked-in fallback
		}
		if web != "" {
			doc.IRODS.URL = strings.TrimRight(web, "/") + remote
		}

		// Persistent URL (`irods_purl`) — keyed on iRODS catalog ID, stable
		// across renames/moves. Built from <pid_base>/<zone>/<id>/. Best-
		// effort: if `iron stat -j` fails for any reason we just skip it.
		if stat, statErr := client.Stat(ctx, remote); statErr == nil {
			zone := firstPathSegment(remote)
			pidBase := env.String("irods_pid_base")
			if pidBase == "" {
				pidBase = defaults.IRODSPIDBase
			}
			if zone != "" && pidBase != "" && stat.ID != 0 {
				doc.IRODS.PURL = fmt.Sprintf("%s/%s/%d/",
					strings.TrimRight(pidBase, "/"), zone, stat.ID)
			}
		}
		// Stamp the data_project we used into the sidecar so the file
		// carries the membership label even after the .env cascade changes.
		if dp := overrideDataProject; dp != "" {
			doc.DataProject = dp
		} else if dp := env.String("data_project"); dp != "" {
			doc.DataProject = dp
		}
		// Stamp ELN context onto the sidecar. Only the experiment ID is
		// load-bearing for the new tag-folder flow; the *_name / *_id fields
		// are best-effort (populated only if some earlier ELN API call had
		// filled them into .mus).
		if env.Has("eln_experiment_id") {
			if doc.ELN == nil {
				doc.ELN = &sidecar.ELN{}
			}
			doc.ELN.ExperimentID = env.String("eln_experiment_id")
			if v := env.String("eln_experiment_name"); v != "" {
				doc.ELN.ExperimentName = v
			}
			if v := env.String("eln_study_id"); v != "" {
				doc.ELN.StudyID = v
			}
			if v := env.String("eln_study_name"); v != "" {
				doc.ELN.StudyName = v
			}
			if v := env.String("eln_project_id"); v != "" {
				doc.ELN.ProjectID = v
			}
			if v := env.String("eln_project_name"); v != "" {
				doc.ELN.ProjectName = v
			}
		}
		// Biology fields from the .env cascade — gap-fill so an h5ad
		// inspector pass (which already populated doc.Bio) wins over
		// what the depositor declared in .env.
		stampBioFromEnv(doc, env)

		// Local sidecar update + AVU push are both skipped on dry-run.
		// On dry-run we may not have actually uploaded the bytes (iron
		// just prints), so recording an upload locally — or trying to
		// attach AVU to a catalog entry that may be a phantom — would
		// be a lie.
		if !opts.DryRun {
			if err := sidecar.Write(scPath, doc); err != nil {
				return err
			}
			noMeta, _ := cmd.Flags().GetBool("no-metadata")
			if !noMeta {
				applyMetadataToIRODS(ctx, client, remote, doc, cmd.ErrOrStderr())
			}
			// Stamp the iRODS group ACL — group members get `own` so
			// they can read AND overwrite (mus prompts before any
			// overwrite, so even with self-service write there's a
			// human gate). Best-effort: failure warns but doesn't
			// roll back the upload.
			applyIRODSGroupACL(ctx, client, env, remote, cmd.ErrOrStderr())
		}

		// Companion files (sidecar, *.h5ad.md, folder mus.cfg,
		// folder index.md) are bundled into one tar.gz archive per
		// source folder and pushed AFTER all primaries from that
		// folder have uploaded — see the per-folder loop after this
		// one. We only stash the primary path here so the archive
		// step can pick it up.
		if !opts.DryRun {
			primariesByFolder[filepath.Dir(abs)] = append(
				primariesByFolder[filepath.Dir(abs)], abs)
		}

		uploads = append(uploads, uploadedItem{
			Kind:       "file",
			LocalPath:  abs,
			RemotePath: remote,
			URL:        doc.IRODS.URL,
			PURL:       doc.IRODS.PURL,
			Sha256:     sum,
			Size:       st.Size(),
		})
		// Pika audit-trail entry per upload. Opt-in + best-effort —
		// see pikahook docs.
		pikahook.EmitFireAndForget("irods-upload", map[string]string{
			"path":    abs,
			"remote":  remote,
			"purl":    doc.IRODS.PURL,
			"sha256":  sum,
			"size":    fmt.Sprintf("%d", st.Size()),
			"message": message,
		})
		// Update the local project-log so `mus irods projects --details`
		// can later show "the most recent local folder you uploaded
		// from for project X". Best-effort — silent on every error.
		if dp := doc.DataProject; dp != "" {
			if lp, err := localprojects.Open(""); err == nil {
				lp.RecordBestEffort(dp, filepath.Dir(abs))
			}
		}
	}

	// Per-folder metadata archive. iRODS-flavoured: only the
	// structured metadata (mus.cfg + per-primary .mus sidecars) goes
	// in — .md files are NOT auto-bundled here; the depositor
	// uploads those as standalone iRODS objects by passing them on
	// the command line. Now that every primary in this invocation
	// has a freshly-stamped sidecar (irods_* block populated),
	// bundle + push as one tar.gz per source folder. Failures here
	// are non-fatal — data is already on the catalog with AVU stamped.
	if !opts.DryRun {
		for folder, primaries := range primariesByFolder {
			archivePath, cleanup, err := buildMetadataArchive(folder, primaries, invocationStamp, false)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"  ⚠ metadata archive build failed for %s: %v\n", folder, err)
				continue
			}
			if archivePath == "" {
				cleanup()
				continue
			}
			remotePath := remoteCollection + "/" + filepath.Base(archivePath)
			fmt.Fprintf(cmd.OutOrStdout(), "  + %s\n", remotePath)
			if err := client.Upload(ctx, archivePath, remotePath, opts); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"  ⚠ metadata archive upload failed: %v — primaries already uploaded\n", err)
			}
			cleanup()
		}
	}

	// Register each uploaded item as an ELN Sample on the same
	// experiment (SAMPLESOUT section, one section per invocation).
	// The samples ARE the ELN-side record of the upload — the old
	// HTML-comment summary was dropped in favour of this structured
	// form. Storage-unit placement uses eln_irods_sample_location
	// (distinct from `mus eln sample`'s eln_sample_location).
	postUploadRegisterSamplesInELN(env, uploads, opts.DryRun, postELNComment, message, cmd.ErrOrStderr())
	return nil
}

func newIRODSCheckCmd() *cobra.Command {
	var deep, silent bool
	cmd := &cobra.Command{
		Use:   "check [SIDECAR_OR_FILE...]",
		Short: "verify local sha256 against the remote checksum reported by IRON",
		Long: "Runs a sequence of explicit checks per sidecar, printing one line each:\n\n" +
			"    STATUS    check-name           target                  (why)\n\n" +
			"STATUS is OK / MISMATCH / ERROR / MISSING / SKIP. Every line carries the\n" +
			"check name (file-vs-iRODS, archive-vs-iRODS, source-merkle, …) so the\n" +
			"output is grep-friendly and the reason for each result is visible.\n\n" +
			"Checks per sidecar shape:\n" +
			"  - file sidecar               → file-vs-iRODS\n" +
			"  - folder sidecar + archived  → archive-vs-iRODS\n" +
			"                                 source-merkle (skipped if source removed)\n" +
			"  - folder sidecar + as-is     → source-merkle\n" +
			"                                 deep-walk-vs-iRODS (skipped unless --deep)\n\n" +
			"--deep walks `iron tree --json` for as-is folder uploads and compares\n" +
			"every remote file's checksum against the local one. Slow on big trees,\n" +
			"but the only way to confirm bytes-on-disk == bytes-on-iRODS for as-is\n" +
			"folder deposits.\n\n" +
			"--silent suppresses OK / SKIP lines; failures (MISMATCH / ERROR /\n" +
			"MISSING) and the final summary still print.",
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSCheck(cmd, args, deep, silent)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false,
		"for as-is folder sidecars, walk iron tree to compare every file's checksum")
	cmd.Flags().BoolVar(&silent, "silent", false,
		"only print failures + summary; suppress per-check OK / SKIP lines")
	return cmd
}

// checkPrinter centralises status-line formatting for `mus irods check`.
// Every check funnels through one of its methods so the wire format stays
// consistent (STATUS  check  target  (note)) and `--silent` is honoured
// in exactly one place. Failures always print; OK / SKIP suppressible.
type checkPrinter struct {
	silent bool
	stdout io.Writer
	stderr io.Writer
}

func (cp *checkPrinter) printLine(w io.Writer, status, check, target, note string) {
	if note == "" {
		fmt.Fprintf(w, "%-9s %-22s %s\n", status, check, target)
		return
	}
	fmt.Fprintf(w, "%-9s %-22s %s  (%s)\n", status, check, target, note)
}

// ok / skip are suppressible.
func (cp *checkPrinter) ok(check, target, note string) {
	if cp.silent {
		return
	}
	cp.printLine(cp.stdout, "OK", check, target, note)
}
func (cp *checkPrinter) skip(check, target, note string) {
	if cp.silent {
		return
	}
	cp.printLine(cp.stdout, "SKIP", check, target, note)
}

// Failures always print on stderr regardless of --silent.
func (cp *checkPrinter) mismatch(check, target, note string) {
	cp.printLine(cp.stderr, "MISMATCH", check, target, note)
}
func (cp *checkPrinter) errf(check, target, format string, args ...any) {
	cp.printLine(cp.stderr, "ERROR", check, target, fmt.Sprintf(format, args...))
}
func (cp *checkPrinter) missing(check, target, note string) {
	cp.printLine(cp.stderr, "MISSING", check, target, note)
}

func runIRODSCheck(cmd *cobra.Command, args []string, deep, silent bool) error {
	if len(args) == 0 {
		args = []string{"."}
	}
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	client, err := iron.New()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	cp := &checkPrinter{
		silent: silent,
		stdout: cmd.OutOrStdout(),
		stderr: cmd.ErrOrStderr(),
	}

	var sidecars []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return err
		}
		if st.IsDir() {
			more, err := walkForSidecars(a, true)
			if err != nil {
				return err
			}
			sidecars = append(sidecars, more...)
		} else if sidecar.IsSidecar(a) {
			sidecars = append(sidecars, a)
		} else {
			sc := sidecar.SidecarPath(a)
			if _, err := os.Stat(sc); err == nil {
				sidecars = append(sidecars, sc)
			}
		}
	}
	if len(sidecars) == 0 {
		return fmt.Errorf("no sidecars with iRODS info to check")
	}

	checked, failed := 0, 0
	for _, sc := range sidecars {
		doc, err := sidecar.Read(sc)
		if err != nil {
			cp.errf("read-sidecar", sc, "%v", err)
			failed++
			continue
		}
		if doc.IRODS == nil || doc.IRODS.Path == "" {
			cp.skip("file-vs-iRODS", sc, "sidecar has no irods_path")
			continue
		}
		dataPath := sidecar.DataPath(sc)

		// Folder sidecars: their own sequence of checks.
		if doc.Kind == "folder" {
			c, f := irodsCheckFolder(ctx, cache, client, sc, doc, deep, cp)
			checked += c
			failed += f
			continue
		}

		// File sidecar: one check, file bytes vs iRODS checksum.
		localSum, err := cache.Sum(dataPath)
		if err != nil {
			cp.errf("file-vs-iRODS", dataPath, "local hash: %v", err)
			failed++
			continue
		}
		remoteSum, err := client.Checksum(ctx, doc.IRODS.Path)
		if err != nil {
			cp.errf("file-vs-iRODS", dataPath, "remote checksum: %v", err)
			failed++
			continue
		}
		checked++
		if strings.EqualFold(localSum, remoteSum) {
			cp.ok("file-vs-iRODS", dataPath, "local sha256 == remote checksum")
		} else {
			cp.mismatch("file-vs-iRODS", dataPath,
				fmt.Sprintf("local=%s remote=%s", short(localSum), short(remoteSum)))
			failed++
		}
	}
	fmt.Fprintf(cp.stdout, "checked %d, failed %d\n", checked, failed)
	if failed > 0 {
		return fmt.Errorf("%d failure(s)", failed)
	}
	return nil
}

// irodsCheckFolder runs the appropriate local-vs-remote check for a folder
// sidecar:
//
//   - archived form  → local archive sha256 vs iron-reported remote checksum
//                       of the archive object. Direct file analogue.
//   - as-is form     → the iRODS object is a collection; iron checksum
//                       refuses collections. We do a LOCAL-only verification
//                       (Merkle hash of source folder vs recursive_sha256 in
//                       sidecar) and print a one-line note that there is no
//                       per-file remote comparison yet.
//
// Returns (checked, failed) — caller folds into the running totals.
func irodsCheckFolder(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, scPath string, doc *sidecar.Doc, deep bool, cp *checkPrinter) (int, int) {

	if doc.Archive != nil && doc.Archive.Filename != "" {
		return irodsCheckFolderArchived(ctx, cache, client, scPath, doc, cp)
	}
	return irodsCheckFolderAsIs(ctx, cache, client, scPath, doc, deep, cp)
}

// irodsCheckFolderArchived runs the two checks for an archived
// folder deposit: archive bytes vs iRODS, and (when source still
// present) source Merkle vs sidecar.recursive_sha256.
func irodsCheckFolderArchived(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, scPath string, doc *sidecar.Doc, cp *checkPrinter) (int, int) {

	archivePath := filepath.Join(filepath.Dir(scPath), doc.Archive.Filename)
	if _, err := os.Stat(archivePath); err != nil {
		cp.missing("archive-vs-iRODS", archivePath,
			fmt.Sprintf("archive sibling absent — `mus irods get %s` to fetch", scPath))
		// Without the local archive we can't do either check; still
		// try source-merkle if the source folder is around.
		_ = checkSourceMerkleIfPresent(scPath, doc, cache, cp, "archive missing")
		return 0, 1
	}
	fail := 0

	// 1. archive bytes vs iRODS.
	localSum, err := cache.Sum(archivePath)
	if err != nil {
		cp.errf("archive-vs-iRODS", archivePath, "local hash: %v", err)
		return 1, 1
	}
	remoteSum, err := client.Checksum(ctx, doc.IRODS.Path)
	if err != nil {
		cp.errf("archive-vs-iRODS", archivePath, "remote checksum: %v", err)
		return 1, 1
	}
	if strings.EqualFold(localSum, remoteSum) {
		cp.ok("archive-vs-iRODS", archivePath, "local archive sha256 == remote checksum")
	} else {
		cp.mismatch("archive-vs-iRODS", archivePath,
			fmt.Sprintf("local=%s remote=%s", short(localSum), short(remoteSum)))
		fail = 1
	}

	// 2. source folder Merkle vs sidecar (when source is still present).
	if checkSourceMerkleIfPresent(scPath, doc, cache, cp, "") {
		// nothing extra; checkSourceMerkleIfPresent printed its own line
	}
	if mergedFolderMerkleFailed(scPath, doc, cache) {
		fail = 1
	}
	return 1, fail
}

// checkSourceMerkleIfPresent prints either an OK/MISMATCH/ERROR or a
// SKIP line describing the source-merkle check. Returns true when a
// substantive (non-skip) line was printed — caller usually doesn't
// care but the bool is useful for tests / callers that branch on
// whether the check actually ran.
//
// The fail bookkeeping is computed by mergedFolderMerkleFailed (which
// recomputes the hash; cheap thanks to the local sha256 cache).
//
// skipNote, when non-empty, is appended to the SKIP reason — used by
// the archive-missing case to explain why we still tried.
func checkSourceMerkleIfPresent(scPath string, doc *sidecar.Doc,
	cache *hashcache.Cache, cp *checkPrinter, skipNote string) bool {

	dataPath := sidecar.DataPath(scPath)
	st, err := os.Stat(dataPath)
	if err != nil || !st.IsDir() {
		why := "source folder absent (normal after `mus rm`)"
		if skipNote != "" {
			why = skipNote + "; " + why
		}
		cp.skip("source-merkle", dataPath, why)
		return false
	}
	if doc.Folder == nil || doc.Folder.RecursiveSha256 == "" {
		cp.skip("source-merkle", dataPath, "sidecar has no recursive_sha256")
		return false
	}
	actual, err := folder.RecursiveSHA256(dataPath, cache.Sum)
	if err != nil {
		cp.errf("source-merkle", dataPath, "%v", err)
		return true
	}
	if strings.EqualFold(actual, doc.Folder.RecursiveSha256) {
		cp.ok("source-merkle", dataPath, "folder Merkle == sidecar recursive_sha256")
		return true
	}
	cp.mismatch("source-merkle", dataPath,
		fmt.Sprintf("local Merkle drift now=%s, sidecar=%s",
			short(actual), short(doc.Folder.RecursiveSha256)))
	return true
}

// mergedFolderMerkleFailed re-evaluates whether the source-merkle
// check would have failed. Used by the archived-folder path to fold
// the source-merkle outcome into the failure count without making
// checkSourceMerkleIfPresent return multi-state.
//
// Returns false when the check was skipped (source absent, sidecar
// missing recursive_sha256). Returns true only on a real failure
// (mismatch or hash error).
func mergedFolderMerkleFailed(scPath string, doc *sidecar.Doc, cache *hashcache.Cache) bool {
	dataPath := sidecar.DataPath(scPath)
	st, err := os.Stat(dataPath)
	if err != nil || !st.IsDir() {
		return false
	}
	if doc.Folder == nil || doc.Folder.RecursiveSha256 == "" {
		return false
	}
	actual, err := folder.RecursiveSHA256(dataPath, cache.Sum)
	if err != nil {
		return true
	}
	return !strings.EqualFold(actual, doc.Folder.RecursiveSha256)
}

// irodsCheckFolderAsIs runs the source-merkle check (always) and the
// deep per-file walk (only when --deep) for an as-is folder deposit.
// iRODS doesn't expose a single-call checksum for collections, so
// there's no analogue of archive-vs-iRODS here.
func irodsCheckFolderAsIs(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, scPath string, doc *sidecar.Doc, deep bool, cp *checkPrinter) (int, int) {

	dataPath := sidecar.DataPath(scPath)
	st, err := os.Stat(dataPath)
	if err != nil || !st.IsDir() {
		cp.missing("source-merkle", dataPath, "source folder absent")
		cp.skip("deep-walk-vs-iRODS", dataPath, "source missing — nothing to compare")
		return 0, 1
	}
	if doc.Folder == nil || doc.Folder.RecursiveSha256 == "" {
		cp.errf("source-merkle", dataPath, "sidecar has no recursive_sha256")
		cp.skip("deep-walk-vs-iRODS", dataPath, "no baseline to compare against")
		return 0, 1
	}
	actual, err := folder.RecursiveSHA256(dataPath, cache.Sum)
	if err != nil {
		cp.errf("source-merkle", dataPath, "%v", err)
		return 0, 1
	}
	if !strings.EqualFold(actual, doc.Folder.RecursiveSha256) {
		cp.mismatch("source-merkle", dataPath,
			fmt.Sprintf("local Merkle drift now=%s, sidecar=%s",
				short(actual), short(doc.Folder.RecursiveSha256)))
		cp.skip("deep-walk-vs-iRODS", dataPath, "source already differs from sidecar")
		return 1, 1
	}
	cp.ok("source-merkle", dataPath, "folder Merkle == sidecar recursive_sha256")

	if !deep {
		cp.skip("deep-walk-vs-iRODS", dataPath,
			"--deep not set; per-file iRODS comparison skipped")
		return 1, 0
	}

	// Deep walk.
	mism, miss, extra, derr := deepFolderCheckAgainstIRODS(ctx, cache, client,
		dataPath, doc.IRODS.Path, cp.stdout, cp.stderr)
	if derr != nil {
		cp.errf("deep-walk-vs-iRODS", dataPath, "%v", derr)
		return 1, 1
	}
	if mism+miss+extra > 0 {
		cp.mismatch("deep-walk-vs-iRODS", dataPath,
			fmt.Sprintf("%d mismatched, %d missing on iRODS, %d extra on iRODS",
				mism, miss, extra))
		return 1, 1
	}
	cp.ok("deep-walk-vs-iRODS", dataPath, "every file matches iRODS")
	return 1, 0
}

// deepFolderCheckAgainstIRODS walks the remote collection via iron tree, then
// the local folder. For each local file, finds the corresponding remote
// object by relpath and compares checksums.
//
// Returns (mismatched, missingOnIRODS, extraOnIRODS, error). Missing-on-iRODS
// = a local file with no remote counterpart. Extra-on-iRODS = a remote file
// not present locally.
//
// Comparison is hex-string case-insensitive. iron's tree --columns checksum
// emits hex sha256 today (per the BADS scan output); if Mango ever switches
// to sha2-base64 we'll have to canonicalise here.
func deepFolderCheckAgainstIRODS(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, localDir, remoteColl string,
	stdout, stderr io.Writer) (mism, miss, extra int, err error) {

	raw, err := client.TreeJSON(ctx, remoteColl, "name", "size", "checksum")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("iron tree: %w", err)
	}

	// Build remote map: relpath (forward slashes) → checksum.
	remoteByRel := map[string]string{}
	remotePrefix := strings.TrimRight(remoteColl, "/") + "/"
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			Name     string `json:"name"`
			Checksum string `json:"checksum"`
		}
		if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
			return 0, 0, 0, fmt.Errorf("parse iron tree line: %w", jerr)
		}
		if rec.Checksum == "" {
			continue // collection or unchecksummed object — skip
		}
		if !strings.HasPrefix(rec.Name, remotePrefix) {
			continue
		}
		rel := strings.TrimPrefix(rec.Name, remotePrefix)
		remoteByRel[rel] = rec.Checksum
	}

	// Walk local; compare each file.
	seenLocal := map[string]bool{}
	werr := filepath.WalkDir(localDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(localDir, p)
		rel = filepath.ToSlash(rel)
		seenLocal[rel] = true

		remoteSum, ok := remoteByRel[rel]
		if !ok {
			fmt.Fprintf(stderr, "  MISSING on iRODS: %s\n", rel)
			miss++
			return nil
		}
		localSum, herr := cache.Sum(p)
		if herr != nil {
			return herr
		}
		if !strings.EqualFold(localSum, remoteSum) {
			fmt.Fprintf(stderr, "  MISMATCH %s  local=%s remote=%s\n",
				rel, short(localSum), short(remoteSum))
			mism++
		}
		return nil
	})
	if werr != nil {
		return mism, miss, extra, werr
	}

	for rel := range remoteByRel {
		if !seenLocal[rel] {
			fmt.Fprintf(stderr, "  EXTRA on iRODS: %s\n", rel)
			extra++
		}
	}
	return mism, miss, extra, nil
}

func newIRODSGetCmd() *cobra.Command {
	var force, verify bool
	cmd := &cobra.Command{
		Use:   "get SIDECAR [SIDECAR...]",
		Short: "download the file/folder each *.mus (or legacy *.mango) sidecar refers to",
		Long: "Accepts:\n" +
			"  - *.mus sidecar — file or folder (Kind=folder); downloads to the location\n" +
			"    next to the sidecar. For archived folder uploads, the archive object is\n" +
			"    downloaded (its filename comes from archive_filename).\n" +
			"  - *.mango legacy file (deprecated) — either JSON with a `url` field or a\n" +
			"    bare iRODS URL. The remote path is derived from the URL.\n",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := iron.New()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			for _, a := range args {
				if err := downloadOne(ctx, client, a, force, verify, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite local file/folder if present")
	cmd.Flags().BoolVar(&verify, "verify-checksum", true, "re-hash both sides after download")
	return cmd
}

// downloadOne resolves a single argument to (remote path, local destination)
// and invokes iron download. Handles three cases:
//
//   - *.mus file sidecar  → download remote object to <sidecar>[:-4]
//   - *.mus folder sidecar → download remote collection or archive into the
//     sidecar's parent directory. If archived, the archive's filename comes
//     from the sidecar; otherwise it's the basename of irods_path.
//   - *.mango legacy file → deprecation warning; parse URL → iRODS path;
//     download next to the .mango file.
func downloadOne(ctx context.Context, client *iron.Client,
	arg string, force, verify bool, stderr io.Writer) error {

	if strings.HasSuffix(arg, ".mango") {
		fmt.Fprintf(stderr, "note: %s is a legacy .mango file (deprecated); "+
			"prefer regenerating sidecars with `mus irods upload` to get *.mus instead\n", arg)
		return downloadFromMango(ctx, client, arg, force, verify)
	}

	// Bare iRODS path? `mus irods get /zone/.../foo.h5ad` is the
	// catalog-discovery flow (paired with `mus irods find`). We
	// synthesise a sidecar locally from the remote AVU + sha so
	// the rest of the get logic works unchanged.
	if looksLikeIRODSPath(arg) {
		return downloadFromBareIRODSPath(ctx, client, arg, force, verify, stderr)
	}

	if !sidecar.IsSidecar(arg) {
		return fmt.Errorf("%s is not a *.mus sidecar, *.mango file, or iRODS path", arg)
	}
	doc, err := sidecar.Read(arg)
	if err != nil {
		return err
	}
	if doc.IRODS == nil || doc.IRODS.Path == "" {
		return fmt.Errorf("%s has no irods_path", arg)
	}

	// Determine LOCAL destination:
	//   folder sidecar + archived: <parent>/<archive_filename>
	//   folder sidecar + as-is:    <parent>/ (iron places the collection here as a subdir)
	//   file sidecar:              <parent>/<basename of data path>
	parent := filepath.Dir(arg)
	var local string
	if doc.Kind == "folder" {
		if doc.Archive != nil && doc.Archive.Filename != "" {
			local = filepath.Join(parent, doc.Archive.Filename)
		} else {
			// As-is folder upload — IRON places the subcollection under
			// parent/ automatically.
			local = parent + string(os.PathSeparator)
		}
	} else {
		local = sidecar.DataPath(arg)
	}

	// Decide whether to download at all by comparing what's already on disk
	// against what the sidecar says SHOULD be there:
	//
	//   absent         → download (always)
	//   present, match → skip silently unless --force forces a re-download
	//   present, diff  → refuse without --force; with --force overwrite
	//
	// This makes `mus irods get` idempotent / incrementally re-runnable —
	// the common case (you already pulled the data, just want to confirm)
	// is now zero-cost.
	state, stateErr := localStateVsSidecar(arg, doc)
	if stateErr != nil {
		return fmt.Errorf("checking local state of %s: %w", local, stateErr)
	}
	skipDownload := false
	switch state {
	case localMatch:
		if !force {
			fmt.Fprintf(stderr, "  ✓ %s already matches sidecar; skipping download\n", local)
			skipDownload = true
		} else {
			fmt.Fprintf(stderr, "  ↻ %s already matches but --force is set; re-downloading\n", local)
		}
	case localMismatch:
		if !force {
			return fmt.Errorf(
				"%s exists but does NOT match the sidecar — refusing to overwrite.\n"+
					"  Re-run with --force to overwrite, or remove the local copy first.\n"+
					"  (Run `mus check %s` to see exactly how it differs.)",
				local, arg)
		}
		fmt.Fprintf(stderr, "  ↻ %s exists and differs from sidecar; overwriting (--force)\n", local)
	case localAbsent:
		// fall through — normal download
	}

	if !skipDownload {
		if err := client.Download(ctx, doc.IRODS.Path, local, iron.DownloadOpts{
			Exclusive:      !force,
			VerifyChecksum: verify,
		}); err != nil {
			return err
		}

		// After download, re-verify what landed against what the sidecar says.
		// iron --verify-checksum confirms transport integrity (bytes match the
		// current iRODS object); this confirms PROVENANCE integrity (the iRODS
		// object hasn't drifted from what mus uploaded).
		if err := verifyDownloadedSidecar(arg, doc, stderr); err != nil {
			return err
		}
	}

	// Refresh sidecar metadata from the remote AVU. Always runs — even
	// when the download was skipped (the user may have re-run get to
	// pick up annotation updates from a colleague). Fields the local
	// sidecar didn't have are filled silently. Fields that DIFFER from
	// the local sidecar surface as `~ key: old → new` warning lines and
	// overwrite the sidecar. Failures here are non-fatal — bytes are
	// already on disk; metadata refresh is a bonus.
	refreshSidecarFromAVU(ctx, client, arg, doc, stderr)
	pikahook.EmitFireAndForget("irods-get", map[string]string{
		"path":   local,
		"remote": doc.IRODS.Path,
		"purl":   doc.IRODS.PURL,
		"sha256": doc.File.Sha256,
	})
	return nil
}

// refreshSidecarFromAVU pulls the current AVU from iRODS and folds it
// into the local sidecar via mergeAVUIntoDoc. Saves the sidecar if
// anything changed. All failures are swallowed with a one-line note
// — the download has already succeeded, this is non-essential.
func refreshSidecarFromAVU(ctx context.Context, client *iron.Client,
	sidecarPath string, doc *sidecar.Doc, stderr io.Writer) {
	if doc.IRODS == nil || doc.IRODS.Path == "" {
		return
	}
	info, err := client.Stat(ctx, doc.IRODS.Path)
	if err != nil {
		fmt.Fprintf(stderr, "  note: could not fetch AVU for sidecar refresh: %v\n", err)
		return
	}
	if mergeAVUIntoDoc(doc, info.MetadataMap(), stderr) {
		if err := sidecar.Write(sidecarPath, doc); err != nil {
			fmt.Fprintf(stderr, "  note: AVU refresh found changes but sidecar write failed: %v\n", err)
		}
	}
}

// localState describes how a local file/folder compares to its sidecar.
type localState int

const (
	localAbsent   localState = iota // nothing on disk yet
	localMatch                      // present and sha256/Merkle matches
	localMismatch                   // present but differs from sidecar
)

// localStateVsSidecar runs the same logic as `mus check` against an
// existing local path and maps the result to a localState.
func localStateVsSidecar(sidecarPath string, _ *sidecar.Doc) (localState, error) {
	cache, err := hashcache.Open("")
	if err != nil {
		return 0, err
	}
	defer cache.Close()
	res := checkOne(cache, sidecarPath)
	switch res.status {
	case "missing":
		return localAbsent, nil
	case "ok":
		return localMatch, nil
	case "mismatch", "stale":
		return localMismatch, nil
	}
	return 0, fmt.Errorf("checkOne: %s (%s)", res.status, res.detail)
}

// verifyDownloadedSidecar runs the same logic as `mus check` against a
// just-downloaded sidecar. Reuses the hashcache so subsequent `mus check`
// calls are stat-fast.
func verifyDownloadedSidecar(sidecarPath string, doc *sidecar.Doc, stderr io.Writer) error {
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	res := checkOne(cache, sidecarPath)
	switch res.status {
	case "ok":
		fmt.Fprintf(stderr, "  ✓ verified %s against sidecar checksum\n", res.path)
		return nil
	case "missing":
		// Shouldn't happen — we just downloaded it — but report cleanly.
		return fmt.Errorf("downloaded file not present at %s after download", res.path)
	case "mismatch", "stale":
		return fmt.Errorf(
			"PROVENANCE MISMATCH at %s: %s\n"+
				"  The downloaded bytes do NOT match what the sidecar says was uploaded.\n"+
				"  Possible causes:\n"+
				"    - the iRODS object was modified after `mus irods upload` wrote the sidecar\n"+
				"    - the sidecar is stale (manually edited, regenerated from a different source)\n"+
				"  The downloaded file has been LEFT in place; inspect or remove it manually.",
			res.path, res.detail)
	}
	return fmt.Errorf("verify %s: %s (%s)", res.path, res.status, res.detail)
}

// looksLikeIRODSPath returns true when arg is shaped like an iRODS
// absolute path (`/zone/...`). The check is deliberately
// conservative — the caller has already ruled out *.mus sidecars
// and *.mango files; if it then walks like an iRODS path we treat
// it as one and let the subsequent iron.Stat fail loudly if it's
// not actually addressable on the catalog.
func looksLikeIRODSPath(arg string) bool {
	return strings.HasPrefix(arg, "/") && !strings.HasSuffix(arg, ".mus")
}

// downloadFromBareIRODSPath is the catalog-discovery entrypoint.
// User passes an iRODS path directly (typically piped in from
// `mus irods find`); mus synthesises a local sidecar from the
// remote AVU + sha and then runs the standard download path.
//
// The synthesised sidecar lands next to the downloaded file, so a
// subsequent `mus irods get <local.mus>` is fully idempotent —
// no different from how a sidecar-driven fetch behaves.
func downloadFromBareIRODSPath(ctx context.Context, client *iron.Client,
	remotePath string, force, verify bool, stderr io.Writer) error {

	info, err := client.Stat(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("iron stat %s: %w", remotePath, err)
	}
	remoteSum, err := client.Checksum(ctx, remotePath)
	if err != nil {
		// Non-fatal — sidecar without sha still drives a download
		// + sha will be computed on the fresh local file.
		fmt.Fprintf(stderr, "  note: remote checksum unavailable for %s (%v)\n", remotePath, err)
	}

	// Build a sidecar from the remote AVU.
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{
			Sha256:  remoteSum,
			Size:    info.Size,
			AbsPath: localDestForBarePath(remotePath),
		},
		IRODS: &sidecar.IRODS{
			Path:   remotePath,
			Status: "uploaded",
		},
	}
	mergeAVUIntoDoc(doc, info.MetadataMap(), stderr)

	// Local destination: cwd / basename.
	local := localDestForBarePath(remotePath)
	scPath := sidecar.SidecarPath(local)

	// Don't clobber an existing sidecar in cwd — if one exists, the
	// user is in a tree that already knows about this object and
	// should use the sidecar-driven flow instead.
	if _, err := os.Stat(scPath); err == nil && !force {
		return fmt.Errorf("%s already exists; re-run with --force, or use `mus irods get %s` to refresh",
			scPath, scPath)
	}

	// Write the synthesised sidecar so the standard download path
	// can reuse the localStateVsSidecar / verify / refresh code.
	if err := sidecar.Write(scPath, doc); err != nil {
		return fmt.Errorf("write synthesised sidecar: %w", err)
	}
	fmt.Fprintf(stderr, "  ↻ synthesised %s from iRODS AVU\n", scPath)

	return downloadOne(ctx, client, scPath, force, verify, stderr)
}

func localDestForBarePath(remotePath string) string {
	base := filepath.Base(remotePath)
	cwd, err := os.Getwd()
	if err != nil {
		return base
	}
	return filepath.Join(cwd, base)
}

// downloadFromMango handles the legacy *.mango sidecar files emitted by the
// Python mus. Delegates the actual parsing to parseMangoFile so the URL
// extraction is unit-testable without spinning up an iron client.
func downloadFromMango(ctx context.Context, client *iron.Client,
	mangoPath string, force, verify bool) error {

	raw, err := os.ReadFile(mangoPath)
	if err != nil {
		return err
	}
	remotePath, err := parseMangoFile(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", mangoPath, err)
	}

	// Local destination: drop the trailing ".mango" off the sidecar name.
	// e.g. /tmp/raw.h5ad.mango → /tmp/raw.h5ad
	local := strings.TrimSuffix(mangoPath, ".mango")
	if _, err := os.Stat(local); err == nil && !force {
		return fmt.Errorf("%s already exists — use --force to overwrite", local)
	}
	return client.Download(ctx, remotePath, local, iron.DownloadOpts{
		Exclusive:      !force,
		VerifyChecksum: verify,
	})
}
