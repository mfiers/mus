package cli

// `mus s3 upload` — h5ad-only, hard-gated deposit. Writes to the
// single bucket configured by `mus s3 login`, using the cascade's
// data_project as the key prefix.
//
// Gates (every one must pass before any bytes leave the local
// machine):
//
//   1. file ends in .h5ad
//   2. data_project is set in the cascade (becomes the key prefix)
//   3. -m / --message provided
//   4. configured bucket exists (HeadBucket) — caught at login but
//      re-checked here in case the bucket was deleted since
//   5. `mus h5ad check` has stamped a fresh PASS on the sidecar —
//      file sha + vocab sha + check version all current. Reuses the
//      gateH5adForUpload logic that `mus irods upload` already uses.
//
// On a clean pass:
//   - multipart-upload the h5ad to
//     s3://<configured_bucket>/<data_project>/<basename>
//   - stamp the same mus_* metadata set as the iRODS AVU (organism,
//     tissue, technology, …, upload message) as x-amz-meta-mus-*
//   - refresh the sidecar with s3_url / s3_bucket / s3_key /
//     s3_etag / s3_uploaded_at + persist
//   - print one line per file: "+ s3://<bucket>/<key>"

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/envformat"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/localprojects"
	"codeberg.org/mfiers/mus/internal/s3client"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

func newS3UploadCmd() *cobra.Command {
	var message string
	var forceH5adGate bool
	var forceOverwrite bool
	var staticFiles []string
	var subfolder string
	cmd := &cobra.Command{
		Use:   "upload -m MESSAGE [--subfolder NAME] [--static FILE]... [FILE_OR_FOLDER...]",
		Short: "upload h5ad / DE / static file(s) to s3://<bucket>/__incoming/<data_project>/",
		Long: "Uploads the listed file(s) and/or folder(s) to the bucket\n" +
			"configured by `mus s3 login`. Every mus S3 upload lands under\n" +
			"the fixed staging prefix __incoming/<data_project>/ — depositor\n" +
			"(or a downstream process) later promotes from there to the\n" +
			"catalog location.\n\n" +
			"Accepted positional args (file OR folder; folders recurse):\n" +
			"  - .h5ad                           → h5ad gate, h5ad-shape S3 key\n" +
			"  - .tsv / .csv / .tsv.gz / .csv.gz → DE gate, de/<parent-dir>/ S3 key\n\n" +
			"Layout on S3:\n\n" +
			"  s3://<bucket>/__incoming/<data_project>/<basename>.h5ad\n" +
			"  s3://<bucket>/__incoming/<data_project>/<basename>.h5ad.mus           (collapsed sidecar)\n" +
			"  s3://<bucket>/__incoming/<data_project>/<subfolder>/<basename>.h5ad   (--subfolder NAME)\n" +
			"  s3://<bucket>/__incoming/<data_project>/de/<parent-dir>/<basename>    (DE file; <parent-dir>\n" +
			"                                                                         is the local folder\n" +
			"                                                                         the DE file came from)\n" +
			"  s3://<bucket>/__incoming/<data_project>/de/<parent-dir>/<basename>.mus (collapsed DE sidecar)\n" +
			"  s3://<bucket>/__incoming/<data_project>/static/<basename>             (--static FILE)\n\n" +
			"COLLAPSED SIDECAR (<basename>.mus next to every primary):\n" +
			"Single KEY=VALUE file holding the EFFECTIVE metadata at upload\n" +
			"time — every mus.cfg key in the cascade (root-most → deepest,\n" +
			"deeper overrides) overlaid with the file's own .mus sidecar\n" +
			"(sidecar wins for scalar conflicts; list keys merge per the\n" +
			"cascade semantics). Includes the freshly-stamped s3_* block.\n\n" +
			"GATES — refused atomically (no partial uploads):\n" +
			"  - h5ad positionals must carry a fresh `mus h5ad check` PASS\n" +
			"    (file sha + vocab sha + check version all current);\n" +
			"    --force-h5ad-upload bypasses (NOT recommended)\n" +
			"  - DE positionals must carry a fresh `mus de check` PASS\n" +
			"    (file sha + de.CheckVersion both current); no bypass — the\n" +
			"    check is cheap (cached-pass is stat-fast)\n" +
			"  - data_project is set in the cascade\n" +
			"  - configured bucket exists on S3\n" +
			"  - -m / --message describes why this upload is happening\n\n" +
			"--subfolder NAME (optional, h5ad-only): writes the h5ad + its\n" +
			"collapsed sidecar under __incoming/<dp>/NAME/. Single path\n" +
			"segment only; \"static\", \".\", and \"..\" are reserved. DE files\n" +
			"ignore --subfolder; they use their local parent-dir name as the\n" +
			"<de_folder> segment.\n\n" +
			"--static FILE (repeatable): ALWAYS lands at\n" +
			"__incoming/<data_project>/static/<basename> regardless of\n" +
			"--subfolder. Static files are auxiliary assets (images, JSON,\n" +
			"PDFs) — no h5ad gate, no sidecar update, no collapsed sidecar\n" +
			"uploaded. Only the upload message lands as x-amz-meta. Static-\n" +
			"only invocations (no positional args) are accepted.\n\n" +
			"Mus_* biology metadata (organism, tissue, technology, …) is\n" +
			"stamped on each S3 object as x-amz-meta-* headers, mirroring\n" +
			"the iRODS AVU set. Each primary's local *.mus sidecar gets\n" +
			"s3_url / s3_bucket / s3_key / s3_etag / s3_uploaded_at written.",
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := strings.TrimSpace(message)
			if msg == "" {
				return fmt.Errorf("`mus s3 upload` requires a -m / --message describing why this upload is happening")
			}
			if len(args) == 0 && len(staticFiles) == 0 {
				return fmt.Errorf("`mus s3 upload` requires at least one file/folder positional or --static FILE")
			}
			cleanSub, err := validateSubfolder(subfolder)
			if err != nil {
				return err
			}
			return runS3Upload(cmd, args, staticFiles, cleanSub, msg, forceH5adGate, forceOverwrite)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"upload message describing what / why (REQUIRED)")
	_ = cmd.MarkFlagRequired("message")
	cmd.Flags().BoolVar(&forceH5adGate, "force-h5ad-upload", false,
		"bypass the .h5ad sanity-check gate (NOT recommended — depositor's responsibility)")
	cmd.Flags().BoolVar(&forceOverwrite, "force", false,
		"overwrite existing S3 objects without prompting (default: skip on identical bytes, "+
			"prompt on differing bytes, refuse non-TTY)")
	cmd.Flags().StringArrayVarP(&staticFiles, "static", "s", nil,
		"additional file to upload to <data_project>/static/ (repeatable; basename is the S3 key)")
	cmd.Flags().StringVar(&subfolder, "subfolder", "",
		"write h5ad + metadata archive under <data_project>/<NAME>/ (single segment; 'static' is reserved)")
	return cmd
}

// validateSubfolder sanitises the --subfolder flag value. Returns
// "" for empty input (meaning "use the data_project root"). Errors
// on multi-segment paths, "." / "..", or "static" (reserved for the
// --static auxiliary-assets prefix).
func validateSubfolder(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, "/\\") {
		return "", fmt.Errorf(
			"--subfolder %q: must be a single path component (no '/' or '\\')", raw)
	}
	if s == "." || s == ".." {
		return "", fmt.Errorf("--subfolder %q: '.' and '..' are not allowed", raw)
	}
	if s == "static" {
		return "", fmt.Errorf(
			"--subfolder %q: 'static' is reserved for --static uploads", raw)
	}
	return s, nil
}

// s3IncomingPrefix is the staging prefix every mus S3 upload lands
// under. By policy, mus only writes to this prefix — the depositor
// (or some downstream process) later promotes the data to its final
// location. Hard-coded, NOT configurable: keeping all in-flight
// deposits under one well-known prefix is the whole point.
const s3IncomingPrefix = "__incoming"

// s3KeyForDE builds the canonical S3 key for a differential-
// expression deposit. The shape is:
//
//	__incoming/<data_project>/de/<de_folder>/<basename>
//
// where `de_folder` is the basename of the local directory the
// DE file came from (typically the analysis name — main_model,
// sex_stratified, etc.). Two DE runs against the same target
// h5ad land under different prefixes when they live in different
// local folders. Both `de_folder` and `basename` MUST be final
// path components.
func s3KeyForDE(dataProject, deFolder, basename string) string {
	if strings.ContainsAny(basename, "/\\") {
		panic(fmt.Sprintf(
			"s3KeyForDE: basename must be a final path component, got %q", basename))
	}
	if strings.ContainsAny(deFolder, "/\\") {
		panic(fmt.Sprintf(
			"s3KeyForDE: de_folder must be a single segment, got %q", deFolder))
	}
	return s3IncomingPrefix + "/" + dataProject + "/de/" + deFolder + "/" + basename
}

// s3KeyFor builds the canonical S3 key for a mus deposit. The key
// shape is always:
//
//	__incoming/<data_project>/[<subfolder>/]<basename>
//
// subfolder is one of: "" (data_project root), "static" (auxiliary
// --static uploads), or a depositor-supplied single-segment name
// from --subfolder. basename MUST be a final path component —
// coding-bug violations panic with a clear message. The "no path
// separators in subfolder" check is a belt-and-braces rerun of
// validateSubfolder so callers can't ship a path through by
// accident.
func s3KeyFor(dataProject, subfolder, basename string) string {
	if strings.ContainsAny(basename, "/\\") {
		panic(fmt.Sprintf(
			"s3KeyFor: basename must be a final path component, got %q", basename))
	}
	if subfolder == "" {
		return s3IncomingPrefix + "/" + dataProject + "/" + basename
	}
	if strings.ContainsAny(subfolder, "/\\") {
		panic(fmt.Sprintf(
			"s3KeyFor: subfolder must be a single segment, got %q", subfolder))
	}
	return s3IncomingPrefix + "/" + dataProject + "/" + subfolder + "/" + basename
}

// buildCollapsedSidecar produces the per-h5ad metadata blob that
// rides alongside the primary on S3. It's a single KEY=VALUE flat
// file (same grammar as the .h5ad.mus sidecar) holding the
// EFFECTIVE metadata for this file at upload time:
//
//   - every mus.cfg key in the cascade (root-most first; deeper
//     overrides shallower) is the base layer
//   - the file's own .h5ad.mus sidecar is overlaid on top — sidecar
//     wins for any scalar conflict; list-typed keys merge with the
//     usual append/`-prefix-removal` cascade semantics
//
// The result is what a fresh `mus config show` plus the freshly-
// stamped Bio/H5ad/S3 blocks would jointly say. Used in place of
// the per-folder tar.gz archive on the S3 path — the depositor's
// downstream "move to proper location" step can read one .mus per
// h5ad instead of unpacking an archive.
//
// MUST be called AFTER the primary has uploaded and the local
// sidecar has been re-written with the fresh s3_* block, so the
// snapshot carries the S3 pointer.
func buildCollapsedSidecar(h5adPath string) ([]byte, error) {
	dir := filepath.Dir(h5adPath)
	env, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load cascade for %s: %w", dir, err)
	}
	merged := env.All()

	scPath := sidecar.SidecarPath(h5adPath)
	raw, err := os.ReadFile(scPath)
	if err != nil {
		return nil, fmt.Errorf("read sidecar %s: %w", scPath, err)
	}
	sidecarMap, err := envformat.Parse(raw, sidecar.ParseOpts())
	if err != nil {
		return nil, fmt.Errorf("parse sidecar %s: %w", scPath, err)
	}
	envformat.Resolve(sidecarMap, sidecar.ParseOpts())

	// Sidecar overlays cascade: most-specific source wins.
	envformat.MergeInto(merged, sidecarMap, sidecar.ParseOpts())
	return envformat.Marshal(merged)
}

// uploadKind classifies each positional argument by destination
// shape on S3. Used by `mus s3 upload` to route h5ad vs DE files
// per file rather than refusing the whole batch when a non-h5ad
// shows up.
type uploadKind int

const (
	uploadKindH5ad uploadKind = iota + 1
	uploadKindDE
)

// uploadEntry is one resolved positional → upload target. Folder
// positionals expand into multiple entries; file positionals into
// one.
type uploadEntry struct {
	AbsPath string
	Kind    uploadKind
	// ParentDir is the basename of the directory holding the file.
	// For DE files this becomes the <de_folder> segment in the S3
	// key: `__incoming/<dp>/de/<ParentDir>/<basename>`. Unused for
	// h5ad entries.
	ParentDir string
}

// classifyUploadArgs expands folder positionals (recursive walk)
// and classifies each file by extension. Returns one uploadEntry
// per file. .mus sidecars are skipped (they ride alongside their
// data file as collapsed sidecars built at upload time).
//
// Errors when:
//   - a file positional has an extension mus doesn't support
//   - a folder positional contains zero supported files
//   - a path doesn't exist
//
// Folder positionals that contain a mix of h5ad + DE files are
// fine — each lands at its own S3 prefix.
func classifyUploadArgs(args []string) ([]uploadEntry, error) {
	if len(args) == 0 {
		return nil, nil
	}
	var out []uploadEntry
	seen := map[string]bool{}
	add := func(abs string, kind uploadKind) {
		if seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, uploadEntry{
			AbsPath:   abs,
			Kind:      kind,
			ParentDir: filepath.Base(filepath.Dir(abs)),
		})
	}
	for _, raw := range args {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", raw, err)
		}
		if st.IsDir() {
			folderHits := 0
			err := filepath.WalkDir(abs, func(p string, d os.DirEntry, werr error) error {
				if werr != nil {
					return werr
				}
				if d.IsDir() {
					return nil
				}
				if sidecar.IsSidecar(p) {
					return nil
				}
				if k, ok := classifyFileExt(p); ok {
					add(p, k)
					folderHits++
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("walk %s: %w", abs, err)
			}
			if folderHits == 0 {
				return nil, fmt.Errorf("%s: no .h5ad / .tsv / .csv / .tsv.gz / .csv.gz files found", abs)
			}
			continue
		}
		k, ok := classifyFileExt(abs)
		if !ok {
			return nil, fmt.Errorf(
				"%s: unsupported file extension — mus s3 upload accepts .h5ad / .tsv / .csv / .tsv.gz / .csv.gz "+
					"(use --static for any other file)", abs)
		}
		add(abs, k)
	}
	return out, nil
}

// classifyFileExt picks an uploadKind for `path` based on its
// extension. Returns (_, false) for anything mus s3 upload doesn't
// handle directly. Compressed variants share the kind of the
// inner extension.
func classifyFileExt(path string) (uploadKind, bool) {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".h5ad") {
		return uploadKindH5ad, true
	}
	for _, ext := range []string{".tsv", ".csv", ".tsv.gz", ".csv.gz"} {
		if strings.HasSuffix(lower, ext) {
			return uploadKindDE, true
		}
	}
	return 0, false
}

// validateStaticFiles checks that every --static path is a regular
// existing file with a unique basename across the list (since they
// all land in the same <data_project>/static/ prefix and a duplicate
// basename would silently clobber).
func validateStaticFiles(paths []string) error {
	seen := map[string]string{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("--static %s: %w", p, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("--static %s: %w", p, err)
		}
		if st.IsDir() {
			return fmt.Errorf("--static %s: is a directory (only regular files)", p)
		}
		base := filepath.Base(abs)
		if prev, dup := seen[base]; dup {
			return fmt.Errorf(
				"--static collision: %s and %s share basename %q "+
					"(both would land at <data_project>/static/%s — refusing)",
				prev, abs, base, base)
		}
		seen[base] = abs
	}
	return nil
}

func runS3Upload(cmd *cobra.Command, args, staticFiles []string, subfolder, message string, forceH5adGate, forceOverwrite bool) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	ctx := cmd.Context()

	// Classify each positional (file or folder) into typed
	// uploadEntries. Folder args expand recursively. Unknown
	// extensions in a file arg → hard error; in a folder arg →
	// silently skipped (we only refuse a folder that contains
	// ZERO supported files).
	entries, err := classifyUploadArgs(args)
	if err != nil {
		return err
	}
	if len(entries) == 0 && len(staticFiles) == 0 {
		return fmt.Errorf("`mus s3 upload` requires at least one .h5ad / .tsv / .csv file or --static FILE")
	}

	if err := validateStaticFiles(staticFiles); err != nil {
		return err
	}
	// Refuse multi-source-folder invocations for h5ad entries —
	// h5ads share a key under <data_project>/, so two h5ads with
	// the same basename in different source folders would collide.
	// DE files don't have this problem (they land under
	// de/<parent-dir>/<basename>, scoped by parent dir).
	h5adArgs := h5adArgsOnly(entries)
	if err := requireSingleSourceFolder(h5adArgs); err != nil {
		return err
	}

	// Cascade → data_project → bucket name. data_project is also
	// required for the iRODS path component; reuse the same lookup
	// + error wording.
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
	dataProject := env.String("data_project")
	if dataProject == "" {
		return fmt.Errorf("data_project not set — `mus config set data_project NameYear` (or `mus eln link <id>`)")
	}

	// h5ad freshness gate (same logic as `mus irods upload`).
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()
	if len(h5adArgs) > 0 {
		failures, err := gateH5adForUpload(ctx, cache, h5adArgs, forceH5adGate)
		if err != nil {
			return err
		}
		if len(failures) > 0 {
			return formatH5adGateError(failures)
		}
	}
	// DE freshness gate — every DE file must carry a cached PASS
	// from `mus de check`. No --force flag; depositor re-runs the
	// check (cheap on a stat-fast cached pass).
	deArgs := deArgsOnly(entries)
	if len(deArgs) > 0 {
		deFailures, err := gateDEForUpload(cache, deArgs)
		if err != nil {
			return err
		}
		if len(deFailures) > 0 {
			return formatDEGateError(deFailures)
		}
	}

	// S3 client + bucket existence check (do this BEFORE any upload
	// so we fail fast on missing bucket).
	store, err := openSecrets()
	if err != nil {
		return err
	}
	cfg, err := loadS3Config(store)
	if err != nil {
		return err
	}
	client, err := s3client.New(ctx, cfg)
	if err != nil {
		return err
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return fmt.Errorf("HeadBucket %s: %w", cfg.Bucket, err)
	}
	if !exists {
		return fmt.Errorf(
			"bucket %q does not exist on %s (or no permission).\n"+
				"  Re-run `mus s3 login` after creating it on the provider.",
			cfg.Bucket, cfg.Endpoint)
	}

	// Upload loop — primaries + a per-file collapsed sidecar. Each
	// uploadEntry carries a Kind so we route h5ad and DE files to
	// their respective S3 key shapes. Failure on any primary is
	// fatal (returns), but a collapsed-sidecar failure is logged
	// and the loop continues — the primary + x-amz-meta have
	// already landed and that's the load-bearing artefact.
	for _, ent := range entries {
		dataKey, sidecarKey, err := s3KeysFor(ent, dataProject, subfolder)
		if err != nil {
			return err
		}
		scPath := sidecar.SidecarPath(ent.AbsPath)
		doc, err := sidecar.Read(scPath)
		if err != nil {
			return fmt.Errorf("read sidecar %s: %w", scPath, err)
		}
		if doc == nil {
			return fmt.Errorf("%s: missing sidecar (run `mus %s check` first)",
				ent.AbsPath, checkSubcommandFor(ent.Kind))
		}

		// Fold cascade biology so the object metadata is populated
		// even when the sidecar hasn't been re-stamped recently.
		stampBioFromEnv(doc, env)
		meta := s3MetadataFromDoc(doc, message)

		fmt.Fprintf(stdout, "→ s3://%s/%s\n", cfg.Bucket, dataKey)

		// Pre-upload state check: HeadObject the destination. If
		// the remote already exists AND its x-amz-meta-mus_sha256
		// matches our local sha, skip the upload silently (refresh
		// the sidecar to record the round-trip, but don't transfer
		// bytes). On a sha mismatch, prompt the user — non-TTY
		// callers refuse without --force. Mirrors the iRODS-side
		// 0.2.27 behaviour for cross-bucket consistency.
		state, headErr := inspectS3RemoteForUpload(ctx, client, cfg.Bucket, dataKey, doc.File.Sha256)
		if headErr != nil {
			// Head failed for some other reason — log and continue
			// to the upload. Don't let a flaky head abort the deposit.
			fmt.Fprintf(stderr,
				"  ⚠ HeadObject probe failed: %v (proceeding with upload)\n", headErr)
		}
		var result *s3client.UploadResult
		switch state {
		case s3RemoteSameBytes:
			fmt.Fprintf(stdout,
				"  ✓ sha256 matches remote — skipping upload (sidecar will be refreshed)\n")
			// Synthesize a result so the downstream sidecar refresh
			// records the existing object's location.
			info, _ := client.HeadObjectExtended(ctx, cfg.Bucket, dataKey)
			result = &s3client.UploadResult{
				Bucket: cfg.Bucket,
				Key:    dataKey,
				URL:    s3URLFor(cfg.Endpoint, cfg.Bucket, dataKey),
				ETag:   info.ETag,
			}
		case s3RemoteDiffers:
			if !forceOverwrite && !promptOverwrite(os.Stdin, stdout,
				fmt.Sprintf("s3://%s/%s", cfg.Bucket, dataKey)) {
				return fmt.Errorf(
					"s3://%s/%s exists with different bytes — overwrite refused.\n"+
						"  Pass --force to overwrite without prompt.",
					cfg.Bucket, dataKey)
			}
			r, err := client.Upload(ctx, cfg.Bucket, dataKey, ent.AbsPath, meta)
			if err != nil {
				fmt.Fprintf(stderr, "  ✘ upload failed for %s: %v\n", ent.AbsPath, err)
				return err
			}
			result = r
			fmt.Fprintf(stdout, "  ✓ etag=%s\n", strings.Trim(result.ETag, `"`))
		default: // s3RemoteAbsent (or head failed and we're treating as absent)
			r, err := client.Upload(ctx, cfg.Bucket, dataKey, ent.AbsPath, meta)
			if err != nil {
				fmt.Fprintf(stderr, "  ✘ upload failed for %s: %v\n", ent.AbsPath, err)
				return err
			}
			result = r
			fmt.Fprintf(stdout, "  ✓ etag=%s\n", strings.Trim(result.ETag, `"`))
		}

		// Refresh sidecar with S3 pointer BEFORE building the
		// collapsed .mus so the snapshot includes s3_url / s3_etag.
		if doc.S3 == nil {
			doc.S3 = &sidecar.S3{}
		}
		doc.S3.Bucket = result.Bucket
		doc.S3.Key = result.Key
		doc.S3.URL = result.URL
		doc.S3.Etag = strings.Trim(result.ETag, `"`)
		doc.S3.UploadedAt = time.Now().UTC().Truncate(time.Second)
		if err := sidecar.Write(scPath, doc); err != nil {
			return fmt.Errorf("update sidecar %s: %w", scPath, err)
		}
		// Update the local project-log so `mus irods projects --details`
		// can later show "the most recent local folder you uploaded
		// from for project X". Best-effort — silent on every error.
		if lp, err := localprojects.Open(""); err == nil {
			lp.RecordBestEffort(dataProject, filepath.Dir(ent.AbsPath))
		}

		// Build + upload the collapsed sidecar next to the primary.
		// Same KEY=VALUE grammar as the on-disk .mus; cascade is
		// folded in so the depositor (or a downstream `mv`) can
		// pair data + sidecar without referencing the cascade
		// tree. Errors here log + continue — primary already on S3.
		collapsed, err := buildCollapsedSidecar(ent.AbsPath)
		if err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ collapsed sidecar build failed for %s: %v — primary already uploaded\n",
				ent.AbsPath, err)
			continue
		}
		tmpFile, err := os.CreateTemp("", "mus-collapsed-sidecar-*.mus")
		if err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ tempfile for collapsed sidecar failed for %s: %v\n", ent.AbsPath, err)
			continue
		}
		tmpPath := tmpFile.Name()
		_, writeErr := tmpFile.Write(collapsed)
		closeErr := tmpFile.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(tmpPath)
			fmt.Fprintf(stderr,
				"  ⚠ write collapsed sidecar failed for %s: %v / %v\n",
				ent.AbsPath, writeErr, closeErr)
			continue
		}
		fmt.Fprintf(stdout, "  + s3://%s/%s\n", cfg.Bucket, sidecarKey)
		if _, err := client.Upload(ctx, cfg.Bucket, sidecarKey, tmpPath, meta); err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ collapsed sidecar upload failed for %s: %v — primary already uploaded\n",
				ent.AbsPath, err)
		}
		os.Remove(tmpPath)
	}

	// Static files: upload to <data_project>/static/<basename>. They
	// carry only mus_upload_message — no biology AVU, no sidecar
	// update, no h5ad gate. The catalog ingester treats this prefix
	// as auxiliary assets (figures, JSON, README PDFs, etc).
	if len(staticFiles) > 0 {
		staticMeta := map[string]string{}
		if msg := strings.TrimSpace(message); msg != "" && !strings.ContainsAny(msg, "\r\n") {
			staticMeta["mus_upload_message"] = msg
		}
		for _, raw := range staticFiles {
			abs, err := filepath.Abs(raw)
			if err != nil {
				return err
			}
			key := s3KeyFor(dataProject, "static", filepath.Base(abs))
			fmt.Fprintf(stdout, "→ s3://%s/%s\n", cfg.Bucket, key)
			result, err := client.Upload(ctx, cfg.Bucket, key, abs, staticMeta)
			if err != nil {
				fmt.Fprintf(stderr, "  ✘ static upload failed for %s: %v\n", abs, err)
				return err
			}
			fmt.Fprintf(stdout, "  ✓ etag=%s\n", strings.Trim(result.ETag, `"`))
		}
	}
	return nil
}

// s3KeysFor returns the (data, sidecar) S3 key pair for one
// uploadEntry. h5ad files land at the configured subfolder under
// __incoming/<dp>/; DE files land at __incoming/<dp>/de/<ParentDir>/.
// Both pairs share a common base — the sidecar key is just the
// data key plus the existing .mus suffix.
func s3KeysFor(ent uploadEntry, dataProject, subfolder string) (dataKey, sidecarKey string, err error) {
	switch ent.Kind {
	case uploadKindH5ad:
		dataKey = s3KeyFor(dataProject, subfolder, filepath.Base(ent.AbsPath))
	case uploadKindDE:
		if ent.ParentDir == "" {
			return "", "", fmt.Errorf(
				"%s: cannot derive de_folder segment (file has no parent directory)",
				ent.AbsPath)
		}
		dataKey = s3KeyForDE(dataProject, ent.ParentDir, filepath.Base(ent.AbsPath))
	default:
		return "", "", fmt.Errorf("%s: unknown upload kind %d (internal bug)",
			ent.AbsPath, ent.Kind)
	}
	sidecarKey = dataKey + ".mus"
	return dataKey, sidecarKey, nil
}

// h5adArgsOnly returns the absolute paths of every uploadEntry
// whose kind is h5ad. Used to feed the existing
// requireSingleSourceFolder / gateH5adForUpload helpers, which
// were written before mus s3 upload accepted DE files.
func h5adArgsOnly(entries []uploadEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Kind == uploadKindH5ad {
			out = append(out, e.AbsPath)
		}
	}
	return out
}

// deArgsOnly is the dual of h5adArgsOnly — every DE-kind entry's
// path. Fed into gateDEForUpload to enforce the cached `mus de
// check` PASS before any DE file's bytes leave the machine.
func deArgsOnly(entries []uploadEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Kind == uploadKindDE {
			out = append(out, e.AbsPath)
		}
	}
	return out
}

// checkSubcommandFor maps an uploadKind to the user-facing
// subcommand a depositor runs to satisfy the gate. Used in error
// messages so the suggested fix is concrete ("run `mus de check`"
// vs "run `mus h5ad check`").
func checkSubcommandFor(k uploadKind) string {
	switch k {
	case uploadKindDE:
		return "de"
	}
	return "h5ad"
}

// s3RemoteState classifies the pre-upload relationship between
// our local file's sha256 and whatever's at the destination S3 key.
// Mirror of remoteUploadState on the iRODS side — different
// transport, same UX contract.
type s3RemoteState int

const (
	s3RemoteAbsent s3RemoteState = iota
	s3RemoteSameBytes
	s3RemoteDiffers
)

// inspectS3RemoteForUpload classifies the destination key relative
// to a local file's sha256. Best-effort: any error returns
// (s3RemoteAbsent, err) — caller can choose to log the error and
// proceed with the upload rather than abort on a flaky head.
//
// Sha comparison is against the x-amz-meta-mus_sha256 metadata
// mus stamps on every PutObject (avuFromDoc → "mus_sha256"). The
// SDK lowercases the header name and strips the x-amz-meta- prefix
// on read, so we look up "mus_sha256" in info.Metadata. ETag is
// NOT a reliable sha256 source (multipart uploads use a hash-of-
// part-etags scheme that doesn't match file sha256).
func inspectS3RemoteForUpload(ctx context.Context, client *s3client.Client,
	bucket, key, localSum string) (s3RemoteState, error) {
	if localSum == "" {
		// No local sha to compare — let the upload run. The
		// downstream sidecar check should have caught this already.
		return s3RemoteAbsent, nil
	}
	info, err := client.HeadObjectExtended(ctx, bucket, key)
	if err != nil {
		return s3RemoteAbsent, err
	}
	if !info.Exists {
		return s3RemoteAbsent, nil
	}
	remoteSha := info.Metadata["mus_sha256"]
	if remoteSha == "" {
		// Object exists but mus didn't stamp it (uploaded outside
		// mus, OR by an older mus version before the AVU set was
		// stable). Treat as "differs" to force a prompt — caller
		// decides whether to overwrite.
		return s3RemoteDiffers, nil
	}
	if strings.EqualFold(remoteSha, localSum) {
		return s3RemoteSameBytes, nil
	}
	return s3RemoteDiffers, nil
}

// s3URLFor reconstructs the browser URL for an S3 object. Cheap
// stringbuild — used when we synthesise an UploadResult after a
// skip-if-same (no real Upload happened to give us a URL).
func s3URLFor(endpoint, bucket, key string) string {
	return strings.TrimRight(endpoint, "/") + "/" + bucket + "/" + key
}

// s3MetadataFromDoc builds the x-amz-meta-* set for an S3 PutObject
// from a sidecar Doc + the upload message. Mirrors the iRODS AVU
// set (avuFromDoc) — same key names with the `mus_` prefix, so a
// catalog reader can pull either path interchangeably.
//
// S3 enforces lowercase header keys and ASCII-only values; mus's
// `mus_<field>` style already complies. Values containing newlines
// are dropped — they'd be rejected by the SDK with a confusing
// error otherwise.
func s3MetadataFromDoc(doc *sidecar.Doc, message string) map[string]string {
	avu := avuFromDoc(doc)
	out := make(map[string]string, len(avu)+1)
	for key, vals := range avu {
		// AVU is multi-value via repeated keys; S3 metadata is a
		// flat map. Join with comma — matches the canonical .env /
		// mus.cfg list serialisation, and any catalog reader using
		// the same vocab knows to split on comma for list fields.
		v := strings.Join(vals, ",")
		if strings.ContainsAny(v, "\r\n") {
			continue
		}
		out[key] = v
	}
	if msg := strings.TrimSpace(message); msg != "" && !strings.ContainsAny(msg, "\r\n") {
		out["mus_upload_message"] = msg
	}
	return out
}

// _ context import keeps the file compilable even if Cobra inlines
// the context handling differently in a future refactor.
var _ = context.Background
