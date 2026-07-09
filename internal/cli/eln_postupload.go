package cli

// Post-upload ELN sample-registration hook.
//
// After a successful `mus irods upload`, if the folder is linked to an ELN
// experiment (`eln_experiment_id` set in the cascade) AND the user has stored
// an ELN token, register each uploaded file as an ELN Sample on the same
// experiment (one SAMPLESOUT section per invocation). The samples ARE the
// ELN-side record of the upload — mus does not additionally post a comment
// section, since the samples cover the same information in structured form.
//
// Strictly best-effort: every failure path logs a one-line warning to stderr
// and continues. The iRODS upload already succeeded by the time we're called.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/secret"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// uploadedItem describes one successful upload from a `mus irods upload`
// invocation. File arg → "file"; folder uploaded as collection → "folder";
// folder packed and uploaded as tar.gz → "archive".
//
// Sha256 is the verifiable hash:
//   - file:    sha256 of the bytes
//   - archive: sha256 of the .tar.gz archive on disk
//   - folder:  Merkle hash over the directory tree (mus's recursive_sha256)
type uploadedItem struct {
	Kind       string
	LocalPath  string
	RemotePath string
	URL        string
	PURL       string
	Sha256     string
	Size       int64
	// FileCount / DirCount are populated for Kind == "folder" or "archive"
	// (the original source tree counts, regardless of whether we shipped a
	// flat collection or a single tarball). Zero for Kind == "file".
	FileCount int64
	DirCount  int64
}

// postUploadRegisterSamplesInELN creates one Sample record per uploaded
// item on the linked experiment via a SAMPLESOUT section. The sample's
// `path` meta field carries the iRODS path (NOT the local one — the
// point is that the sample references the archived, immutable copy);
// `upload_from_path` carries the local source so provenance is
// preserved. purl / sha256sum / size_bytes / mtime / upload_user /
// upload_from_server / upload_data / mus_version fill any of the
// tenant's declared fields (see eln_sample_provenance.go for the
// full auto-fill list).
//
// Same idempotency rules as `mus eln sample`:
//   - sidecar's `eln_sample_id` present + sample active on ELN + section
//     link recorded → skip.
//   - sidecar has SampleID but no SampleSectionID → orphan: re-link.
//   - sample archived / hard-deleted on ELN → recreate with the new
//     upload's metadata.
//   - no sidecar SampleID → fresh create.
//
// Strictly best-effort: every failure path logs a one-line warning to
// stderr and continues. The iRODS upload has already succeeded.
func postUploadRegisterSamplesInELN(env *config.Env, items []uploadedItem,
	dryRun, enabled bool, message string, stderr io.Writer) {

	if !enabled || dryRun || len(items) == 0 {
		return
	}
	expIDStr := env.String("eln_experiment_id")
	if expIDStr == "" {
		// Folder isn't ELN-linked. Not a bug — some folders are
		// intentionally offline. Say so once so the depositor
		// isn't left wondering where the samples went.
		fmt.Fprintln(stderr, "  ELN sample-register skipped: no eln_experiment_id in cascade (`mus eln link <ID>` to enable)")
		return
	}
	expID, err := strconv.ParseInt(expIDStr, 10, 64)
	if err != nil {
		fmt.Fprintf(stderr, "  ⚠ ELN sample-register skipped: invalid eln_experiment_id %q\n", expIDStr)
		return
	}
	client, err := openELN()
	if err != nil {
		// No token stored on this machine. This is the most common
		// silent-skip cause when a depositor sets up mus on a new
		// server — the folder IS ELN-linked (cascade carries
		// eln_experiment_id) but they haven't run `mus eln login`
		// on this box. Surface it loudly so they know.
		fmt.Fprintf(stderr,
			"  ⚠ ELN sample-register skipped: %v — run `mus eln login` on this machine to enable auto-sample-registration\n",
			err)
		return
	}

	// Sample type name: mus uses the same default as `mus eln sample`
	// ("Data"). Overridable per-folder via mus.cfg key `eln_sample_type`.
	sampleTypeName := env.String("eln_sample_type")
	if strings.TrimSpace(sampleTypeName) == "" {
		sampleTypeName = "Data"
	}
	st, err := client.FindSampleTypeByName(sampleTypeName)
	if err != nil {
		fmt.Fprintf(stderr,
			"  ⚠ ELN sample-register skipped: %v (set eln_sample_type in mus.cfg to override)\n",
			err)
		return
	}
	typeMetas, err := client.ListSampleTypeMetas(st.ID)
	if err != nil {
		fmt.Fprintf(stderr,
			"  ⚠ ELN sample-register: could not fetch sample-type meta fields: %v — auto-fill disabled\n", err)
		typeMetas = nil
	}

	// iRODS-upload samples get their own location key,
	// eln_irods_sample_location, so labs can route iRODS-uploaded
	// samples to a different storage unit from local-only ones. Zero
	// means "no placement"; a typo / missing unit warns and falls back
	// to zero. Sample creation proceeds regardless.
	store, _ := secret.Open()
	storageLayerID := resolveSampleStorageLayerID(client, env, store, ELNIRODSSampleLocationKey, stderr)

	// --- classify each uploaded item -------------------------------
	// Same buckets as runELNSample. The classifier lives inline here
	// rather than sharing with eln_sample.go because the two flows
	// differ in what they compute per item (paths, purls, kinds) and
	// squashing them into one signature turned out messier than the
	// duplication.
	type action int
	const (
		actSkip action = iota
		actFresh
		actOrphan
		actRecreate
	)
	type job struct {
		item        uploadedItem
		act         action
		doc         *sidecar.Doc
		scPath      string
		priorID     int64
		priorReason string
	}

	jobs := make([]job, 0, len(items))
	var skipped int
	for _, it := range items {
		// Folder + archive uploads don't have a single primary
		// sidecar — the local sidecar sits on the source folder
		// (Doc.Folder) or on the archive tarball, and it isn't in
		// the same "one sidecar per file" contract we use for
		// individual files. Register those items as fresh samples
		// every time (never skip, never orphan-recover); the sample
		// count-per-upload will be honest but no sidecar link is
		// carried between runs. Small downside for a small use case.
		if it.Kind != "file" {
			jobs = append(jobs, job{item: it, act: actFresh})
			continue
		}
		abs := it.LocalPath
		scPath := sidecar.SidecarPath(abs)
		doc, _ := sidecar.Read(scPath)
		if doc == nil || doc.ELN == nil || doc.ELN.SampleID == "" {
			jobs = append(jobs, job{item: it, act: actFresh, doc: doc, scPath: scPath})
			continue
		}
		id, perr := strconv.ParseInt(doc.ELN.SampleID, 10, 64)
		if perr != nil {
			jobs = append(jobs, job{item: it, act: actFresh, doc: doc, scPath: scPath})
			continue
		}
		remote, gerr := client.GetSample(id)
		switch {
		case eln.IsNotFound(gerr):
			jobs = append(jobs, job{
				item: it, act: actRecreate, doc: doc, scPath: scPath,
				priorID: id, priorReason: "gone from ELN",
			})
		case gerr != nil:
			// Transient error: best not to silently duplicate.
			// Warn and skip THIS item; other items in the batch
			// continue.
			fmt.Fprintf(stderr,
				"  ⚠ sample-register: lookup sample %d for %s failed: %v — skipping this item\n",
				id, filepath.Base(abs), gerr)
			skipped++
			continue
		case remote.Archived:
			jobs = append(jobs, job{
				item: it, act: actRecreate, doc: doc, scPath: scPath,
				priorID: id, priorReason: "archived",
			})
		case doc.ELN.SampleSectionID == "":
			// Sidecar never got a section-link stamp → orphan
			// from a prior failed batch-link.
			jobs = append(jobs, job{
				item: it, act: actOrphan, doc: doc, scPath: scPath, priorID: id,
			})
		case !remote.LinkedToExperiment(expID):
			// Sidecar says linked, server says not. Section was
			// deleted or the sample was unlinked from it — re-link
			// into a fresh section on this experiment.
			jobs = append(jobs, job{
				item: it, act: actOrphan, doc: doc, scPath: scPath, priorID: id,
			})
		default:
			skipped++
		}
	}

	pending := 0
	for _, j := range jobs {
		if j.act != actSkip {
			pending++
		}
	}
	if pending == 0 {
		if skipped > 0 {
			fmt.Fprintf(stderr,
				"  ELN sample-register: all %d file(s) already registered — nothing to do\n",
				skipped)
		}
		return
	}

	// --- create the section ---------------------------------------
	sectionHeader := "mus irods upload: " + firstLine(message)
	journalID, err := client.CreateExperimentSection(expID, "SAMPLESOUT", sectionHeader)
	if err != nil {
		fmt.Fprintf(stderr, "  ⚠ ELN sample-register: create SAMPLESOUT section: %v\n", err)
		return
	}

	type stampable struct {
		abs      string
		doc      *sidecar.Doc
		scPath   string
		sampleID int64
	}
	var pendingLink []int64
	var toStamp []stampable
	var fresh, recreated, relinked, failed int
	now := time.Now().UTC().Truncate(time.Second)
	journalStr := strconv.FormatInt(journalID, 10)

	for _, j := range jobs {
		it := j.item
		if j.act == actOrphan {
			pendingLink = append(pendingLink, j.priorID)
			toStamp = append(toStamp, stampable{
				abs: it.LocalPath, doc: j.doc, scPath: j.scPath, sampleID: j.priorID,
			})
			relinked++
			continue
		}
		// Fresh + Recreate: build provenance + create sample.
		prov := sampleProvenance{
			path:           it.RemotePath,
			uploadFromPath: it.LocalPath,
			sha256:         it.Sha256,
			sizeBytes:      it.Size,
			uploadTime:     now,
			purl:           it.PURL,
			kind:           it.Kind,
		}
		if fi, err := os.Stat(it.LocalPath); err == nil {
			prov.mtime = fi.ModTime()
		}
		provDict := buildProvenanceDict(prov)
		metas := metasForType(provDict, typeMetas)

		sample, err := client.CreateSample(eln.CreateSampleReq{
			SampleTypeID:   st.ID,
			Name:           filepath.Base(it.RemotePath),
			Description:    message,
			SampleMetas:    metas,
			StorageLayerID: storageLayerID,
		})
		if err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ ELN sample-register: create sample for %s: %v\n",
				filepath.Base(it.LocalPath), err)
			failed++
			continue
		}
		if j.act == actRecreate {
			recreated++
		} else {
			fresh++
		}
		pendingLink = append(pendingLink, sample.ID)

		// Update the sidecar with the new SampleID. Only file-kind
		// items carry sidecars in the standard mus contract; folder
		// / archive items skip the stamp step.
		if it.Kind == "file" {
			doc := j.doc
			if doc == nil {
				doc = &sidecar.Doc{}
			}
			if doc.ELN == nil {
				doc.ELN = &sidecar.ELN{}
			}
			doc.ELN.SampleID = strconv.FormatInt(sample.ID, 10)
			doc.ELN.SampleType = st.Name
			doc.ELN.SampleCreatedAt = now
			doc.ELN.SampleSectionID = "" // set after batch-link
			if err := sidecar.Write(j.scPath, doc); err != nil {
				fmt.Fprintf(stderr,
					"  ⚠ sidecar write %s: %v (sample already created)\n", j.scPath, err)
			}
			toStamp = append(toStamp, stampable{
				abs: it.LocalPath, doc: doc, scPath: j.scPath, sampleID: sample.ID,
			})
		}
	}

	if len(pendingLink) == 0 {
		// All items failed to create — nothing to link. This is
		// distinct from "nothing needed doing" (that case exits
		// earlier at `pending == 0`). Say so loudly so the
		// depositor knows they need to look at the per-item
		// warnings above.
		fmt.Fprintf(stderr,
			"  ⚠ ELN sample-register: 0 samples created (%d failed) — no section linked. See per-file errors above.\n",
			failed)
		return
	}
	if err := client.LinkSamplesToSection(journalID, pendingLink); err != nil {
		fmt.Fprintf(stderr,
			"  ⚠ ELN sample-register: link %d sample(s) to section %d: %v — orphans, re-run to recover\n",
			len(pendingLink), journalID, err)
		return
	}
	// Batch-link ok — stamp SampleSectionID on every touched sidecar.
	for _, s := range toStamp {
		s.doc.ELN.SampleSectionID = journalStr
		if err := sidecar.Write(s.scPath, s.doc); err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ sidecar write %s: %v (sample linked to section anyway)\n", s.scPath, err)
		}
	}
	fmt.Fprintf(stderr,
		"  registered %d ELN sample(s) on experiment %d (section %d) — fresh %d, recreated %d, relinked %d, skipped %d, failed %d\n",
		len(pendingLink), expID, journalID, fresh, recreated, relinked, skipped, failed)
}
