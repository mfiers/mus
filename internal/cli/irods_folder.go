package cli

// Folder-upload code path. `mus irods upload <folder>` enters here when a
// positional argument is a directory; regular files take the existing flat
// code path in irods.go.
//
// The flow:
//
//  1. Profile the folder (count, total size, median, density).
//  2. If density check fails (too many small files), prompt for action:
//       (t)ar.gz, (a)s-is, (c)ancel
//     Non-TTY callers must pass --pack tar.gz / --pack none — refuse loudly
//     otherwise.
//  3. If packed: build <folder>.tar.gz next to the source folder, upload
//     the single archive object.
//  4. If as-is: `iron upload` the folder to the same remote collection.
//  5. Either way, stat the uploaded object/collection to grab the catalog
//     ID, derive the PURL, walk the source for a recursive_sha256.
//  6. Write ONE folder-level sidecar at <folder>.mus (sibling of the
//     folder). No per-file sidecars are produced inside the folder.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/folder"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// packMode is the --pack flag value.
type packMode string

const (
	packAuto   packMode = "auto"   // prompt on TTY, refuse on non-TTY (default)
	packTarGz  packMode = "tar.gz" // always archive
	packNone   packMode = "none"   // never archive
	packPrompt packMode = "prompt" // alias for auto, kept for clarity in tests
)

// resolvePackMode parses the user's --pack value into the canonical packMode.
func resolvePackMode(raw string) (packMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return packAuto, nil
	case "tar.gz", "targz", "tar":
		return packTarGz, nil
	case "none", "no", "asis", "as-is":
		return packNone, nil
	}
	return "", fmt.Errorf("invalid --pack value %q (use: auto, tar.gz, none)", raw)
}

// runIRODSUploadFolder handles a single folder argument. Most of the
// parameters mirror runIRODSUpload's; collected here to keep that function
// readable.
func runIRODSUploadFolder(
	cmd *cobra.Command,
	srcFolder string,
	opts iron.UploadOpts,
	remoteCollection string,
	env *config.Env,
	mode packMode,
) error {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	srcAbs, err := filepath.Abs(srcFolder)
	if err != nil {
		return err
	}
	st, err := os.Stat(srcAbs)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", srcAbs)
	}

	prof, err := folder.ProfileFolder(srcAbs)
	if err != nil {
		return fmt.Errorf("profile %s: %w", srcAbs, err)
	}
	minDensity := readFloat(env, "irods_upload_min_density", 10.0)
	minCount := readInt64(env, "irods_upload_min_density_count", 20)

	densityErr := folder.DensityOK(prof, minDensity, minCount)

	fmt.Fprintf(stdout, "%s — %d files, total %s, median %s\n",
		srcAbs, prof.FileCount,
		folder.FormatHumanBytes(prof.TotalBytes),
		folder.FormatHumanBytes(prof.MedianSize))
	if densityErr != nil {
		fmt.Fprintf(stdout, "  %s\n", densityErr)
	}

	// Decide packing strategy.
	chosen := mode
	if chosen == packAuto {
		switch {
		case densityErr == nil:
			chosen = packNone // direct upload — density is fine
		case !stdinIsTTY(stdin):
			return fmt.Errorf(
				"density check failed and no TTY available for prompt.\n"+
					"  Re-run with one of:\n"+
					"    --pack tar.gz   (bundle into <name>.tar.gz and upload)\n"+
					"    --pack none     (upload as-is regardless of density)\n"+
					"  Profile: %d files, median %s, density=%.4f (need ≥ %.0f)",
				prof.FileCount,
				folder.FormatHumanBytes(prof.MedianSize),
				prof.Density, minDensity)
		default:
			c, err := promptPackChoice(stdin, stdout)
			if err != nil {
				return err
			}
			chosen = c
			if chosen == "cancel" {
				return errors.New("upload cancelled")
			}
		}
	}

	cleanup, _ := cmd.Flags().GetBool("cleanup-archive")
	switch chosen {
	case packTarGz:
		return uploadFolderArchived(cmd, srcAbs, remoteCollection, opts, env, prof, cleanup)
	case packNone:
		return uploadFolderDirect(cmd, srcAbs, remoteCollection, opts, env, prof)
	}
	_ = stderr
	return fmt.Errorf("unknown pack mode %q", chosen)
}

// promptPackChoice prompts the user to choose between tar.gz, as-is, cancel.
// Loops up to 3 times on invalid input.
func promptPackChoice(stdin io.Reader, stdout io.Writer) (packMode, error) {
	r := bufio.NewReader(stdin)
	for attempts := 0; attempts < 3; attempts++ {
		fmt.Fprintf(stdout, "  → too many small files for direct upload; pick:\n")
		fmt.Fprintf(stdout, "    [t] pack as tar.gz and upload the archive\n")
		fmt.Fprintf(stdout, "    [a] upload as-is anyway\n")
		fmt.Fprintf(stdout, "    [c] cancel\n")
		fmt.Fprintf(stdout, "  Choice [t/a/c]: ")
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "t", "tar", "tar.gz", "targz":
			return packTarGz, nil
		case "a", "as-is", "asis":
			return packNone, nil
		case "c", "cancel", "":
			return "cancel", nil
		}
		fmt.Fprintln(stdout, "  invalid choice")
	}
	return "", errors.New("too many invalid choices")
}

// uploadFolderDirect uploads the source folder tree as a collection on
// iRODS, then writes one folder-level sidecar.
func uploadFolderDirect(
	cmd *cobra.Command,
	srcAbs, remoteCollection string,
	opts iron.UploadOpts,
	env *config.Env,
	prof *folder.Profile,
) error {
	client, err := iron.New()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	remoteBase := filepath.Base(srcAbs)
	// IRON disambiguates "upload as subcollection vs upload contents" by the
	// trailing slash on the TARGET. `iron upload /local/scripts /remote/dst/`
	// → creates /remote/dst/scripts/. We use that form.
	remoteTarget := remoteCollection + "/"
	remotePath := remoteCollection + "/" + remoteBase
	fmt.Fprintf(cmd.OutOrStdout(), "→ %s/\n", remotePath)
	if err := client.Mkdir(ctx, remoteCollection); err != nil {
		return fmt.Errorf("imkdir %s: %w", remoteCollection, err)
	}
	if err := client.Upload(ctx, srcAbs, remoteTarget, opts); err != nil {
		return err
	}
	return finalizeFolderSidecar(cmd, srcAbs, remotePath, env, prof, nil, client, ctx)
}

// uploadFolderArchived builds <basename>.tar.gz, uploads the archive, and
// writes one folder-level sidecar describing both the source folder
// (recursive_sha256, file_count, …) and the archive object on iRODS.
func uploadFolderArchived(
	cmd *cobra.Command,
	srcAbs, remoteCollection string,
	opts iron.UploadOpts,
	env *config.Env,
	prof *folder.Profile,
	cleanupAfter bool,
) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	base := filepath.Base(srcAbs)
	archiveLocal := filepath.Join(filepath.Dir(srcAbs), base+".tar.gz")

	if _, err := os.Stat(archiveLocal); err == nil {
		return fmt.Errorf("%s already exists; remove it or pick a different name",
			archiveLocal)
	}

	fmt.Fprintf(stdout, "  packing %s → %s\n", base, filepath.Base(archiveLocal))
	t0 := time.Now()
	res, err := folder.TarGzip(srcAbs, archiveLocal)
	if err != nil {
		return fmt.Errorf("tar.gz: %w", err)
	}
	fmt.Fprintf(stdout, "  packed in %s: %s (sha256 %s)\n",
		time.Since(t0).Round(time.Millisecond),
		folder.FormatHumanBytes(res.Size),
		res.Sha256[:12])

	client, err := iron.New()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	remotePath := remoteCollection + "/" + filepath.Base(archiveLocal)
	fmt.Fprintf(stdout, "→ %s\n", remotePath)
	if err := client.Mkdir(ctx, remoteCollection); err != nil {
		return fmt.Errorf("imkdir %s: %w", remoteCollection, err)
	}
	if err := client.Upload(ctx, archiveLocal, remotePath, opts); err != nil {
		return err
	}

	archive := &sidecar.ArchiveInfo{
		Format:   "tar.gz",
		Filename: filepath.Base(archiveLocal),
		Size:     res.Size,
		Sha256:   res.Sha256,
	}
	if err := finalizeFolderSidecar(cmd, srcAbs, remotePath, env, prof, archive, client, ctx); err != nil {
		return err
	}

	// Cleanup or warn — the archive is local cruft after a successful upload.
	if cleanupAfter {
		if err := os.Remove(archiveLocal); err != nil {
			fmt.Fprintf(stderr, "  WARNING: could not remove %s: %v\n", archiveLocal, err)
		} else {
			fmt.Fprintf(stdout, "  removed local archive %s\n", archiveLocal)
		}
	} else {
		// Loud warning so users don't accidentally leak archives over time.
		fmt.Fprintf(stderr, "\n  \033[33m⚠  local archive kept: %s (%s)\033[0m\n",
			archiveLocal, folder.FormatHumanBytes(res.Size))
		fmt.Fprintf(stderr,
			"     delete it manually or re-run with --cleanup-archive next time.\n\n")
	}
	return nil
}

// finalizeFolderSidecar handles the post-upload bookkeeping for both the
// direct and archived paths: stat the remote, compute the folder PURL,
// build the recursive_sha256, and write the sidecar.
func finalizeFolderSidecar(
	cmd *cobra.Command,
	srcAbs, remotePath string,
	env *config.Env,
	prof *folder.Profile,
	archive *sidecar.ArchiveInfo,
	client *iron.Client,
	ctx context.Context,
) error {
	stdout := cmd.OutOrStdout()

	// Hash every file under srcAbs once (reusing hashcache); fold into Merkle.
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()
	fmt.Fprintf(stdout, "  hashing %d files for folder Merkle…\n", prof.FileCount)
	recursive, err := folder.RecursiveSHA256(srcAbs, cache.Sum)
	if err != nil {
		return fmt.Errorf("recursive sha256: %w", err)
	}

	// Build the sidecar.
	scPath := srcAbs + sidecar.Suffix
	doc, err := readMaybe(scPath)
	if err != nil {
		return err
	}
	if doc == nil {
		doc = &sidecar.Doc{}
	}
	doc.Kind = "folder"
	doc.Folder = &sidecar.FolderInfo{
		FileCount:       prof.FileCount,
		TotalBytes:      prof.TotalBytes,
		MedianBytes:     prof.MedianSize,
		RecursiveSha256: recursive,
	}
	if dp := env.String("data_project"); dp != "" {
		doc.DataProject = dp
	}
	if doc.IRODS == nil {
		doc.IRODS = &sidecar.IRODS{}
	}
	doc.IRODS.Path = remotePath
	doc.IRODS.Status = "uploaded"
	doc.IRODS.UploadedAt = time.Now().UTC().Truncate(time.Second)

	web := env.String("irods_web")
	if web == "" {
		web = defaults.IRODSWeb
	}
	if web != "" {
		doc.IRODS.URL = strings.TrimRight(web, "/") + remotePath
	}

	// Skip the iron stat → PURL roundtrip during dry-run. iron --dry-run
	// still creates catalog entries server-side (counter-intuitive, but
	// observed empirically), so calling stat afterwards returns an ID for
	// an object that may not have real contents. We'd be stamping a
	// misleading PURL into the sidecar.
	pidBase := env.String("irods_pid_base")
	if pidBase == "" {
		pidBase = defaults.IRODSPIDBase
	}
	if !skipStat(cmd) {
		if stat, statErr := client.Stat(ctx, remotePath); statErr == nil &&
			stat.ID != 0 && pidBase != "" {
			zone := firstPathSegment(remotePath)
			if zone != "" {
				doc.IRODS.PURL = fmt.Sprintf("%s/%s/%d/",
					strings.TrimRight(pidBase, "/"), zone, stat.ID)
			}
		}
	}

	if archive != nil {
		// Archive PURL is the same as IRODS.PURL since the iRODS object IS
		// the archive when packed.
		archive.PURL = doc.IRODS.PURL
		doc.Archive = archive
	}

	// Stamp ELN context if available
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

	if err := sidecar.Write(scPath, doc); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "  wrote %s\n", scPath)

	// Apply mus_* AVU metadata unless explicitly skipped or this was a
	// dry-run (where the catalog object may be a phantom).
	noMeta, _ := cmd.Flags().GetBool("no-metadata")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !noMeta && !dryRun {
		applyMetadataToIRODS(ctx, client, remotePath, doc, cmd.ErrOrStderr())
	}
	return nil
}

// --- small helpers ----------------------------------------------------------

// skipStat returns true when the upload was a dry-run; iron --dry-run still
// touches the catalog so post-upload stat would return misleading IDs.
func skipStat(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("dry-run")
	return v
}

func stdinIsTTY(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// readFloat returns a numeric setting from .env, or fallback if absent/invalid.
func readFloat(env *config.Env, key string, fallback float64) float64 {
	raw := env.String(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return v
}

// readInt64 returns a numeric setting from .env, or fallback if absent/invalid.
func readInt64(env *config.Env, key string, fallback int64) int64 {
	raw := env.String(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return v
}
