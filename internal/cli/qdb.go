package cli

// `mus qdb` — Quick-and-Dirty Backup. UNDOCUMENTED on purpose (hidden
// from --help, absent from the website + skill). One-shot archival of an
// old, unmanaged folder:
//
//   1. sanity checks (auth, not-already-managed, size cap, non-empty)
//   2. create an ELN experiment under a backup study ("Very Old Projects")
//   3. tag this folder with the new experiment (writes eln_* + data_project)
//   4. pack (unless a few large files) + mus irods upload + verify
//   5. auto ELN sample registration (normal upload side effect)
//   6. once verified, ASK permission, then delete source + archive,
//      leaving only the tombstone sidecar.
//
// Steps 4–6 reuse `mus irods upload --verify-and-clean` verbatim, so the
// verify-before-delete safety gate is the same tested code path.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/folder"
)

const (
	// Default backup study: "Very Old Projects" (REST studyID 411192;
	// the ELN UI shows it as the long id 012000000411192).
	qdbDefaultStudyID int64 = 411192

	// Size guard: refuse folders larger than this unless --max-size-gb
	// raises it. Stops an accidental terabyte backup.
	qdbDefaultMaxSizeGB float64 = 25.0

	// Pack decision: upload AS-IS (skip tar.gz) only when the folder is a
	// few genuinely large files — packing those (usually already-
	// compressed bam/cram/fastq.gz) wastes time + peak disk for ~no gain.
	// Everything else packs, so verify is a single checksum and iRODS
	// isn't hit with thousands of small objects.
	qdbMaxUnpackedFiles  int64 = 5
	qdbMinLargeFileBytes int64 = 1 << 30 // 1 GiB median
)

func newQDBCmd() *cobra.Command {
	var studyID int64
	var maxSizeGB float64
	var dataProject string
	cmd := &cobra.Command{
		Use:    "qdb [flags] EXPERIMENT NAME...",
		Short:  "quick-and-dirty backup of the current folder",
		Hidden: true, // intentionally undocumented
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expName := strings.TrimSpace(strings.Join(args, " "))
			return runQDB(cmd, expName, studyID, maxSizeGB, dataProject)
		},
	}
	cmd.Flags().Int64VarP(&studyID, "study", "s", qdbDefaultStudyID,
		"ELN study id to create the backup experiment under (long UI ids accepted)")
	cmd.Flags().Float64Var(&maxSizeGB, "max-size-gb", qdbDefaultMaxSizeGB,
		"refuse to back up a folder larger than this many GB")
	cmd.Flags().StringVarP(&dataProject, "data-project", "p", "",
		"data_project for the backup (NameYear, e.g. OldPilot2026) — REQUIRED")
	return cmd
}

func runQDB(cmd *cobra.Command, expName string, studyIDRaw int64, maxSizeGB float64, dataProject string) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	// --- 1. sanity checks ------------------------------------------------
	if expName == "" {
		return fmt.Errorf("qdb: an experiment name is required (e.g. `mus qdb old pilot data`)")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	srcAbs, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}

	// All cheap LOCAL checks run first, before any network call, so the
	// user gets fast feedback and the gates are testable without creds.

	// Refuse if this folder is already ELN-linked or has a data_project:
	// qdb is for fresh, unmanaged folders. Use the normal flow otherwise.
	env, _ := config.Load(srcAbs)
	if env != nil {
		if v := env.String("eln_experiment_id"); v != "" {
			return fmt.Errorf("qdb: this folder is already linked to ELN experiment %s — "+
				"use the normal `mus irods upload` flow instead", v)
		}
		if v := env.String("data_project"); v != "" {
			return fmt.Errorf("qdb: this folder already has data_project=%s — "+
				"use the normal `mus irods upload` flow instead", v)
		}
	}

	// data_project is required (qdb targets unmanaged folders). qdb writes
	// it to mus.cfg below; the reused upload then reads it from the cascade.
	dataProject = strings.TrimSpace(dataProject)
	if dataProject == "" {
		return fmt.Errorf("qdb: --data-project (-p) is required (NameYear, e.g. OldPilot2026)")
	}
	if err := dataproject.ValidateName(dataProject); err != nil {
		return fmt.Errorf("qdb: data_project %q invalid: %w", dataProject, err)
	}

	// Profile the folder: non-empty check + size cap + pack decision.
	prof, err := folder.ProfileFolder(srcAbs)
	if err != nil {
		return fmt.Errorf("qdb: profiling %s: %w", srcAbs, err)
	}
	if prof.FileCount == 0 {
		return fmt.Errorf("qdb: %s has no files to back up", srcAbs)
	}
	maxBytes := int64(maxSizeGB * float64(1<<30))
	if prof.TotalBytes > maxBytes {
		return fmt.Errorf("qdb: folder is %s, over the %.0f GB safety cap — "+
			"raise it with --max-size-gb if you really mean to back up this much",
			folder.FormatHumanBytes(prof.TotalBytes), maxSizeGB)
	}

	studyID := eln.FixStudyID(studyIDRaw)

	// ELN must be authenticated — qdb creates an experiment. (Last of the
	// preconditions: everything cheaper/local has already passed.)
	elnClient, err := openELN()
	if err != nil {
		return fmt.Errorf("qdb: ELN is required (run `mus eln login`): %w", err)
	}

	fmt.Fprintf(stdout, "qdb: backing up %s\n", srcAbs)
	fmt.Fprintf(stdout, "  %d files, %s\n", prof.FileCount, folder.FormatHumanBytes(prof.TotalBytes))
	fmt.Fprintf(stdout, "  experiment:   %q under study %d\n", expName, studyID)
	fmt.Fprintf(stdout, "  data_project: %s\n", dataProject)

	// --- 2. create the ELN experiment -----------------------------------
	expID, err := elnClient.CreateExperiment(studyID, expName)
	if err != nil {
		return fmt.Errorf("qdb: creating ELN experiment: %w", err)
	}
	fmt.Fprintf(stdout, "  ✓ created ELN experiment %d\n", expID)

	// --- 3. tag the folder (write eln_* + data_project to mus.cfg) -------
	info, err := elnClient.ExpInfo(expID)
	if err != nil {
		return fmt.Errorf("qdb: created experiment %d but could not read it back: %w", expID, err)
	}
	kv := experimentToKV(info)
	kv["data_project"] = dataProject
	if err := config.Save(srcAbs, kv); err != nil {
		return fmt.Errorf("qdb: writing mus.cfg: %w", err)
	}
	fmt.Fprintf(stdout, "  ✓ tagged folder (eln_experiment_id=%d, data_project=%s)\n", expID, dataProject)

	// --- 4/5/6. pack decision, then reuse `irods upload --verify-and-clean`
	packMode := "tar.gz"
	if prof.FileCount <= qdbMaxUnpackedFiles && prof.MedianSize >= qdbMinLargeFileBytes {
		packMode = "none"
		fmt.Fprintf(stdout, "  pack: none (%d large files — median %s; packing would not help)\n",
			prof.FileCount, folder.FormatHumanBytes(prof.MedianSize))
	} else {
		fmt.Fprintf(stdout, "  pack: tar.gz (bundling before upload)\n")
	}

	msg := fmt.Sprintf("qdb backup of %s", expName)

	up := newIRODSUploadCmd()
	up.SetContext(cmd.Context())
	up.SetIn(cmd.InOrStdin())
	up.SetOut(stdout)
	up.SetErr(stderr)
	up.SetArgs([]string{
		srcAbs,
		"-m", msg,
		"--pack", packMode,
		"--verify-and-clean",
	})
	if err := up.Execute(); err != nil {
		return fmt.Errorf("qdb: upload/verify/clean step failed (local data NOT deleted on a verify failure): %w", err)
	}

	fmt.Fprintf(stdout, "\nqdb: done. %s is backed up to iRODS + registered in ELN experiment %d.\n", expName, expID)
	return nil
}
