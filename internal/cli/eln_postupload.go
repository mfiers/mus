package cli

// Post-upload ELN comment hook.
//
// After a successful `mus irods upload`, if the folder is linked to an ELN
// experiment (`eln_experiment_id` set in the cascade) AND the user has stored
// an ELN token, append a COMMENT section to that experiment summarising the
// uploads (one comment per command invocation, not per file).
//
// Strictly best-effort: every failure path logs a one-line warning to stderr
// and continues. The iRODS upload already succeeded by the time we're called.

import (
	"fmt"
	"html"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/folder"
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

// postUploadCommentToELN posts a single COMMENT section summarising `items`
// to the experiment named by env["eln_experiment_id"]. Returns silently
// (no error) on every failure path — the caller's upload already succeeded.
//
// Skips silently when:
//   - enabled == false (user passed --no-eln-comment)
//   - dryRun (no real bytes moved)
//   - items is empty
//   - the cascade has no eln_experiment_id (this folder isn't ELN-linked)
//   - the user hasn't run `mus eln login` (no token in secret store)
func postUploadCommentToELN(env *config.Env, items []uploadedItem,
	dryRun, enabled bool, message string, stderr io.Writer) {

	if !enabled || dryRun || len(items) == 0 {
		return
	}
	expIDStr := env.String("eln_experiment_id")
	if expIDStr == "" {
		return
	}
	expID, err := strconv.ParseInt(expIDStr, 10, 64)
	if err != nil {
		fmt.Fprintf(stderr, "  eln comment skipped: invalid eln_experiment_id %q\n", expIDStr)
		return
	}
	client, err := openELN()
	if err != nil {
		// Most common case: no token. Don't nag — folder may be ELN-linked
		// but this user isn't authenticated against ELN.
		return
	}

	// Warn about items that fell back to URL (no PURL) — that hints at a
	// transient `iron stat` failure during the upload and the comment will
	// show a path-based link instead of the persistent one. Visible to the
	// operator so they can re-run `mus irods upload` to refresh the PURL.
	for _, it := range items {
		if it.PURL == "" {
			fmt.Fprintf(stderr,
				"  NOTE: %s has no PURL — ELN link will be path-based and break on rename/move\n",
				it.RemotePath)
		}
	}

	title := buildELNCommentTitle(items)
	body := buildELNCommentHTML(env, items, message)

	jid, err := client.CreateCommentSection(expID, title, body)
	if err != nil {
		fmt.Fprintf(stderr, "  WARNING: could not post ELN comment: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "  posted ELN comment to experiment %d (section %d)\n", expID, jid)
}

// contentsCellHTML renders the "Contents" column for a folder / archive
// row. Falls back to em-dash for file rows (or when counts are absent).
func contentsCellHTML(it uploadedItem) string {
	if it.Kind != "folder" && it.Kind != "archive" {
		return "—"
	}
	if it.FileCount == 0 && it.DirCount == 0 {
		return "—"
	}
	parts := []string{pluralNoun(int(it.FileCount), "file")}
	if it.DirCount > 0 {
		parts = append(parts, pluralNoun(int(it.DirCount), "subdir"))
	}
	return html.EscapeString(strings.Join(parts, ", "))
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
		return
	}
	expID, err := strconv.ParseInt(expIDStr, 10, 64)
	if err != nil {
		fmt.Fprintf(stderr, "  eln sample-register skipped: invalid eln_experiment_id %q\n", expIDStr)
		return
	}
	client, err := openELN()
	if err != nil {
		// No token — folder may be ELN-linked but this operator
		// isn't authenticated. Silent skip; same policy as the
		// comment poster.
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

	// Same location resolution as `mus eln sample`. Non-fatal — a
	// missing unit falls back to zero and samples land unplaced.
	store, _ := secret.Open()
	storageLayerID := resolveSampleStorageLayerID(client, env, store, stderr)

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
		// All items failed; nothing to link.
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

func buildELNCommentTitle(items []uploadedItem) string {
	nFiles, nFolders, nArchives := 0, 0, 0
	for _, it := range items {
		switch it.Kind {
		case "file":
			nFiles++
		case "folder":
			nFolders++
		case "archive":
			nArchives++
		}
	}
	var parts []string
	if nFiles > 0 {
		parts = append(parts, pluralNoun(nFiles, "file"))
	}
	if nFolders > 0 {
		parts = append(parts, pluralNoun(nFolders, "folder"))
	}
	if nArchives > 0 {
		parts = append(parts, pluralNoun(nArchives, "archive"))
	}
	return "mus: uploaded " + strings.Join(parts, ", ") + " to iRODS"
}

func pluralNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// buildELNCommentHTML renders the comment body. Layout:
//
//   <p>summary line: who / when / data_project / total size</p>
//   <p>working directory</p>
//   <table>
//     Kind | iRODS path (clickable; href = PURL)  | Size | Sha256
//   </table>
//   <p>footnote when any item lacks a PURL (rare)</p>
//
// PURL is always the link target when present (persistent across moves /
// renames). If iron stat failed during the upload, we fall back to the
// path-based URL and add a one-line footnote so the reader knows why.
func buildELNCommentHTML(env *config.Env, items []uploadedItem, message string) string {
	var b strings.Builder

	host, _ := os.Hostname()
	var who string
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	cwd, _ := os.Getwd()

	// --- message (the depositor's -m, prominent at the top) ---
	if msg := strings.TrimSpace(message); msg != "" {
		fmt.Fprintf(&b,
			"<blockquote><p><strong>Upload message:</strong> %s</p></blockquote>",
			html.EscapeString(msg))
	}

	// --- summary line ---
	var total int64
	for _, it := range items {
		total += it.Size
	}
	fmt.Fprintf(&b,
		"<p>Uploaded via <code>mus</code> by <code>%s</code>@<code>%s</code> at %s — %d item(s), %s total.</p>",
		html.EscapeString(who),
		html.EscapeString(host),
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		len(items),
		html.EscapeString(folder.FormatHumanBytes(total)))

	// data_project pulled from the cascade so the iRODS membership is
	// recorded even if the comment is later exported away from the
	// experiment record. The experiment itself is obvious from context
	// (the comment lives on the experiment page) — don't duplicate it.
	if dp := env.String("data_project"); dp != "" {
		fmt.Fprintf(&b, "<p>data_project=<code>%s</code></p>", html.EscapeString(dp))
	}
	if cwd != "" {
		fmt.Fprintf(&b, "<p>Source: <code>%s:%s</code></p>",
			html.EscapeString(host),
			html.EscapeString(cwd))
	}

	// --- per-item table ---
	// Add the "Contents" column only when at least one item is a folder /
	// archive — keeps file-only uploads visually lean.
	showContents := false
	for _, it := range items {
		if it.Kind == "folder" || it.Kind == "archive" {
			showContents = true
			break
		}
	}
	anyFallback := false
	b.WriteString(`<table border="1" cellpadding="4" cellspacing="0">`)
	if showContents {
		b.WriteString(`<thead><tr><th>Kind</th><th>Name</th><th>Contents</th><th>Size</th><th>sha256</th></tr></thead><tbody>`)
	} else {
		b.WriteString(`<thead><tr><th>Kind</th><th>Name</th><th>Size</th><th>sha256</th></tr></thead><tbody>`)
	}
	for _, it := range items {
		// Always prefer PURL. Fall back to URL with a visible marker; the
		// stderr message above already nagged the operator.
		href := it.PURL
		marker := ""
		if href == "" && it.URL != "" {
			href = it.URL
			marker = " *"
			anyFallback = true
		}
		// Display the basename only — the comment lives on a known
		// experiment record and the full path is always
		// <irods_home>/project/<data_project>/<exp>/<basename>. The full
		// path is in title= so it shows on hover; the anchor still uses
		// the PURL as href.
		display := path.Base(it.RemotePath)
		if display == "" || display == "." || display == "/" {
			display = it.RemotePath
		}
		var pathCell string
		if href != "" {
			pathCell = fmt.Sprintf(`<a href="%s" title="%s"><code>%s</code></a>%s`,
				html.EscapeString(href),
				html.EscapeString(it.RemotePath),
				html.EscapeString(display),
				marker)
		} else {
			pathCell = fmt.Sprintf(`<code title="%s">%s</code>`,
				html.EscapeString(it.RemotePath),
				html.EscapeString(display))
		}
		shaCell := "—"
		if it.Sha256 != "" {
			shortSha := it.Sha256
			if len(shortSha) > 12 {
				shortSha = shortSha[:12]
			}
			shaCell = fmt.Sprintf(`<code title="%s">%s</code>`,
				html.EscapeString(it.Sha256), html.EscapeString(shortSha))
		}
		if showContents {
			fmt.Fprintf(&b,
				`<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				html.EscapeString(it.Kind),
				pathCell,
				contentsCellHTML(it),
				html.EscapeString(folder.FormatHumanBytes(it.Size)),
				shaCell)
		} else {
			fmt.Fprintf(&b,
				`<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				html.EscapeString(it.Kind),
				pathCell,
				html.EscapeString(folder.FormatHumanBytes(it.Size)),
				shaCell)
		}
	}
	b.WriteString(`</tbody></table>`)
	if anyFallback {
		b.WriteString(`<p><small>* path-based link only — PURL not available; ` +
			`will break if the object is renamed or moved.</small></p>`)
	}
	return b.String()
}
