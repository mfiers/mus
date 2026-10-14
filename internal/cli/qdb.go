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
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/folder"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// qdbCruftDirs are directory names purged before a qdb backup — caches,
// VCS metadata, tmp, editor/runtime scratch. Matched by exact base name
// at any depth. Never worth archiving.
var qdbCruftDirs = map[string]bool{
	// VCS + Python/Jupyter
	".git": true, ".svn": true, "__pycache__": true,
	".ipynb_checkpoints": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true,
	// Generic cache / tmp / language runtimes
	".cache": true, "tmp": true, "temp": true,
	"node_modules": true, ".venv": true, "venv": true,
	".Rproj.user": true,
}

// qdbCruftFileSuffixes are file suffixes purged alongside the dirs.
var qdbCruftFileSuffixes = []string{".pyc"}

// qdbCruftFileNames are exact file base names purged.
var qdbCruftFileNames = map[string]bool{
	".DS_Store": true, "Thumbs.db": true, ".Rhistory": true,
}

// isQDBCruft reports whether a path (dir or file) matches the cruft list.
// *.mus sidecars and mus.cfg are never cruft (they are the deposit's
// self-describing layer).
func isQDBCruft(base string, isDir bool) bool {
	if isDir {
		return qdbCruftDirs[base]
	}
	if qdbCruftFileNames[base] {
		return true
	}
	for _, suf := range qdbCruftFileSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// qdbFindCruft walks srcAbs and returns the top-most paths to remove. A
// matching directory is returned without descending into it (so nested
// __pycache__ inside node_modules isn't listed separately).
func qdbFindCruft(srcAbs string) ([]string, error) {
	var hits []string
	err := filepath.WalkDir(srcAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcAbs {
			return nil
		}
		base := filepath.Base(path)
		if d.IsDir() {
			if isQDBCruft(base, true) {
				hits = append(hits, path)
				return filepath.SkipDir // don't descend / double-list
			}
			return nil
		}
		if isQDBCruft(base, false) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(hits)
	return hits, nil
}

// qdbPurgeCruft finds cruft, lists it, asks for explicit confirmation,
// and deletes it from disk. Does nothing (no prompt) when there's no
// cruft. On a non-TTY with cruft present it refuses rather than guessing.
func qdbPurgeCruft(cmd *cobra.Command, srcAbs string) error {
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()

	hits, err := qdbFindCruft(srcAbs)
	if err != nil {
		return fmt.Errorf("qdb: scanning for cruft: %w", err)
	}
	if len(hits) == 0 {
		return nil
	}

	fmt.Fprintf(stdout, "\nqdb: the following will be PERMANENTLY DELETED (not backed up):\n")
	for _, h := range hits {
		rel, relErr := filepath.Rel(srcAbs, h)
		if relErr != nil {
			rel = h
		}
		fmt.Fprintf(stdout, "    %s\n", rel)
	}
	fmt.Fprintf(stdout, "  (%d item(s); caches / VCS / tmp — pass --no-exclude to keep them)\n", len(hits))

	if !stdinIsTTY(stdin) {
		return fmt.Errorf("qdb: cruft present but stdin is not a TTY to confirm deletion — "+
			"re-run interactively, or pass --no-exclude to back up everything (%d item(s))", len(hits))
	}
	fmt.Fprintf(stdout, "  delete these now? [y/N] ")
	reader := bufio.NewReader(stdin)
	answer, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		// proceed
	default:
		return fmt.Errorf("qdb: aborted — cruft not deleted (pass --no-exclude to back up everything)")
	}

	for _, h := range hits {
		if err := os.RemoveAll(h); err != nil {
			return fmt.Errorf("qdb: removing %s: %w", h, err)
		}
	}
	fmt.Fprintf(stdout, "  ✓ removed %d cruft item(s)\n", len(hits))
	return nil
}

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

// qdbOpts bundles the qdb command's flags. studyID / experimentID are
// STRINGS, not ints: the eLabJournal UI shows ids zero-padded (e.g.
// 001000000002004560), and a numeric flag parses a leading zero as OCTAL
// (strconv base-0), silently mangling the value. We parse them ourselves
// as base-10 and then strip the long-form prefix.
type qdbOpts struct {
	studyIDRaw   string
	experimentID string // -x: use an EXISTING experiment instead of creating one
	maxSizeGB    float64
	dataProject  string
	noExclude    bool // --no-exclude: back up everything, skip the cruft purge
}

func newQDBCmd() *cobra.Command {
	var o qdbOpts
	cmd := &cobra.Command{
		Use:    "qdb [flags] EXPERIMENT NAME...",
		Short:  "quick-and-dirty backup of the current folder",
		Hidden: true, // intentionally undocumented
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expName := strings.TrimSpace(strings.Join(args, " "))
			// -s and -x are mutually exclusive: either create under a study
			// or reuse an existing experiment, never both.
			if strings.TrimSpace(o.experimentID) != "" && cmd.Flags().Changed("study") {
				return fmt.Errorf("qdb: pass either -s (create under a study) OR -x (use an existing experiment), not both")
			}
			return runQDB(cmd, expName, o)
		},
	}
	cmd.Flags().StringVarP(&o.studyIDRaw, "study", "s", strconv.FormatInt(qdbDefaultStudyID, 10),
		"ELN study id to CREATE the backup experiment under (long UI ids accepted)")
	cmd.Flags().StringVarP(&o.experimentID, "experiment", "x", "",
		"use an EXISTING ELN experiment id instead of creating one (mutually exclusive with -s)")
	cmd.Flags().Float64Var(&o.maxSizeGB, "max-size-gb", qdbDefaultMaxSizeGB,
		"refuse to back up a folder larger than this many GB")
	cmd.Flags().StringVarP(&o.dataProject, "data-project", "p", "",
		"data_project for the backup (NameYear, e.g. OldPilot2026) — REQUIRED")
	cmd.Flags().BoolVar(&o.noExclude, "no-exclude", false,
		"back up everything; skip the default cruft purge (.git, __pycache__, caches, …)")
	return cmd
}

// parseELNID parses an eLabJournal id from the command line as base-10,
// tolerating the UI's leading-zero padding (which a base-0 numeric parse
// would misread as octal). Returns the raw int64; the caller applies
// FixStudyID / FixExperimentID to strip the long-form prefix.
func parseELNID(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty id")
	}
	// Explicit base 10, and trim leading zeros so "001000..." is decimal,
	// never octal.
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" {
		trimmed = "0"
	}
	v, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not a numeric id: %q", s)
	}
	return v, nil
}

func runQDB(cmd *cobra.Command, expName string, o qdbOpts) error {
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
	dataProject := strings.TrimSpace(o.dataProject)
	if dataProject == "" {
		return fmt.Errorf("qdb: --data-project (-p) is required (NameYear, e.g. OldPilot2026)")
	}
	if err := dataproject.ValidateName(dataProject); err != nil {
		return fmt.Errorf("qdb: data_project %q invalid: %w", dataProject, err)
	}

	// --- 1b. purge cruft (confirmed) ------------------------------------
	// Remove well-known cache / VCS / tmp folders BEFORE profiling, so the
	// size cap + pack decision reflect only what will actually be backed
	// up. These are deleted from disk, not just excluded — qdb deletes the
	// whole source at the end anyway, and this stuff is never worth
	// archiving. Always confirmed interactively; --no-exclude skips it.
	if !o.noExclude {
		if err := qdbPurgeCruft(cmd, srcAbs); err != nil {
			return err
		}
	}

	// Profile the folder: non-empty check + size cap + pack decision.
	prof, err := folder.ProfileFolder(srcAbs)
	if err != nil {
		return fmt.Errorf("qdb: profiling %s: %w", srcAbs, err)
	}
	if prof.FileCount == 0 {
		return fmt.Errorf("qdb: %s has no files to back up", srcAbs)
	}
	maxBytes := int64(o.maxSizeGB * float64(1<<30))
	if prof.TotalBytes > maxBytes {
		return fmt.Errorf("qdb: folder is %s, over the %.0f GB safety cap — "+
			"raise it with --max-size-gb if you really mean to back up this much",
			folder.FormatHumanBytes(prof.TotalBytes), o.maxSizeGB)
	}

	// ELN must be authenticated — qdb creates/links an experiment. (Last of
	// the preconditions: everything cheaper/local has already passed.)
	elnClient, err := openELN()
	if err != nil {
		return fmt.Errorf("qdb: ELN is required (run `mus eln login`): %w", err)
	}

	fmt.Fprintf(stdout, "qdb: backing up %s\n", srcAbs)
	fmt.Fprintf(stdout, "  %d files, %s\n", prof.FileCount, folder.FormatHumanBytes(prof.TotalBytes))
	fmt.Fprintf(stdout, "  data_project: %s\n", dataProject)

	// --- 2. resolve the ELN experiment (create new, or use existing -x) --
	// Ids are parsed as base-10 (tolerating the UI's leading-zero padding)
	// then long-form-stripped. See parseELNID / FixStudyID / FixExperimentID.
	var expID int64
	if strings.TrimSpace(o.experimentID) != "" {
		raw, perr := parseELNID(o.experimentID)
		if perr != nil {
			return fmt.Errorf("qdb: --experiment %q: %w", o.experimentID, perr)
		}
		expID = eln.FixExperimentID(raw)
		fmt.Fprintf(stdout, "  using existing ELN experiment %d\n", expID)
	} else {
		raw, perr := parseELNID(o.studyIDRaw)
		if perr != nil {
			return fmt.Errorf("qdb: --study %q: %w", o.studyIDRaw, perr)
		}
		studyID := eln.FixStudyID(raw)
		fmt.Fprintf(stdout, "  creating experiment %q under study %d\n", expName, studyID)
		expID, err = elnClient.CreateExperiment(studyID, expName)
		if err != nil {
			return fmt.Errorf("qdb: creating ELN experiment: %w", err)
		}
		fmt.Fprintf(stdout, "  ✓ created ELN experiment %d\n", expID)
	}

	// --- 3. tag the folder (write eln_* + data_project to mus.cfg) -------
	info, err := elnClient.ExpInfo(expID)
	if err != nil {
		return fmt.Errorf("qdb: could not read ELN experiment %d: %w", expID, err)
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

	// The sibling sidecar the upload will write (and tombstone). It
	// SURVIVES --verify-and-clean's source deletion, so we can register
	// the ELN sample from it afterwards.
	sidecarPath := srcAbs + sidecar.Suffix

	// Run the upload with --no-eln: qdb does the ELN registration itself,
	// AFTER the upload, so it can see success/failure (the upload's own
	// registration is best-effort and swallows errors to stderr — exactly
	// how the taxem sample was silently lost). qdb is supposed to be
	// supersafe, so it must not claim success unless the ELN record exists.
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
		"--no-eln",
	})
	if err := up.Execute(); err != nil {
		return fmt.Errorf("qdb: upload/verify/clean step failed (local data NOT deleted on a verify failure): %w", err)
	}

	// Data is on iRODS and verified. Now register the ELN sample from the
	// surviving tombstone sidecar, capturing any failure. With the ELN
	// client's 5xx retry this rarely fails, but if it does we must report
	// it loudly rather than falsely claim "registered" — the data is safe
	// on iRODS, only the ELN record is missing, and it can be backfilled.
	fmt.Fprintf(stdout, "  registering ELN sample…\n")
	if err := runELNRegisterIRODS(cmd, sidecarPath, false); err != nil {
		fmt.Fprintf(stderr, "\n  \033[33m⚠ qdb: data IS safely on iRODS, but the ELN sample record was NOT created:\033[0m\n    %v\n", err)
		fmt.Fprintf(stderr, "    Recover with:  mus eln register-irods %s\n\n", sidecarPath)
		return fmt.Errorf("qdb: backup uploaded + verified, but ELN registration failed — see recovery command above")
	}

	fmt.Fprintf(stdout, "\nqdb: done. %s is backed up to iRODS AND registered in ELN experiment %d.\n", expName, expID)
	return nil
}
