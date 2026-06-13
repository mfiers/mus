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
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/s3client"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

func newS3UploadCmd() *cobra.Command {
	var message string
	var forceH5adGate bool
	var staticFiles []string
	cmd := &cobra.Command{
		Use:   "upload -m MESSAGE [--static FILE]... [FILE...]",
		Short: "upload h5ad and/or static file(s) to s3://<bucket>/<data_project>/",
		Long: "Uploads each .h5ad positional arg to the bucket configured by\n" +
			"`mus s3 login`, with data_project from the cascade as the key prefix.\n" +
			"Final layout (the only two subfolders mus ever writes):\n\n" +
			"  s3://<configured_bucket>/<data_project>/<basename>.h5ad\n" +
			"  s3://<configured_bucket>/<data_project>/mus_metadata_<stamp>.tar.gz\n" +
			"  s3://<configured_bucket>/<data_project>/static/<basename>   (--static)\n\n" +
			"Strict gates — every .h5ad arg must pass ALL of:\n\n" +
			"  - filename ends in .h5ad\n" +
			"  - sidecar carries a fresh `mus h5ad check` PASS (file sha +\n" +
			"    vocab sha + check version all current)\n" +
			"  - data_project is set in the cascade (used as the key prefix)\n" +
			"  - configured bucket exists on S3\n" +
			"  - -m / --message describes why this upload is happening\n\n" +
			"--static FILE (repeatable) uploads any file to <data_project>/static/.\n" +
			"Static files are auxiliary assets (images, JSON, etc) — no h5ad gate,\n" +
			"no sidecar update, only the upload message lands as x-amz-meta. Run\n" +
			"with --static alone (no positional args) for a static-only upload.\n\n" +
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
			return runS3Upload(cmd, args, staticFiles, msg, forceH5adGate)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"upload message describing what / why (REQUIRED)")
	_ = cmd.MarkFlagRequired("message")
	cmd.Flags().BoolVar(&forceH5adGate, "force-h5ad-upload", false,
		"bypass the .h5ad sanity-check gate (NOT recommended — depositor's responsibility)")
	cmd.Flags().StringArrayVarP(&staticFiles, "static", "s", nil,
		"additional file to upload to <data_project>/static/ (repeatable; basename is the S3 key)")
	return cmd
}

// s3KeyFor builds the canonical S3 key under data_project. subfolder
// must be one of "" (the data_project root) or "static" (auxiliary
// assets). basename MUST be a final path component, never a path —
// that's the invariant that keeps S3 layout flat to two known
// subfolders. Coding-bug violations panic with a clear message.
func s3KeyFor(dataProject, subfolder, basename string) string {
	if strings.ContainsAny(basename, "/\\") {
		panic(fmt.Sprintf(
			"s3KeyFor: basename must be a final path component, got %q", basename))
	}
	switch subfolder {
	case "":
		return dataProject + "/" + basename
	case "static":
		return dataProject + "/static/" + basename
	default:
		panic(fmt.Sprintf(
			"s3KeyFor: unknown subfolder %q (allowed: \"\", \"static\")", subfolder))
	}
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

func runS3Upload(cmd *cobra.Command, args, staticFiles []string, message string, forceH5adGate bool) error {
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
	// would build a `mus_metadata_<stamp>.tar.gz` with the same
	// basename under the same `<data_project>/` prefix and the
	// second PUT would overwrite the first. Applies to .h5ad
	// positionals only — --static files don't generate archives.
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

	// One invocation = one timestamp shared across every metadata
	// archive built this run, so a batch of files from multiple
	// folders produces a coherent set of dated archives.
	invocationStamp := time.Now().UTC()
	// Track primaries per source folder so we can build one
	// archive per folder AFTER all primaries from that folder
	// have uploaded (sidecars refreshed with s3_* block first).
	primariesByFolder := map[string][]string{}

	// Upload loop — primaries only. Companions go up later via
	// the per-folder metadata archive.
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

		// Key layout: <data_project>/<basename> within the
		// configured bucket. s3KeyFor enforces the no-subfolder
		// invariant — see its doc.
		key := s3KeyFor(dataProject, "", filepath.Base(abs))
		meta := s3MetadataFromDoc(doc, message)

		fmt.Fprintf(stdout, "→ s3://%s/%s\n", cfg.Bucket, key)
		result, err := client.Upload(ctx, cfg.Bucket, key, abs, meta)
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ upload failed for %s: %v\n", abs, err)
			return err
		}
		fmt.Fprintf(stdout, "  ✓ etag=%s\n", strings.Trim(result.ETag, `"`))

		// Refresh sidecar with S3 pointer.
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
		primariesByFolder[filepath.Dir(abs)] = append(primariesByFolder[filepath.Dir(abs)], abs)
	}

	// Per source folder: build the metadata archive (now with
	// freshly-updated sidecars) and PUT it. Archive failures log
	// a warning but don't roll back the primary uploads — the
	// data + AVU / x-amz-meta has already landed.
	for folder, primaries := range primariesByFolder {
		archivePath, cleanup, err := buildMetadataArchive(folder, primaries, invocationStamp, true)
		if err != nil {
			fmt.Fprintf(stderr, "  ⚠ metadata archive build failed for %s: %v\n", folder, err)
			continue
		}
		if archivePath == "" {
			cleanup()
			continue // nothing to archive
		}
		archiveKey := s3KeyFor(dataProject, "", filepath.Base(archivePath))
		fmt.Fprintf(stdout, "  + s3://%s/%s\n", cfg.Bucket, archiveKey)
		if _, err := client.Upload(ctx, cfg.Bucket, archiveKey, archivePath, nil); err != nil {
			fmt.Fprintf(stderr, "  ⚠ metadata archive upload failed: %v — primaries already uploaded\n", err)
		}
		cleanup()
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
