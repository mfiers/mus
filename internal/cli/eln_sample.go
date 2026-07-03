package cli

// `mus eln sample` and `mus eln attach` — two file-oriented
// eLabNext workflows that share the same file-walk + hash + sidecar
// plumbing.
//
// `mus eln sample -m MSG [--type Data] [--section samplesout] files...`
//     Register each file as a Sample record in ELN. The file's LOCATION
//     (absolute path + sha256 + size) goes into the sample's note; the
//     -m message becomes the sample's description. Data stays on disk —
//     mus never copies the bytes to the ELN. All samples in the batch
//     get linked to the current experiment via a new SAMPLESOUT (or
//     SAMPLESIN) section.
//
// `mus eln attach -m MSG files...`
//     Upload each file's BYTES as an attachment to a FILE section on
//     the current experiment. Use for actual documents (protocols,
//     figures, small data blobs) that belong INSIDE the notebook page.
//     One section per invocation, with all files in the batch attached
//     to that section.
//
// Both commands read eln_experiment_id from the mus.cfg cascade and
// refuse if unset. Both stamp their action on the file's sidecar so
// re-runs are idempotent — mus skips files that already carry the
// relevant eln_* marker.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// --- mus eln sample ---------------------------------------------------------

func newELNSampleCmd() *cobra.Command {
	var message, sampleType, sectionKind string
	cmd := &cobra.Command{
		Use:   "sample -m MESSAGE [--type Data] [--section samplesout] FILE [FILE...]",
		Short: "register file locations as ELN samples (no data upload)",
		Long: "Creates one eLabNext Sample record PER FILE, then links every\n" +
			"created sample to the current experiment via a new SAMPLESOUT (or\n" +
			"SAMPLESIN) section.\n\n" +
			"Each sample carries:\n" +
			"  name        = file basename\n" +
			"  description = the -m / --message value\n" +
			"  note        = auto-generated 'path=<abs>\\nsha256=<hex>\\nsize=<n>\\nmtime=<iso>'\n\n" +
			"Data is NOT uploaded to the ELN. The sample record only points at\n" +
			"where the file lives on the NAS / local disk. Use `mus eln attach`\n" +
			"if you want the file's bytes inside the notebook page instead.\n\n" +
			"Idempotent: mus stamps eln_sample_id on the file's sidecar. Re-runs\n" +
			"skip any file that already has one — safe to run twice.\n\n" +
			"Requires eln_experiment_id in the mus.cfg cascade (usually via\n" +
			"`mus eln link`).\n\n" +
			"--type NAME picks the sample type by name; the default is 'Data'.\n" +
			"Fails loudly if that name isn't configured on the tenant, with the\n" +
			"full list of available types.\n\n" +
			"--section samplesout | samplesin decides which section type gets\n" +
			"created on the experiment. samplesout is right when the files are\n" +
			"the OUTPUT of the experiment (results, analysis files); samplesin\n" +
			"when they're INPUTS the experiment consumed.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(message) == "" {
				return fmt.Errorf("-m / --message is required — describes what these files ARE")
			}
			sk := strings.ToUpper(strings.TrimSpace(sectionKind))
			switch sk {
			case "SAMPLESOUT", "SAMPLESIN":
			default:
				return fmt.Errorf("--section must be samplesout or samplesin (got %q)", sectionKind)
			}
			return runELNSample(cmd, args, message, sampleType, sk)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"description of what these files are (REQUIRED); stamped as the sample description")
	_ = cmd.MarkFlagRequired("message")
	cmd.Flags().StringVar(&sampleType, "type", "Data",
		"sample type name on the ELN tenant (must exist; default 'Data')")
	cmd.Flags().StringVar(&sectionKind, "section", "samplesout",
		"experiment section type: samplesout (default) | samplesin")
	return cmd
}

func runELNSample(cmd *cobra.Command, args []string, message, sampleTypeName, sectionKind string) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	expIDRaw := env.String("eln_experiment_id")
	if expIDRaw == "" {
		return fmt.Errorf("no eln_experiment_id in cascade — run `mus eln link <ID>` in this folder first")
	}
	expID, err := parseELNExperimentID(expIDRaw)
	if err != nil {
		return fmt.Errorf("invalid eln_experiment_id %q: %w", expIDRaw, err)
	}

	client, err := openELN()
	if err != nil {
		return err
	}

	// Resolve the sampleType once for the batch.
	st, err := client.FindSampleTypeByName(sampleTypeName)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sample type: %s (id=%d)\n", st.Name, st.ID)

	// Collect the input files (absolute paths, skipping *.mus sidecars).
	files, err := collectSampleInputs(args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no eligible files (all args were sidecars or missing)")
	}

	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	// Create the section FIRST so we have a place to attach samples
	// as they're created. Section header is derived from the message
	// so multiple invocations create audibly-distinct sections.
	sectionHeader := "mus eln sample: " + firstLine(message)
	journalID, err := client.CreateExperimentSection(expID, sectionKind, sectionHeader)
	if err != nil {
		return fmt.Errorf("create %s section: %w", sectionKind, err)
	}
	fmt.Fprintf(stdout, "created section (expJournalID=%d) on experiment %d\n",
		journalID, expID)

	// One sample per file. Collect IDs so we can batch-link at the end.
	var createdIDs []int64
	var skipped, failed int
	now := time.Now().UTC().Truncate(time.Second)
	journalStr := strconv.FormatInt(journalID, 10)

	for _, path := range files {
		abs, _ := filepath.Abs(path)

		// Idempotency: skip if the sidecar already records a
		// SampleID. Re-running is safe.
		scPath := sidecar.SidecarPath(abs)
		doc, _ := readMaybe(scPath)
		if doc != nil && doc.ELN != nil && doc.ELN.SampleID != "" {
			fmt.Fprintf(stdout, "  ↷ %s already registered as sample %s — skipping\n",
				filepath.Base(abs), doc.ELN.SampleID)
			skipped++
			continue
		}

		fi, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ stat %s: %v\n", abs, err)
			failed++
			continue
		}
		sha, err := cache.Sum(abs)
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ hash %s: %v\n", abs, err)
			failed++
			continue
		}

		note := "path=" + abs + "\n" +
			"sha256=" + sha + "\n" +
			"size=" + strconv.FormatInt(fi.Size(), 10) + "\n" +
			"mtime=" + fi.ModTime().UTC().Format(time.RFC3339)

		sample, err := client.CreateSample(eln.CreateSampleReq{
			SampleTypeID: st.ID,
			Name:         filepath.Base(abs),
			Description:  message,
			Note:         note,
		})
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ create sample %s: %v\n", filepath.Base(abs), err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "  ✓ %s → sample %d\n", filepath.Base(abs), sample.ID)
		createdIDs = append(createdIDs, sample.ID)

		// Stamp the sidecar so re-runs skip this file.
		if doc == nil {
			doc = &sidecar.Doc{}
		}
		if doc.ELN == nil {
			doc.ELN = &sidecar.ELN{}
		}
		doc.ELN.SampleID = strconv.FormatInt(sample.ID, 10)
		doc.ELN.SampleType = st.Name
		doc.ELN.SampleCreatedAt = now
		doc.ELN.SampleSectionID = journalStr
		if err := sidecar.Write(scPath, doc); err != nil {
			fmt.Fprintf(stderr, "  ⚠ sidecar write %s: %v (sample already created)\n",
				scPath, err)
		}
	}

	// Batch-link every newly-created sample to the section.
	if len(createdIDs) > 0 {
		if err := client.LinkSamplesToSection(journalID, createdIDs); err != nil {
			// Samples exist but aren't linked to the experiment.
			// Surface the error clearly so the depositor can retry
			// or link manually.
			fmt.Fprintf(stderr,
				"⚠ %d sample(s) created but the section-link call failed: %v\n"+
					"   sample IDs: %v\n"+
					"   experiment: %d, expJournalID: %d\n"+
					"   Re-run to retry (mus is idempotent — will skip existing).\n",
				len(createdIDs), err, createdIDs, expID, journalID)
			return err
		}
		fmt.Fprintf(stdout, "linked %d sample(s) to section %d on experiment %d\n",
			len(createdIDs), journalID, expID)
	}

	fmt.Fprintf(stdout, "\nregistered %d, skipped %d, failed %d\n",
		len(createdIDs), skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d file(s) failed to register", failed)
	}
	return nil
}

// firstLine returns the first line of s (used for section headers).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// collectSampleInputs walks each arg. Directories recurse. Sidecars
// (*.mus) and dotfiles are skipped. Returns absolute paths.
func collectSampleInputs(args []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a, err)
		}
		if !st.IsDir() {
			if sidecar.IsSidecar(a) {
				continue // silent; sidecars ride with their file
			}
			add(a)
			continue
		}
		if err := filepath.Walk(a, func(p string, info os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") && p != a {
					return filepath.SkipDir
				}
				return nil
			}
			if sidecar.IsSidecar(p) {
				return nil
			}
			if strings.HasPrefix(info.Name(), ".") {
				return nil
			}
			add(p)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- mus eln attach ---------------------------------------------------------

func newELNAttachCmd() *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "attach -m MESSAGE FILE [FILE...]",
		Short: "upload file(s) as attachments to the current ELN experiment",
		Long: "Copies each file's bytes into a FILE section on the current\n" +
			"experiment. Use this for documents that belong INSIDE the notebook\n" +
			"page (protocols, figures, small data blobs, PDFs).\n\n" +
			"For data files that live on the NAS and shouldn't be duplicated in\n" +
			"the ELN, use `mus eln sample` instead — it registers the file's\n" +
			"location as a Sample record without uploading bytes.\n\n" +
			"One FILE section per invocation; every file in the batch lands as\n" +
			"an attachment inside that section. Section header derives from -m.\n\n" +
			"Requires eln_experiment_id in the cascade (`mus eln link`).\n" +
			"Idempotent: sidecar stamps eln_attachment_journal_id on success; re-\n" +
			"runs skip files that already have it.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(message) == "" {
				return fmt.Errorf("-m / --message is required (describes what these files are)")
			}
			return runELNAttach(cmd, args, message)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"description of what these files are (REQUIRED); becomes the section header")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

func runELNAttach(cmd *cobra.Command, args []string, message string) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	expIDRaw := env.String("eln_experiment_id")
	if expIDRaw == "" {
		return fmt.Errorf("no eln_experiment_id in cascade — run `mus eln link <ID>` in this folder first")
	}
	expID, err := parseELNExperimentID(expIDRaw)
	if err != nil {
		return fmt.Errorf("invalid eln_experiment_id %q: %w", expIDRaw, err)
	}
	client, err := openELN()
	if err != nil {
		return err
	}

	files, err := collectSampleInputs(args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no eligible files")
	}

	// Filter files that already have an attachment recorded.
	var toUpload []string
	for _, p := range files {
		scPath := sidecar.SidecarPath(p)
		doc, _ := readMaybe(scPath)
		if doc != nil && doc.ELN != nil && doc.ELN.AttachmentJournalID != "" {
			fmt.Fprintf(stdout, "  ↷ %s already attached (section %s) — skipping\n",
				filepath.Base(p), doc.ELN.AttachmentJournalID)
			continue
		}
		toUpload = append(toUpload, p)
	}
	if len(toUpload) == 0 {
		fmt.Fprintln(stdout, "✓ nothing to upload — every file already has an attachment recorded")
		return nil
	}

	journalID, err := client.CreateFileSection(expID, "mus eln attach: "+firstLine(message))
	if err != nil {
		return fmt.Errorf("create FILE section: %w", err)
	}
	fmt.Fprintf(stdout, "created FILE section (expJournalID=%d) on experiment %d\n",
		journalID, expID)

	now := time.Now().UTC().Truncate(time.Second)
	journalStr := strconv.FormatInt(journalID, 10)
	for _, p := range toUpload {
		name := filepath.Base(p)
		fmt.Fprintf(stdout, "  → %s\n", name)
		if err := client.UploadFile(journalID, p, name); err != nil {
			fmt.Fprintf(stderr, "  ✘ upload %s: %v\n", name, err)
			continue
		}
		// Stamp sidecar.
		scPath := sidecar.SidecarPath(p)
		doc, _ := readMaybe(scPath)
		if doc == nil {
			doc = &sidecar.Doc{}
		}
		if doc.ELN == nil {
			doc.ELN = &sidecar.ELN{}
		}
		doc.ELN.AttachmentJournalID = journalStr
		doc.ELN.AttachmentUploadedAt = now
		if err := sidecar.Write(scPath, doc); err != nil {
			fmt.Fprintf(stderr, "  ⚠ sidecar write %s: %v (upload succeeded)\n", scPath, err)
		}
	}
	return nil
}
