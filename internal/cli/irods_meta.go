package cli

// applyMetadataToIRODS attaches the full resolved metadata snapshot
// from a sidecar Doc onto an iRODS object as AVU triplets via the
// `iron meta` family. Result: the object is fully self-describing on
// iRODS (queryable via `iquest`, visible in `iron meta ls`), and the
// local .mus / .env files don't need to be co-uploaded.
//
// Naming convention: every key is `mus_<field>` (or `mus_h5ad_<field>`
// for the h5ad-shape block) so mus's namespace doesn't collide with
// other tools' AVU schemas (e.g. KU Leuven's mango_mdschema).
//
// List-valued fields (tissue, cell_type, disease, condition, doi,
// pmid, link, h5ad_layers, *_ontology_term_id) translate to MULTIPLE
// triplets with the same attribute — iRODS's native multi-value form.
//
// Overwrite semantics: before writing, `iron stat -j` is used to read
// the existing AVU. Per-key:
//   - missing remotely → set silently
//   - same value(s)    → skip (no churn)
//   - different value(s) → set + print a one-line `~ key: old → new` warning
// We never RM keys that exist remotely but aren't in the new set;
// those may have been set by another tool or a future mus.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// avuFromDoc returns the full set of AVU triplets mus wants on the
// iRODS object for this sidecar. Single-valued entries map to a
// one-element slice; list-valued to a multi-element slice. Empty
// values are omitted.
func avuFromDoc(doc *sidecar.Doc) map[string][]string {
	out := map[string][]string{}
	put := func(key, value string) {
		if value != "" {
			out[key] = []string{value}
		}
	}
	putList := func(key string, values []string) {
		if len(values) > 0 {
			cp := make([]string, 0, len(values))
			for _, v := range values {
				if v != "" {
					cp = append(cp, v)
				}
			}
			if len(cp) > 0 {
				out[key] = cp
			}
		}
	}
	if doc.DataProject != "" {
		put("mus_data_project", doc.DataProject)
	}
	if doc.Bio != nil {
		b := doc.Bio
		// Scalars.
		put("mus_organism", b.Organism)
		put("mus_technology", b.Technology)
		put("mus_suspension_type", b.SuspensionType)
		put("mus_sex", b.Sex)
		put("mus_age", b.Age)
		put("mus_strain", b.Strain)
		put("mus_title", b.Title)
		put("mus_contributor", b.Contributor)
		put("mus_license", b.License)
		put("mus_source", b.Source)
		put("mus_source_description", b.SourceDescription)
		put("mus_access", b.Access)
		put("mus_accession", b.Accession)
		if b.Year > 0 {
			put("mus_year", strconv.Itoa(b.Year))
		}
		// Lists.
		putList("mus_tissue", b.Tissue)
		putList("mus_cell_type", b.CellType)
		putList("mus_disease", b.Disease)
		putList("mus_condition", b.Condition)
		putList("mus_doi", b.DOI)
		putList("mus_pmid", b.PMID)
		putList("mus_link", b.Link)
		// Ontology IDs.
		put("mus_organism_ontology_term_id", b.OrganismOntologyTermID)
		put("mus_assay_ontology_term_id", b.AssayOntologyTermID)
		put("mus_sex_ontology_term_id", b.SexOntologyTermID)
		putList("mus_tissue_ontology_term_id", b.TissueOntologyTermID)
		putList("mus_cell_type_ontology_term_id", b.CellTypeOntologyTermID)
		putList("mus_disease_ontology_term_id", b.DiseaseOntologyTermID)
	}
	if doc.H5ad != nil {
		h := doc.H5ad
		if h.NObs > 0 {
			put("mus_h5ad_n_obs", strconv.FormatInt(h.NObs, 10))
		}
		if h.NVars > 0 {
			put("mus_h5ad_n_vars", strconv.FormatInt(h.NVars, 10))
		}
		putList("mus_h5ad_layers", h.Layers)
		put("mus_h5ad_embedding", h.Embedding)
		put("mus_h5ad_anndata_version", h.AnnDataVersion)
		put("mus_h5ad_gene_id_type", h.GeneIDType)
		if !h.InspectedAt.IsZero() {
			put("mus_h5ad_inspected_at", h.InspectedAt.UTC().Format("2006-01-02T15:04:05Z"))
		}
		put("mus_h5ad_inspected_sha256", h.InspectedSha256)
		put("mus_h5ad_inspected_vocab_sha256", h.InspectedVocabSha256)
		if h.InspectedCheckVersion > 0 {
			put("mus_h5ad_inspected_check_version", strconv.Itoa(h.InspectedCheckVersion))
		}
	}
	if doc.ELN != nil {
		put("mus_eln_experiment_id", doc.ELN.ExperimentID)
		put("mus_eln_experiment_name", doc.ELN.ExperimentName)
		put("mus_eln_project_name", doc.ELN.ProjectName)
		put("mus_eln_study_name", doc.ELN.StudyName)
	}
	put("mus_sha256", doc.File.Sha256)
	if doc.Folder != nil {
		put("mus_recursive_sha256", doc.Folder.RecursiveSha256)
		if doc.Folder.FileCount > 0 {
			put("mus_file_count", strconv.FormatInt(doc.Folder.FileCount, 10))
		}
		if doc.Folder.DirCount > 0 {
			put("mus_dir_count", strconv.FormatInt(doc.Folder.DirCount, 10))
		}
	}
	if doc.Archive != nil {
		put("mus_archive_format", doc.Archive.Format)
		put("mus_archive_sha256", doc.Archive.Sha256)
	}
	if doc.IRODS != nil && !doc.IRODS.UploadedAt.IsZero() {
		put("mus_uploaded_at",
			doc.IRODS.UploadedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if doc.IRODS != nil {
		put("mus_upload_message", doc.IRODS.Message)
	}
	return out
}

// mergeAVUIntoDoc folds the AVU map (as returned by iron stat -j)
// into the local sidecar Doc. Semantics:
//
//   - field present in AVU and missing locally  → silent fill
//   - field present in both with same value     → silent no-op
//   - field present in both with different value → log
//     "~ key: old → new" to stderr AND overwrite the sidecar
//
// Returns true if doc was mutated (caller should save the sidecar).
//
// Fields that represent CONTENT IDENTITY rather than metadata are
// excluded: mus_sha256, mus_recursive_sha256, mus_archive_sha256,
// mus_uploaded_at. Those are bookkeeping pinned to the upload
// moment, not facts about the dataset.
func mergeAVUIntoDoc(doc *sidecar.Doc, avu map[string][]string, stderr io.Writer) bool {
	ctx := &avuMergeCtx{avu: avu, stderr: stderr}

	ctx.scalar(&doc.DataProject, "mus_data_project")

	// Bio block — lazy-allocate if AVU carries any bio key.
	if doc.Bio == nil && hasAnyKey(avu, bioAVUKeys...) {
		doc.Bio = &sidecar.Bio{}
		ctx.mutated = true
	}
	if doc.Bio != nil {
		b := doc.Bio
		ctx.scalar(&b.Organism, "mus_organism")
		ctx.scalar(&b.Technology, "mus_technology")
		ctx.scalar(&b.SuspensionType, "mus_suspension_type")
		ctx.scalar(&b.Sex, "mus_sex")
		ctx.scalar(&b.Age, "mus_age")
		ctx.scalar(&b.Strain, "mus_strain")
		ctx.scalar(&b.Title, "mus_title")
		ctx.scalar(&b.Contributor, "mus_contributor")
		ctx.scalar(&b.License, "mus_license")
		ctx.scalar(&b.Source, "mus_source")
		ctx.scalar(&b.SourceDescription, "mus_source_description")
		ctx.scalar(&b.Access, "mus_access")
		ctx.scalar(&b.Accession, "mus_accession")
		ctx.intField(&b.Year, "mus_year")
		ctx.list(&b.Tissue, "mus_tissue")
		ctx.list(&b.CellType, "mus_cell_type")
		ctx.list(&b.Disease, "mus_disease")
		ctx.list(&b.Condition, "mus_condition")
		ctx.list(&b.DOI, "mus_doi")
		ctx.list(&b.PMID, "mus_pmid")
		ctx.list(&b.Link, "mus_link")
		ctx.scalar(&b.OrganismOntologyTermID, "mus_organism_ontology_term_id")
		ctx.scalar(&b.AssayOntologyTermID, "mus_assay_ontology_term_id")
		ctx.scalar(&b.SexOntologyTermID, "mus_sex_ontology_term_id")
		ctx.list(&b.TissueOntologyTermID, "mus_tissue_ontology_term_id")
		ctx.list(&b.CellTypeOntologyTermID, "mus_cell_type_ontology_term_id")
		ctx.list(&b.DiseaseOntologyTermID, "mus_disease_ontology_term_id")
	}

	if doc.H5ad == nil && hasAnyKey(avu, h5adAVUKeys...) {
		doc.H5ad = &sidecar.H5ad{}
		ctx.mutated = true
	}
	if doc.H5ad != nil {
		h := doc.H5ad
		ctx.int64Field(&h.NObs, "mus_h5ad_n_obs")
		ctx.int64Field(&h.NVars, "mus_h5ad_n_vars")
		ctx.list(&h.Layers, "mus_h5ad_layers")
		ctx.scalar(&h.Embedding, "mus_h5ad_embedding")
		ctx.scalar(&h.AnnDataVersion, "mus_h5ad_anndata_version")
		ctx.scalar(&h.GeneIDType, "mus_h5ad_gene_id_type")
		ctx.timeField(&h.InspectedAt, "mus_h5ad_inspected_at")
		ctx.scalar(&h.InspectedSha256, "mus_h5ad_inspected_sha256")
		ctx.scalar(&h.InspectedVocabSha256, "mus_h5ad_inspected_vocab_sha256")
		ctx.intField(&h.InspectedCheckVersion, "mus_h5ad_inspected_check_version")
	}

	if doc.ELN == nil && hasAnyKey(avu, elnAVUKeys...) {
		doc.ELN = &sidecar.ELN{}
		ctx.mutated = true
	}
	if doc.ELN != nil {
		ctx.scalar(&doc.ELN.ExperimentID, "mus_eln_experiment_id")
		ctx.scalar(&doc.ELN.ExperimentName, "mus_eln_experiment_name")
		ctx.scalar(&doc.ELN.ProjectName, "mus_eln_project_name")
		ctx.scalar(&doc.ELN.StudyName, "mus_eln_study_name")
	}

	// Upload message lives on the IRODS block; lazy-allocate if needed.
	if _, ok := avu["mus_upload_message"]; ok {
		if doc.IRODS == nil {
			doc.IRODS = &sidecar.IRODS{}
			ctx.mutated = true
		}
		ctx.scalar(&doc.IRODS.Message, "mus_upload_message")
	}

	// Folder/Archive: only the COUNT fields are metadata-like and worth
	// merging — the sha256 fields are content identity and excluded.
	if doc.Folder != nil {
		ctx.int64Field(&doc.Folder.FileCount, "mus_file_count")
		ctx.int64Field(&doc.Folder.DirCount, "mus_dir_count")
	}
	if doc.Archive != nil {
		ctx.scalar(&doc.Archive.Format, "mus_archive_format")
	}

	return ctx.mutated
}

// avuMergeCtx is the per-merge state passed through scalar/list/int
// helpers so they can record mutation and emit warning lines.
type avuMergeCtx struct {
	avu     map[string][]string
	stderr  io.Writer
	mutated bool
}

func (c *avuMergeCtx) scalar(target *string, key string) {
	vals, ok := c.avu[key]
	if !ok || len(vals) == 0 || vals[0] == "" {
		return
	}
	newVal := vals[0]
	if *target == newVal {
		return
	}
	if *target == "" {
		*target = newVal
		c.mutated = true
		return
	}
	fmt.Fprintf(c.stderr, "  ~ %s: %s → %s\n", key, *target, newVal)
	*target = newVal
	c.mutated = true
}

func (c *avuMergeCtx) list(target *[]string, key string) {
	vals, ok := c.avu[key]
	if !ok || len(vals) == 0 {
		return
	}
	if stringSetsEqual(*target, vals) {
		return
	}
	cp := append([]string(nil), vals...)
	if len(*target) == 0 {
		*target = cp
		c.mutated = true
		return
	}
	fmt.Fprintf(c.stderr, "  ~ %s: %s → %s\n",
		key, joinShort(*target), joinShort(vals))
	*target = cp
	c.mutated = true
}

func (c *avuMergeCtx) intField(target *int, key string) {
	vals, ok := c.avu[key]
	if !ok || len(vals) == 0 {
		return
	}
	n, err := strconv.Atoi(vals[0])
	if err != nil {
		return
	}
	if *target == n {
		return
	}
	if *target == 0 {
		*target = n
		c.mutated = true
		return
	}
	fmt.Fprintf(c.stderr, "  ~ %s: %d → %d\n", key, *target, n)
	*target = n
	c.mutated = true
}

func (c *avuMergeCtx) int64Field(target *int64, key string) {
	vals, ok := c.avu[key]
	if !ok || len(vals) == 0 {
		return
	}
	n, err := strconv.ParseInt(vals[0], 10, 64)
	if err != nil {
		return
	}
	if *target == n {
		return
	}
	if *target == 0 {
		*target = n
		c.mutated = true
		return
	}
	fmt.Fprintf(c.stderr, "  ~ %s: %d → %d\n", key, *target, n)
	*target = n
	c.mutated = true
}

func (c *avuMergeCtx) timeField(target *time.Time, key string) {
	vals, ok := c.avu[key]
	if !ok || len(vals) == 0 {
		return
	}
	parsed, err := time.Parse("2006-01-02T15:04:05Z", vals[0])
	if err != nil {
		return
	}
	if target.Equal(parsed) {
		return
	}
	if target.IsZero() {
		*target = parsed
		c.mutated = true
		return
	}
	fmt.Fprintf(c.stderr, "  ~ %s: %s → %s\n",
		key, target.UTC().Format("2006-01-02T15:04:05Z"),
		parsed.UTC().Format("2006-01-02T15:04:05Z"))
	*target = parsed
	c.mutated = true
}

// hasAnyKey reports whether the AVU map has at least one of the
// listed keys. Used for lazy-allocating sub-blocks (Bio, H5ad, …).
func hasAnyKey(avu map[string][]string, keys ...string) bool {
	for _, k := range keys {
		if _, ok := avu[k]; ok {
			return true
		}
	}
	return false
}

// Sub-block AVU key lists for lazy allocation. Kept as package vars
// so they can't drift from avuFromDoc / mergeAVUIntoDoc.
var (
	bioAVUKeys = []string{
		"mus_organism", "mus_technology", "mus_suspension_type",
		"mus_sex", "mus_age", "mus_strain", "mus_title",
		"mus_contributor", "mus_license", "mus_source",
		"mus_source_description", "mus_access", "mus_accession",
		"mus_year", "mus_tissue", "mus_cell_type", "mus_disease",
		"mus_condition", "mus_doi", "mus_pmid", "mus_link",
		"mus_organism_ontology_term_id", "mus_assay_ontology_term_id",
		"mus_sex_ontology_term_id", "mus_tissue_ontology_term_id",
		"mus_cell_type_ontology_term_id", "mus_disease_ontology_term_id",
	}
	h5adAVUKeys = []string{
		"mus_h5ad_n_obs", "mus_h5ad_n_vars", "mus_h5ad_layers",
		"mus_h5ad_embedding", "mus_h5ad_anndata_version",
		"mus_h5ad_gene_id_type", "mus_h5ad_inspected_at",
		"mus_h5ad_inspected_sha256", "mus_h5ad_inspected_vocab_sha256",
		"mus_h5ad_inspected_check_version",
	}
	elnAVUKeys = []string{
		"mus_eln_experiment_id", "mus_eln_experiment_name",
		"mus_eln_project_name", "mus_eln_study_name",
	}
)

// applyMetadataToIRODS writes the AVU set from the sidecar onto the
// iRODS object at remotePath. Diff-and-warn: skips no-op writes,
// surfaces value changes one line per key. Per-key failures log to
// stderr but don't abort the upload (metadata is bonus).
func applyMetadataToIRODS(
	ctx context.Context,
	client *iron.Client,
	remotePath string,
	doc *sidecar.Doc,
	stderr io.Writer,
) {
	want := avuFromDoc(doc)
	if len(want) == 0 {
		return
	}

	// Read existing AVU via stat -j. If stat fails (e.g. transient
	// network), proceed with naive set — worst case we redo writes
	// that wouldn't have been needed.
	var have map[string][]string
	if info, err := client.Stat(ctx, remotePath); err == nil {
		have = info.MetadataMap()
	}

	// Process keys in sorted order so the warning lines are stable.
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		newVals := want[key]
		oldVals, hadKey := have[key]
		if hadKey && stringSetsEqual(oldVals, newVals) {
			continue // no churn
		}
		if hadKey {
			fmt.Fprintf(stderr, "  ~ %s: %s → %s\n",
				key, joinShort(oldVals), joinShort(newVals))
		}
		if err := setAVU(ctx, client, remotePath, key, newVals); err != nil {
			fmt.Fprintf(stderr,
				"  WARNING: iron meta failed for %s: %v\n", key, err)
		}
	}
}

// setAVU writes one logical attribute, using `iron meta set` for the
// single-valued case (which both replaces existing and sets new) and
// `iron meta unset` + per-value `iron meta add` for list-valued.
func setAVU(ctx context.Context, client *iron.Client, remote, key string, vals []string) error {
	if len(vals) == 1 {
		return client.MetaSet(ctx, remote, key, vals[0])
	}
	// Multi-value: clear then re-add. iron set would collapse to one
	// triplet; we need the multi-triplet form.
	if err := client.MetaUnset(ctx, remote, key); err != nil {
		return fmt.Errorf("unset: %w", err)
	}
	for _, v := range vals {
		if err := client.MetaAdd(ctx, remote, key, v); err != nil {
			return fmt.Errorf("add %q: %w", v, err)
		}
	}
	return nil
}

// stringSetsEqual returns true when a and b carry the same elements,
// order-insensitively (the iRODS AVU set has no defined order).
// Duplicates within a single slice are ignored (a slice with
// duplicates equals a slice with the same elements deduped).
func stringSetsEqual(a, b []string) bool {
	sa := dedupeSorted(a)
	sb := dedupeSorted(b)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	cp := make([]string, len(in))
	copy(cp, in)
	sort.Strings(cp)
	out := cp[:1]
	for i := 1; i < len(cp); i++ {
		if cp[i] != cp[i-1] {
			out = append(out, cp[i])
		}
	}
	return out
}

// joinShort renders a list of values for a warning line. Truncates
// any single value longer than 60 chars; joins multi-value with ",".
func joinShort(vals []string) string {
	if len(vals) == 0 {
		return "(none)"
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		if len(v) > 60 {
			v = v[:57] + "..."
		}
		parts[i] = v
	}
	return strings.Join(parts, ",")
}
