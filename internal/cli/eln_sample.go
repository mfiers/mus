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

// sampleAction says what needs to happen for one file in the batch.
// Classification runs BEFORE the section is created so we can bail out
// cleanly (and without leaving an empty section on the experiment) when
// every file is already fully-registered.
type sampleAction int

const (
	sampleSkip     sampleAction = iota // sidecar recorded a still-active sample linked to a section
	sampleFresh                        // no sidecar / no SampleID — create a brand-new sample
	sampleOrphan                       // sidecar has active SampleID but no SampleSectionID → re-link
	sampleRecreate                     // sidecar has SampleID but the remote sample is archived / gone → new sample
)

type sampleWork struct {
	abs         string
	action      sampleAction
	doc         *sidecar.Doc  // existing sidecar (may be nil)
	priorID     int64         // prior SampleID (orphan / recreate)
	priorReason string        // why we're recreating (archived / not-found) — for the summary line
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

	// --- classify each input BEFORE touching the ELN ----------------
	// A sidecar carrying SampleID does NOT prove the sample is still
	// registered — the depositor may have archived it in the meantime.
	// We ask the server for each recorded SampleID and demote the
	// classification to "recreate" when the sample is archived or has
	// been hard-deleted. Section-creation happens AFTER this pass so a
	// re-run where everything is already up-to-date doesn't spam an
	// empty section onto the experiment page.
	work := make([]sampleWork, 0, len(files))
	var skipped int
	for _, path := range files {
		abs, _ := filepath.Abs(path)
		scPath := sidecar.SidecarPath(abs)
		doc, _ := readMaybe(scPath)

		hasSampleID := doc != nil && doc.ELN != nil && doc.ELN.SampleID != ""
		if !hasSampleID {
			work = append(work, sampleWork{abs: abs, doc: doc, action: sampleFresh})
			continue
		}
		id, perr := strconv.ParseInt(doc.ELN.SampleID, 10, 64)
		if perr != nil {
			fmt.Fprintf(stderr, "  ⚠ %s sidecar has malformed eln_sample_id=%q — treating as fresh\n",
				filepath.Base(abs), doc.ELN.SampleID)
			work = append(work, sampleWork{abs: abs, doc: doc, action: sampleFresh})
			continue
		}

		// Ask the ELN: still there? still active?
		remote, gerr := client.GetSample(id)
		switch {
		case eln.IsNotFound(gerr):
			work = append(work, sampleWork{
				abs: abs, doc: doc, action: sampleRecreate,
				priorID: id, priorReason: "gone from ELN",
			})
		case gerr != nil:
			// Transient error looking up the sample — best not to
			// silently re-create (would duplicate on future retry).
			fmt.Fprintf(stderr, "  ✘ %s: lookup sample %d: %v\n",
				filepath.Base(abs), id, gerr)
			return fmt.Errorf("verify sample %d: %w", id, gerr)
		case remote.Archived:
			work = append(work, sampleWork{
				abs: abs, doc: doc, action: sampleRecreate,
				priorID: id, priorReason: "archived",
			})
		case doc.ELN.SampleSectionID == "":
			work = append(work, sampleWork{
				abs: abs, doc: doc, action: sampleOrphan, priorID: id,
			})
		default:
			work = append(work, sampleWork{abs: abs, doc: doc, action: sampleSkip})
			skipped++
		}
	}

	// Count how many files actually need action — if zero, report and
	// exit without creating a section.
	pending := 0
	for _, w := range work {
		if w.action != sampleSkip {
			pending++
		}
	}
	// Report the skips first — the depositor gets the same
	// per-file feedback whether or not any work follows.
	for _, w := range work {
		if w.action == sampleSkip {
			fmt.Fprintf(stdout, "  ↷ %s already registered as sample %s — skipping\n",
				filepath.Base(w.abs), w.doc.ELN.SampleID)
		}
	}
	if pending == 0 {
		fmt.Fprintf(stdout, "\nregistered 0 (fresh 0 + relinked orphans 0), skipped %d, failed 0\n",
			skipped)
		fmt.Fprintln(stdout, "nothing to do — no section created")
		return nil
	}

	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	// Now create the section — we know we have samples to attach.
	sectionHeader := "mus eln sample: " + firstLine(message)
	journalID, err := client.CreateExperimentSection(expID, sectionKind, sectionHeader)
	if err != nil {
		return fmt.Errorf("create %s section: %w", sectionKind, err)
	}
	fmt.Fprintf(stdout, "created section (expJournalID=%d) on experiment %d\n",
		journalID, expID)

	// pendingLink is the batch we'll PUT-link to the new section.
	// Fresh + recreate + orphan all end up here. toStamp is the
	// sidecar bookkeeping — updated only after the batch-link
	// succeeds, so a failed link leaves the sidecar honest
	// (SampleSectionID stays empty, so re-run picks the orphan path).
	type stampable struct {
		abs      string
		doc      *sidecar.Doc
		sampleID int64
		action   sampleAction
	}
	var pendingLink []int64
	var toStamp []stampable
	var failed, fresh, recreated, relinked int
	now := time.Now().UTC().Truncate(time.Second)
	journalStr := strconv.FormatInt(journalID, 10)

	for _, w := range work {
		if w.action == sampleSkip {
			continue
		}
		if w.action == sampleOrphan {
			fmt.Fprintf(stdout, "  ↺ %s: re-linking orphan sample %d\n",
				filepath.Base(w.abs), w.priorID)
			pendingLink = append(pendingLink, w.priorID)
			toStamp = append(toStamp, stampable{
				abs: w.abs, doc: w.doc, sampleID: w.priorID, action: w.action,
			})
			relinked++
			continue
		}

		// Fresh or Recreate: hash + create.
		fi, err := os.Stat(w.abs)
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ stat %s: %v\n", w.abs, err)
			failed++
			continue
		}
		sha, err := cache.Sum(w.abs)
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ hash %s: %v\n", w.abs, err)
			failed++
			continue
		}

		note := "path=" + w.abs + "\n" +
			"sha256=" + sha + "\n" +
			"size=" + strconv.FormatInt(fi.Size(), 10) + "\n" +
			"mtime=" + fi.ModTime().UTC().Format(time.RFC3339)

		sample, err := client.CreateSample(eln.CreateSampleReq{
			SampleTypeID: st.ID,
			Name:         filepath.Base(w.abs),
			Description:  message,
			Note:         note,
		})
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ create sample %s: %v\n", filepath.Base(w.abs), err)
			failed++
			continue
		}
		switch w.action {
		case sampleRecreate:
			fmt.Fprintf(stdout, "  ↻ %s: prior sample %d %s → new sample %d\n",
				filepath.Base(w.abs), w.priorID, w.priorReason, sample.ID)
			recreated++
		default:
			fmt.Fprintf(stdout, "  ✓ %s → sample %d\n", filepath.Base(w.abs), sample.ID)
			fresh++
		}
		pendingLink = append(pendingLink, sample.ID)

		// Update the sidecar with the new SampleID immediately —
		// safe because the sample exists on the server regardless
		// of what the link step does next. SampleSectionID stays
		// empty until the link succeeds; that empty state is the
		// orphan marker for the next run.
		doc := w.doc
		if doc == nil {
			doc = &sidecar.Doc{}
		}
		if doc.ELN == nil {
			doc.ELN = &sidecar.ELN{}
		}
		doc.ELN.SampleID = strconv.FormatInt(sample.ID, 10)
		doc.ELN.SampleType = st.Name
		doc.ELN.SampleCreatedAt = now
		doc.ELN.SampleSectionID = "" // reset — will be set post-link
		scPath := sidecar.SidecarPath(w.abs)
		if err := sidecar.Write(scPath, doc); err != nil {
			fmt.Fprintf(stderr, "  ⚠ sidecar write %s: %v (sample already created)\n",
				scPath, err)
		}
		toStamp = append(toStamp, stampable{
			abs: w.abs, doc: doc, sampleID: sample.ID, action: w.action,
		})
	}

	// Batch-link every pending sample (fresh + recreate + orphan) to
	// the section. Only stamp SampleSectionID on the sidecars AFTER
	// the link succeeds — otherwise the sidecar would lie.
	if len(pendingLink) > 0 {
		if err := client.LinkSamplesToSection(journalID, pendingLink); err != nil {
			fmt.Fprintf(stderr,
				"⚠ %d sample(s) touched but the section-link call failed: %v\n"+
					"   sample IDs: %v\n"+
					"   experiment: %d, expJournalID: %d\n"+
					"   Re-run `mus eln sample` to retry — mus detects orphans\n"+
					"   (sidecars with eln_sample_id but no eln_sample_section_id)\n"+
					"   and re-links them into a fresh section.\n",
				len(pendingLink), err, pendingLink, expID, journalID)
			return err
		}
		for _, s := range toStamp {
			s.doc.ELN.SampleSectionID = journalStr
			scPath := sidecar.SidecarPath(s.abs)
			if err := sidecar.Write(scPath, s.doc); err != nil {
				fmt.Fprintf(stderr,
					"  ⚠ sidecar write %s: %v (sample linked to section anyway)\n",
					scPath, err)
			}
		}
		fmt.Fprintf(stdout, "linked %d sample(s) to section %d on experiment %d\n",
			len(pendingLink), journalID, expID)
	}

	fmt.Fprintf(stdout,
		"\nregistered %d (fresh %d + recreated %d + relinked orphans %d), skipped %d, failed %d\n",
		len(toStamp), fresh, recreated, relinked, skipped, failed)
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
