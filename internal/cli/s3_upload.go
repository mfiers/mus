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
	var staticFiles []string
	var subfolder string
	cmd := &cobra.Command{
		Use:   "upload -m MESSAGE [--subfolder NAME] [--static FILE]... [FILE...]",
		Short: "upload h5ad and/or static file(s) to s3://<bucket>/<data_project>/",
		Long: "Uploads each .h5ad positional arg to the bucket configured by\n" +
			"`mus s3 login`. Every mus S3 upload lands under the fixed staging\n" +
			"prefix `__incoming/<data_project>/…` — depositor (or downstream)\n" +
			"later promotes from there to the proper catalog location.\n\n" +
			"Layout:\n\n" +
			"  s3://<bucket>/__incoming/<data_project>/<basename>.h5ad             (no --subfolder)\n" +
			"  s3://<bucket>/__incoming/<data_project>/<basename>.h5ad.mus         (collapsed sidecar; next to each h5ad)\n" +
			"  s3://<bucket>/__incoming/<data_project>/<subfolder>/<basename>.h5ad (with --subfolder NAME)\n" +
			"  s3://<bucket>/__incoming/<data_project>/static/<basename>           (--static FILE)\n\n" +
			"COLLAPSED SIDECAR (.h5ad.mus next to each h5ad):\n" +
			"Single KEY=VALUE file holding the EFFECTIVE metadata at upload\n" +
			"time — every mus.cfg key in the cascade (root-most → deepest,\n" +
			"deeper overrides) overlaid with the file's own .h5ad.mus (sidecar\n" +
			"wins for scalar conflicts; list keys merge per the cascade\n" +
			"semantics). Includes the freshly-stamped s3_* block. Replaces the\n" +
			"previous tar.gz archive — a downstream `mv` keeps data + .mus\n" +
			"paired and the metadata is plain KEY=VALUE, no unpacking needed.\n\n" +
			"--subfolder NAME (optional): writes the h5ad + its collapsed\n" +
			"sidecar under <data_project>/NAME/. Single path segment only;\n" +
			"\"static\", \".\", and \"..\" are reserved / refused.\n\n" +
			"--static FILE (repeatable): ALWAYS lands at\n" +
			"__incoming/<data_project>/static/<basename> regardless of\n" +
			"--subfolder. Static files are auxiliary assets (images, JSON,\n" +
			"PDFs) — no h5ad gate, no sidecar update, no collapsed sidecar\n" +
			"uploaded. Only the upload message lands as x-amz-meta. Static-only\n" +
			"invocations (no positional args) are accepted.\n\n" +
			"Strict gates — every .h5ad positional must pass ALL of:\n\n" +
			"  - filename ends in .h5ad\n" +
			"  - sidecar carries a fresh `mus h5ad check` PASS (file sha +\n" +
			"    vocab sha + check version all current)\n" +
			"  - data_project is set in the cascade (used as the key prefix)\n" +
			"  - configured bucket exists on S3\n" +
			"  - -m / --message describes why this upload is happening\n\n" +
			"Mus_* biology metadata (organism, tissue, technology, …) is stamped\n" +
			"on h5ad S3 objects as x-amz-meta-* headers, mirroring the iRODS AVU\n" +
			"set. The local *.h5ad.mus sidecar gets s3_url / s3_bucket / s3_key /\n" +
			"s3_etag / s3_uploaded_at written.\n\n" +
			"--force-h5ad-upload bypasses the h5ad PASS gate (NOT recommended\n" +
			"— depositor's responsibility to know what they're skipping).",
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := strings.TrimSpace(message)
			if msg == "" {
				return fmt.Errorf("`mus s3 upload` requires a -m / --message describing why this upload is happening")
			}
			if len(args) == 0 && len(staticFiles) == 0 {
				return fmt.Errorf("`mus s3 upload` requires at least one .h5ad file or --static FILE")
			}
			cleanSub, err := validateSubfolder(subfolder)
			if err != nil {
				return err
			}
			return runS3Upload(cmd, args, staticFiles, cleanSub, msg, forceH5adGate)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"upload message describing what / why (REQUIRED)")
	_ = cmd.MarkFlagRequired("message")
	cmd.Flags().BoolVar(&forceH5adGate, "force-h5ad-upload", false,
		"bypass the .h5ad sanity-check gate (NOT recommended — depositor's responsibility)")
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

func runS3Upload(cmd *cobra.Command, args, staticFiles []string, subfolder, message string, forceH5adGate bool) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	ctx := cmd.Context()

	// Pre-flight: every positional must be an .h5ad. --static files
	// are validated separately and may have any extension.
	for _, a := range args {
		if !strings.HasSuffix(a, ".h5ad") {
			return fmt.Errorf("%s: positional args must be .h5ad (use --static for any other file; got %s)",
				a, filepath.Ext(a))
		}
	}
	if err := validateStaticFiles(staticFiles); err != nil {
		return err
	}
	// Refuse multi-source-folder invocations up-front: each folder
	// has its own cascade, and the collapsed sidecars on S3 would
	// quietly inherit whatever cwd the depositor happened to be in.
	// Run once per source folder so the cascade matches.
	if err := requireSingleSourceFolder(args); err != nil {
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
	failures, err := gateH5adForUpload(ctx, cache, args, forceH5adGate)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return formatH5adGateError(failures)
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

	// Upload loop — primaries + a per-h5ad collapsed sidecar. The
	// sidecar carries the merged cascade + freshly-stamped s3_*
	// block, so the depositor can later move data + .mus together
	// without losing context.
	for _, raw := range args {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return err
		}
		scPath := sidecar.SidecarPath(abs)
		doc, err := sidecar.Read(scPath)
		if err != nil {
			return fmt.Errorf("read sidecar %s: %w", scPath, err)
		}
		if doc == nil {
			return fmt.Errorf("%s: missing sidecar (run `mus h5ad check` first)", abs)
		}

		// Fold cascade biology so the object metadata is populated
		// even when the sidecar hasn't been re-stamped recently.
		stampBioFromEnv(doc, env)

		// Key layout: __incoming/<data_project>/[<subfolder>/]<basename>.
		// subfolder is the validated --subfolder flag (empty ⇒ data_project root).
		key := s3KeyFor(dataProject, subfolder, filepath.Base(abs))
		meta := s3MetadataFromDoc(doc, message)

		fmt.Fprintf(stdout, "→ s3://%s/%s\n", cfg.Bucket, key)
		result, err := client.Upload(ctx, cfg.Bucket, key, abs, meta)
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ upload failed for %s: %v\n", abs, err)
			return err
		}
		fmt.Fprintf(stdout, "  ✓ etag=%s\n", strings.Trim(result.ETag, `"`))

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
			lp.RecordBestEffort(dataProject, filepath.Dir(abs))
		}

		// Build the collapsed .mus (cascade + local sidecar merged
		// into one KEY=VALUE blob) and upload next to the primary.
		// Collapsed-sidecar failures log but do NOT roll back the
		// primary — data + x-amz-meta have already landed.
		collapsed, err := buildCollapsedSidecar(abs)
		if err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ collapsed sidecar build failed for %s: %v — primary already uploaded\n",
				abs, err)
			continue
		}
		sidecarKey := s3KeyFor(dataProject, subfolder, filepath.Base(scPath))
		tmpFile, err := os.CreateTemp("", "mus-collapsed-sidecar-*.mus")
		if err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ tempfile for collapsed sidecar failed for %s: %v\n", abs, err)
			continue
		}
		tmpPath := tmpFile.Name()
		_, writeErr := tmpFile.Write(collapsed)
		closeErr := tmpFile.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(tmpPath)
			fmt.Fprintf(stderr,
				"  ⚠ write collapsed sidecar failed for %s: %v / %v\n",
				abs, writeErr, closeErr)
			continue
		}
		fmt.Fprintf(stdout, "  + s3://%s/%s\n", cfg.Bucket, sidecarKey)
		if _, err := client.Upload(ctx, cfg.Bucket, sidecarKey, tmpPath, meta); err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ collapsed sidecar upload failed for %s: %v — primary already uploaded\n",
				abs, err)
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
