package cli

// `mus eln register-irods <sidecar.mus>` — backfill the ELN SAMPLESOUT
// record for an iRODS upload from its *.mus sidecar.
//
// The post-upload ELN sample registration in `mus irods upload` is
// best-effort: a transient ELN error (e.g. a one-off HTTP 500 on the
// section create) leaves the data on iRODS but no ELN sample. This
// command recreates that record from the sidecar alone — it needs no
// local data (works on a tombstone, irods_status=local_deleted), reading
// the iRODS path, checksum, size, PURL and eln_experiment_id the sidecar
// already carries, and then runs the SAME registration code path as the
// live upload (so section + sample + meta autofill are identical).
//
// Idempotent in the same way the live path is: a file-kind sidecar that
// already records eln_sample_id is recognised and not duplicated. Folder
// and archive uploads (no per-file sidecar link) are registered fresh.

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

func newELNRegisterIRODSCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "register-irods SIDECAR.mus",
		Short: "backfill the ELN sample record for an iRODS upload from its sidecar",
		Long: "Recreate the ELN SAMPLESOUT sample record for a file/folder that\n" +
			"was uploaded to iRODS but whose ELN registration failed (e.g. a\n" +
			"transient server error during `mus irods upload` / `mus qdb`).\n\n" +
			"Reads everything it needs from the *.mus sidecar — iRODS path,\n" +
			"checksum, size, PURL, eln_experiment_id — so it works even on a\n" +
			"tombstone whose local data is gone. Runs the same registration as\n" +
			"the live upload (section + sample + meta autofill), and is\n" +
			"idempotent for file-kind sidecars that already carry eln_sample_id.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNRegisterIRODS(cmd, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"register even if the sidecar already records an eln_sample_id "+
			"(WILL create a duplicate — only use if the prior sample was deleted)")
	return cmd
}

func runELNRegisterIRODS(cmd *cobra.Command, scArg string, force bool) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	// Accept either the sidecar path or the data path; normalise to the
	// sidecar.
	scPath := scArg
	if !sidecar.IsSidecar(scPath) {
		scPath = sidecar.SidecarPath(scArg)
	}
	doc, err := sidecar.Read(scPath)
	if err != nil {
		return fmt.Errorf("read sidecar %s: %w", scPath, err)
	}
	if doc == nil {
		return fmt.Errorf("no sidecar at %s", scPath)
	}
	if doc.IRODS == nil || doc.IRODS.Path == "" {
		return fmt.Errorf("sidecar %s has no irods_path — was this uploaded to iRODS?", scPath)
	}
	if doc.ELN == nil || doc.ELN.ExperimentID == "" {
		return fmt.Errorf("sidecar %s has no eln_experiment_id — link an experiment before registering", scPath)
	}
	// Idempotency guard. The live registration only dedups file-kind
	// sidecars (folder/archive items register fresh every time). This
	// command is a manual backfill, so a second run would silently create
	// a DUPLICATE sample for a folder/archive. Refuse when a sample is
	// already recorded, unless --force (the prior sample was deleted).
	if doc.ELN.SampleID != "" && !force {
		return fmt.Errorf("sidecar %s already records eln_sample_id=%s — "+
			"already registered. Pass --force only if that sample was deleted "+
			"in the ELN and you want a new one (creates a duplicate otherwise)",
			scPath, doc.ELN.SampleID)
	}

	// Rebuild the uploadedItem the live path would have produced. Kind,
	// checksum and size come from the right sub-block: a plain file uses
	// File.*, an archive uses Archive.*, a non-archived folder uses
	// Folder.RecursiveSha256 + total size.
	// LocalPath is the data path the sidecar describes. It may no longer
	// exist (tombstone) — that's fine: for folder/archive kinds the
	// registration doesn't read it, and os.Stat failures there are
	// tolerated (mtime autofill is simply skipped).
	item := uploadedItem{
		Kind:       doc.Kind,
		LocalPath:  sidecar.DataPath(scPath),
		RemotePath: doc.IRODS.Path,
		URL:        doc.IRODS.URL,
		PURL:       doc.IRODS.PURL,
	}
	switch {
	case doc.Archive != nil && doc.Archive.Sha256 != "":
		item.Sha256 = doc.Archive.Sha256
		item.Size = doc.Archive.Size
		if item.PURL == "" {
			item.PURL = doc.Archive.PURL
		}
	case doc.Folder != nil && doc.Folder.RecursiveSha256 != "":
		item.Sha256 = doc.Folder.RecursiveSha256
		item.Size = doc.Folder.TotalBytes
		item.FileCount = doc.Folder.FileCount
		item.DirCount = doc.Folder.DirCount
	default:
		item.Sha256 = doc.File.Sha256
		item.Size = doc.File.Size
	}
	if item.Kind == "" {
		item.Kind = "file"
	}

	// Build an env carrying just the ELN context the registration reads
	// (experiment id + sample-type/location keys), sourced from the
	// sidecar and the folder's cascade. The cascade (if a mus.cfg still
	// exists beside the sidecar) supplies eln_sample_type /
	// eln_irods_sample_location; the sidecar's experiment id wins.
	env, _ := config.Load(filepath.Dir(sidecar.DataPath(scPath)))
	if env == nil {
		env = &config.Env{}
	}
	env.Set("eln_experiment_id", doc.ELN.ExperimentID)

	msg := doc.IRODS.Message
	if msg == "" {
		msg = "mus eln register-irods backfill"
	}

	fmt.Fprintf(stdout, "registering ELN sample from %s\n", scPath)
	fmt.Fprintf(stdout, "  experiment %s, iRODS %s\n", doc.ELN.ExperimentID, doc.IRODS.Path)

	// Reuse the exact live registration path. enabled=true, dryRun=false.
	postUploadRegisterSamplesInELN(env, []uploadedItem{item}, false, true, msg, stderr)
	return nil
}
