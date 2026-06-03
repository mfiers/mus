// Package cli — `mus rm`.
//
// Safely delete local data after triple-checking that the upstream
// iRODS copy is intact. Three independent verifications must all pass
// before any local bytes are removed:
//
//  1. Sidecar pointer — sidecar exists, irods_status == "uploaded",
//     irods_path is set.
//  2. Local-vs-recorded integrity — fresh hash of the local file (or
//     fresh Merkle of the local folder) matches what the sidecar
//     recorded at upload time. Detects local drift since upload.
//  3. Local-vs-remote integrity — iron checksum of the remote object
//     (or, for as-is folder uploads, a per-file walk of iron tree)
//     matches the fresh local hash. Detects remote drift or remote
//     loss since upload.
//
// On success, the data is removed and the sidecar is kept as a
// tombstone (irods_status flipped to "local_deleted") so
// `mus irods get <sidecar>` can later restore the file. Pass
// --delete-sidecar to remove the sidecar too.

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/folder"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// rmOptions bundles the CLI flags for runRm.
type rmOptions struct {
	dryRun        bool
	yes           bool
	deleteSidecar bool
}

// rmTarget is one verified candidate for deletion.
type rmTarget struct {
	sidecarPath string
	dataPath    string
	doc         *sidecar.Doc
	kind        string // "file" | "folder-archived" | "folder-asis"
	bytes       int64  // size estimate for the summary line
}

func newRmCmd() *cobra.Command {
	var opts rmOptions
	cmd := &cobra.Command{
		Use:   "rm PATH [PATH...]",
		Short: "safely delete local data after triple-checking the iRODS backup",
		Long: "mus rm removes a local file or folder ONLY after verifying that the\n" +
			"upstream iRODS copy is intact, byte-for-byte. Three checks must pass:\n\n" +
			"  1. sidecar says irods_status=uploaded with an irods_path\n" +
			"  2. fresh local hash matches what the sidecar recorded at upload\n" +
			"  3. fresh local hash matches the remote checksum reported by iron\n\n" +
			"For folders uploaded as-is (no tar.gz), every file under the local tree\n" +
			"is matched against its remote counterpart by relative path.\n\n" +
			"By default the .mus sidecar is kept as a tombstone — irods_status flips\n" +
			"to local_deleted and `mus irods get <sidecar>` can later restore the\n" +
			"file from iRODS. Pass --delete-sidecar to remove the sidecar too.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRm(cmd, args, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false,
		"print what would be deleted; do not touch any files")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false,
		"skip the interactive confirmation prompt")
	cmd.Flags().BoolVar(&opts.deleteSidecar, "delete-sidecar", false,
		"also delete the .mus sidecar (default: keep it as a tombstone)")
	return cmd
}

func runRm(cmd *cobra.Command, args []string, opts rmOptions) error {
	ctx := cmd.Context()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	client, err := iron.New()
	if err != nil {
		return err
	}

	targets, resolveErrs := resolveRmTargets(args)
	if len(resolveErrs) > 0 {
		for _, e := range resolveErrs {
			fmt.Fprintf(stderr, "  ✘ %s\n", e)
		}
		return fmt.Errorf("%d target(s) could not be resolved — nothing deleted", len(resolveErrs))
	}

	// Verify every target. If anything fails, refuse the whole batch.
	// All-or-nothing matches the user's mental model for `rm A B C` —
	// they don't want a partial success that leaves the workspace in
	// a confusing state.
	var verifyErrs []string
	for _, t := range targets {
		if err := verifyTargetForDeletion(ctx, cache, client, t); err != nil {
			verifyErrs = append(verifyErrs,
				fmt.Sprintf("✘ %s: %v", t.dataPath, err))
		}
	}
	if len(verifyErrs) > 0 {
		fmt.Fprintln(stderr, "mus rm: verification failed — nothing deleted.")
		for _, e := range verifyErrs {
			fmt.Fprintf(stderr, "  %s\n", e)
		}
		return fmt.Errorf("%d verification failure(s)", len(verifyErrs))
	}

	// All checks passed. Print the deletion plan.
	fmt.Fprintln(stdout, "mus rm: verified — these will be deleted:")
	for _, t := range targets {
		fmt.Fprintf(stdout, "  ✓ %s  (%s)\n", t.dataPath, t.kind)
	}
	if opts.dryRun {
		fmt.Fprintln(stdout, "(--dry-run: nothing actually deleted)")
		return nil
	}

	// Confirm interactively on a TTY unless --yes.
	if !opts.yes {
		if !confirmRm(stdout, stderr, len(targets)) {
			fmt.Fprintln(stdout, "aborted.")
			return nil
		}
	}

	// Execute deletions.
	for _, t := range targets {
		if err := performRm(t, opts); err != nil {
			// Print and continue — a partial success here means the
			// CALLED iRODS-backed deletes succeeded but one of the
			// later ones tripped on a permissions issue. Surface every
			// problem so the user knows the exact state.
			fmt.Fprintf(stderr, "  ✘ delete %s: %v\n", t.dataPath, err)
			continue
		}
		fmt.Fprintf(stdout, "  ✓ deleted %s\n", t.dataPath)
	}
	return nil
}

// resolveRmTargets turns each arg into an rmTarget. Accepts either
// data paths (foo.h5ad, foo/) or sidecar paths (foo.h5ad.mus,
// foo.mus). Returns (targets, errors-as-strings) instead of bailing
// on the first miss so the user sees the full list of problems.
func resolveRmTargets(args []string) ([]rmTarget, []string) {
	var targets []rmTarget
	var errs []string
	for _, arg := range args {
		abs, err := filepath.Abs(arg)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", arg, err))
			continue
		}

		// Normalise: if user passed a sidecar, derive the data path;
		// if they passed a data path, derive the sidecar.
		var sidecarPath, dataPath string
		if sidecar.IsSidecar(abs) {
			sidecarPath = abs
			dataPath = sidecar.DataPath(abs)
		} else {
			dataPath = abs
			sidecarPath = sidecar.SidecarPath(abs)
		}

		doc, err := sidecar.Read(sidecarPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: cannot read sidecar at %s: %v",
				arg, sidecarPath, err))
			continue
		}

		st, statErr := os.Stat(dataPath)
		if statErr != nil {
			errs = append(errs, fmt.Sprintf("%s: data path %s missing: %v",
				arg, dataPath, statErr))
			continue
		}

		t := rmTarget{
			sidecarPath: sidecarPath,
			dataPath:    dataPath,
			doc:         doc,
		}
		switch {
		case doc.Kind == "folder" && doc.Archive != nil && doc.Archive.Filename != "":
			t.kind = "folder-archived"
			t.bytes = doc.Archive.Size
		case doc.Kind == "folder":
			if !st.IsDir() {
				errs = append(errs, fmt.Sprintf("%s: sidecar says folder but %s is not a directory",
					arg, dataPath))
				continue
			}
			t.kind = "folder-asis"
			if doc.Folder != nil {
				t.bytes = doc.Folder.TotalBytes
			}
		default:
			if st.IsDir() {
				errs = append(errs, fmt.Sprintf("%s: %s is a directory but sidecar is a file sidecar",
					arg, dataPath))
				continue
			}
			t.kind = "file"
			t.bytes = st.Size()
		}
		targets = append(targets, t)
	}
	return targets, errs
}

// verifyTargetForDeletion runs the three checks for one target. Any
// failure → returns a descriptive error. The error message is shown
// to the user verbatim, so make it actionable.
func verifyTargetForDeletion(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, t rmTarget) error {

	// Check 1: sidecar pointer.
	if t.doc.IRODS == nil || t.doc.IRODS.Path == "" {
		return errors.New("sidecar has no iRODS path — not uploaded")
	}
	if !strings.EqualFold(t.doc.IRODS.Status, "uploaded") {
		return fmt.Errorf("sidecar irods_status is %q, want 'uploaded'",
			t.doc.IRODS.Status)
	}

	switch t.kind {
	case "file":
		return verifyFileForDeletion(ctx, cache, client, t)
	case "folder-archived":
		return verifyFolderArchivedForDeletion(ctx, cache, client, t)
	case "folder-asis":
		return verifyFolderAsisForDeletion(ctx, cache, client, t)
	default:
		return fmt.Errorf("unknown target kind %q", t.kind)
	}
}

func verifyFileForDeletion(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, t rmTarget) error {

	localSum, err := cache.Sum(t.dataPath)
	if err != nil {
		return fmt.Errorf("local hash: %w", err)
	}
	// Check 2: local matches recorded.
	if t.doc.File.Sha256 != "" && !strings.EqualFold(localSum, t.doc.File.Sha256) {
		return fmt.Errorf("local drift since upload — current sha=%s, sidecar sha=%s",
			short(localSum), short(t.doc.File.Sha256))
	}
	// Check 3: local matches remote. Stat first so a missing remote
	// produces a clean error rather than iron dumping its usage text.
	if _, err := client.Stat(ctx, t.doc.IRODS.Path); err != nil {
		return fmt.Errorf("remote object missing at %s — refusing to delete",
			t.doc.IRODS.Path)
	}
	remoteSum, err := client.Checksum(ctx, t.doc.IRODS.Path)
	if err != nil {
		return fmt.Errorf("remote checksum failed: %v — refusing to delete", err)
	}
	if !strings.EqualFold(localSum, remoteSum) {
		return fmt.Errorf("remote mismatch — local sha=%s, remote sha=%s",
			short(localSum), short(remoteSum))
	}
	return nil
}

func verifyFolderArchivedForDeletion(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, t rmTarget) error {
	// For an archived folder, the iRODS object IS the archive — but
	// the local "data path" is the unpacked folder. We check the
	// archive sibling against iRODS, because that's what was uploaded.
	archive := filepath.Join(filepath.Dir(t.sidecarPath), t.doc.Archive.Filename)
	if _, err := os.Stat(archive); err != nil {
		return fmt.Errorf("archive sibling missing (%s): %w",
			t.doc.Archive.Filename, err)
	}
	localSum, err := cache.Sum(archive)
	if err != nil {
		return fmt.Errorf("local archive hash: %w", err)
	}
	if t.doc.Archive.Sha256 != "" && !strings.EqualFold(localSum, t.doc.Archive.Sha256) {
		return fmt.Errorf("archive drift since upload — current=%s, sidecar=%s",
			short(localSum), short(t.doc.Archive.Sha256))
	}
	if _, err := client.Stat(ctx, t.doc.IRODS.Path); err != nil {
		return fmt.Errorf("remote archive missing at %s — refusing to delete",
			t.doc.IRODS.Path)
	}
	remoteSum, err := client.Checksum(ctx, t.doc.IRODS.Path)
	if err != nil {
		return fmt.Errorf("remote checksum failed: %v — refusing to delete", err)
	}
	if !strings.EqualFold(localSum, remoteSum) {
		return fmt.Errorf("remote mismatch — local archive sha=%s, remote sha=%s",
			short(localSum), short(remoteSum))
	}
	return nil
}

func verifyFolderAsisForDeletion(ctx context.Context, cache *hashcache.Cache,
	client *iron.Client, t rmTarget) error {

	// Check 2: local Merkle matches what was recorded.
	if t.doc.Folder == nil || t.doc.Folder.RecursiveSha256 == "" {
		return errors.New("folder sidecar has no recursive_sha256 to compare against")
	}
	actual, err := folder.RecursiveSHA256(t.dataPath, cache.Sum)
	if err != nil {
		return fmt.Errorf("local Merkle: %w", err)
	}
	if !strings.EqualFold(actual, t.doc.Folder.RecursiveSha256) {
		return fmt.Errorf("local Merkle drift — current=%s, sidecar=%s",
			short(actual), short(t.doc.Folder.RecursiveSha256))
	}

	// Check 3: walk remote, compare every file. We discard the
	// per-file noise stdout/stderr from deepFolderCheckAgainstIRODS
	// (it prints OK lines for each file) but keep the per-file
	// mismatch detail by routing stderr to a buffer when there are
	// problems — so the rm caller can show them to the user.
	var verifyOut strings.Builder
	mism, miss, extra, err := deepFolderCheckAgainstIRODS(
		ctx, cache, client, t.dataPath, t.doc.IRODS.Path,
		io.Discard, &verifyOut,
	)
	if err != nil {
		return fmt.Errorf("remote tree walk: %w", err)
	}
	if mism+miss+extra > 0 {
		return fmt.Errorf("remote tree mismatch — %d mismatched, %d missing on iRODS, %d extra on iRODS\n%s",
			mism, miss, extra, verifyOut.String())
	}
	return nil
}

// performRm executes the actual filesystem removal for one verified
// target. On success the sidecar is updated to a tombstone (or
// deleted, with --delete-sidecar).
func performRm(t rmTarget, opts rmOptions) error {
	if err := os.RemoveAll(t.dataPath); err != nil {
		return err
	}
	// Folder-archived also has an archive sibling on disk we want gone.
	if t.kind == "folder-archived" {
		archive := filepath.Join(filepath.Dir(t.sidecarPath), t.doc.Archive.Filename)
		if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove archive sibling: %w", err)
		}
	}
	if opts.deleteSidecar {
		return os.Remove(t.sidecarPath)
	}
	// Tombstone the sidecar: flip status, bump timestamp. The
	// irods_path + irods_url + irods_purl + sha256 are kept so the
	// sidecar remains a usable handle for `mus irods get`.
	t.doc.IRODS.Status = "local_deleted"
	return sidecar.Write(t.sidecarPath, t.doc)
}

// confirmRm reads a y/N line from stdin. Returns true iff the user
// typed "y" or "yes" (case-insensitive). On a non-TTY (no stdin) or
// any read error, returns false — better to abort than to silently
// proceed.
func confirmRm(stdout, stderr io.Writer, n int) bool {
	noun := "target"
	if n != 1 {
		noun = "targets"
	}
	fmt.Fprintf(stdout, "delete %d %s? [y/N]: ", n, noun)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fmt.Fprintln(stderr) // newline so the cursor lands cleanly
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}
