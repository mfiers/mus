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
	)
	return cmd
}

// resolveIRODSCollection computes where on iRODS uploads from this folder
// land. The layout is now MANDATORY:
//
//	<irods_home>/project/<data_project>/<safe_experiment_name>/
//
// Components:
//   - irods_home          required, from .env cascade
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

	return home + "/project/" + dp + "/" + expName, nil
}

func newIRODSUploadCmd() *cobra.Command {
	// IRON's upload is idempotent by default: matching size+modtime → skip;
	// different → overwrite. There is no "force" concept to expose.
	var exclusive, newer, dryRun, noELNComment, forceH5adUpload bool
	var dataProject, remoteName, packRaw, message string
	verify := true // verify-checksum on by default; opt-out below
	cmd := &cobra.Command{
		Use:   "upload -m MESSAGE FILE_OR_FOLDER [FILE_OR_FOLDER...]",
		Short: "upload via IRON; writes/refreshes *.mus sidecars",
		Long: "Uploads land at <irods_home>/project/<data_project>/<exp_name>/.\n" +
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
			"with --pack tar.gz / --pack none. The folder sidecar also carries a\n" +
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
			pack, err := resolvePackMode(packRaw)
			if err != nil {
				return err
			}
			return runIRODSUpload(cmd, args, iron.UploadOpts{
				VerifyChecksum: verify,
				Exclusive:      exclusive,
				Newer:          newer,
				DryRun:         dryRun,
			}, dataProject, remoteName, pack, !noELNComment, forceH5adUpload, msg)
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
	cmd.Flags().Bool("cleanup-archive", false,
		"after a successful tar.gz upload, delete the local archive (default: keep + warn)")
	cmd.Flags().Bool("no-metadata", false,
		"skip applying mus_* AVU metadata to the uploaded iRODS object")
	cmd.Flags().BoolVar(&noELNComment, "no-eln-comment", false,
		"don't post an upload summary as a COMMENT section in the linked ELN experiment")
	cmd.Flags().BoolVar(&forceH5adUpload, "force-h5ad-upload", false,
		"bypass the .h5ad sanity-check gate (NOT recommended — depositor's responsibility)")
	return cmd
}

func runIRODSUpload(cmd *cobra.Command, args []string, opts iron.UploadOpts, overrideDataProject, overrideRemoteName string, pack packMode, postELNComment, forceH5adUpload bool, message string) error {
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

	fmt.Fprintf(os.Stderr, "→ remote collection: %s\n", remoteCollection)
	if err := client.Mkdir(ctx, remoteCollection); err != nil {
		return fmt.Errorf("imkdir %s: %w", remoteCollection, err)
	}

	var uploads []uploadedItem

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
		remote := remoteCollection + "/" + filepath.Base(abs)
		fmt.Printf("→ %s\n", remote)
		if err := client.Upload(ctx, abs, remote, opts); err != nil {
			return err
		}

		var sum string
		if !st.IsDir() {
			sum, err = cache.Sum(abs)
			if err != nil {
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
		}

		// Metadata travels as AVU on the iRODS object itself
		// (applyMetadataToIRODS above), not as separate .mus / .env
		// files. The depositor's local sidecar + cascade .env stay
		// local — they're working-state, not part of the deposit.

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
	}

	postUploadCommentToELN(env, uploads, opts.DryRun, postELNComment, message, cmd.ErrOrStderr())
	return nil
}

func newIRODSCheckCmd() *cobra.Command {
	var deep bool
	cmd := &cobra.Command{
		Use:   "check [SIDECAR_OR_FILE...]",
		Short: "verify local sha256 against the remote checksum reported by IRON",
		Long: "By default:\n" +
			"  - file sidecar              → hash local file vs iron checksum of remote\n" +
			"  - folder sidecar + archived  → hash local archive vs iron checksum of remote\n" +
			"  - folder sidecar + as-is     → local Merkle drift only (no remote round-trip)\n\n" +
			"With --deep, the as-is folder case fully walks `iron tree --json` and\n" +
			"compares every remote file's checksum against the local one. Slow on big\n" +
			"trees, but the only way to confirm bytes-on-disk match bytes-on-iRODS for\n" +
			"folders that were uploaded as collections.",
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSCheck(cmd, args, deep)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false,
		"for as-is folder sidecars, walk iron tree to compare every file's checksum")
	return cmd
}

func runIRODSCheck(cmd *cobra.Command, args []string, deep bool) error {
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
			fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", sc, err)
			failed++
			continue
		}
		if doc.IRODS == nil || doc.IRODS.Path == "" {
			continue
		}
		dataPath := sidecar.DataPath(sc)

		// Folder sidecars: pick the right thing to hash + compare.
		if doc.Kind == "folder" {
			c, f := irodsCheckFolder(ctx, cache, client, sc, doc, deep)
			checked += c
			failed += f
			continue
		}

		localSum, err := cache.Sum(dataPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", dataPath, err)
			failed++
			continue
		}
		remoteSum, err := client.Checksum(ctx, doc.IRODS.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR %s remote checksum: %v\n", dataPath, err)
			failed++
			continue
		}
		checked++
		if strings.EqualFold(localSum, remoteSum) {
			fmt.Printf("OK       %s\n", dataPath)
		} else {
			fmt.Fprintf(os.Stderr, "MISMATCH %s  local=%s remote=%s\n",
				dataPath, short(localSum), short(remoteSum))
			failed++
		}
	}
	fmt.Printf("checked %d, failed %d\n", checked, failed)
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
	client *iron.Client, scPath string, doc *sidecar.Doc, deep bool) (int, int) {

	stderr := os.Stderr
	stdout := os.Stdout

	if doc.Archive != nil && doc.Archive.Filename != "" {
		// Archived: hash local archive, ask iron for the remote checksum.
		archivePath := filepath.Join(filepath.Dir(scPath), doc.Archive.Filename)
		if _, err := os.Stat(archivePath); err != nil {
			fmt.Fprintf(stderr, "MISSING  %s  (archive sibling absent — `mus irods get %s` to fetch)\n",
				archivePath, scPath)
			return 0, 1
		}
		localSum, err := cache.Sum(archivePath)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR    %s: %v\n", archivePath, err)
			return 0, 1
		}
		remoteSum, err := client.Checksum(ctx, doc.IRODS.Path)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR    %s remote checksum: %v\n", archivePath, err)
			return 0, 1
		}
		if strings.EqualFold(localSum, remoteSum) {
			fmt.Fprintf(stdout, "OK       %s  (archive)\n", archivePath)
			return 1, 0
		}
		fmt.Fprintf(stderr, "MISMATCH %s  local=%s remote=%s\n",
			archivePath, short(localSum), short(remoteSum))
		return 1, 1
	}

	// As-is folder upload — iRODS object is a collection. No per-call iron
	// checksum exists for collections. We fall back to LOCAL Merkle drift
	// detection, which is what `mus check` already does. Surface that with
	// a clear status so users know nothing is being compared against iRODS.
	dataPath := sidecar.DataPath(scPath)
	st, err := os.Stat(dataPath)
	if err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "MISSING  %s  (source folder absent)\n", dataPath)
		return 0, 1
	}
	if doc.Folder == nil || doc.Folder.RecursiveSha256 == "" {
		fmt.Fprintf(stderr, "STALE    %s  (sidecar has no recursive_sha256)\n", dataPath)
		return 0, 1
	}
	actual, err := folder.RecursiveSHA256(dataPath, cache.Sum)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR    %s: %v\n", dataPath, err)
		return 0, 1
	}
	if !strings.EqualFold(actual, doc.Folder.RecursiveSha256) {
		fmt.Fprintf(stderr, "MISMATCH %s  local Merkle drift (now=%s, sidecar=%s)\n",
			dataPath, short(actual), short(doc.Folder.RecursiveSha256))
		// Local already disagrees with sidecar; --deep wouldn't add info.
		return 1, 1
	}

	if !deep {
		fmt.Fprintf(stdout,
			"OK       %s  (local Merkle ok; pass --deep for per-file iRODS comparison)\n",
			dataPath)
		return 1, 0
	}

	// Deep mode: walk the remote collection's tree, build a map of remote
	// relpath → checksum, then compare every local file to its remote
	// counterpart. Slow but exact.
	mism, miss, extra, derr := deepFolderCheckAgainstIRODS(ctx, cache, client,
		dataPath, doc.IRODS.Path, stdout, stderr)
	if derr != nil {
		fmt.Fprintf(stderr, "ERROR    %s deep check: %v\n", dataPath, derr)
		return 1, 1
	}
	if mism+miss+extra > 0 {
		fmt.Fprintf(stderr,
			"MISMATCH %s  per-file deep check: %d mismatched, %d missing on iRODS, %d extra on iRODS\n",
			dataPath, mism, miss, extra)
		return 1, 1
	}
	fmt.Fprintf(stdout, "OK       %s  (deep: every file matches iRODS)\n", dataPath)
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
