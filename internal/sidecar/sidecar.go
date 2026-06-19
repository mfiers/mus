// Package sidecar reads and writes `*.mus` sidecar files that record a
// single data file's checksum, size, mtime, and remote-storage state.
//
// Format: the same flat KEY=VALUE grammar used by `.env` folder configs (see
// internal/envformat). Field names are underscore-prefixed:
//
//	sha256=...
//	size=12345
//	mtime=2026-05-23T...
//	irods_path=/zone/home/lab/exp_42/foo.csv
//	irods_status=uploaded
//	eln_experiment_id=12345
//	tags=raw,qc-pass
//
// Naming: for a data file `foo.h5ad`, the sidecar is `foo.h5ad.mus`. The
// sidecar lives next to the data file. The folder-config file is `.env`
// (handled by package config); since sidecars require a non-empty basename
// before `.mus`, the two cannot collide.
package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/atomicfile"
	"codeberg.org/mfiers/mus/internal/envformat"
)

// Suffix is the sidecar filename suffix.
const Suffix = ".mus"

// FolderConfig is the canonical folder-level config filename
// (handled by package config). FolderConfigLegacy is the pre-0.2.12
// name we still recognise until every project has run
// `mus config migrate`. Named here only so IsSidecar can reject
// them when scanning a folder.
const (
	FolderConfig       = "mus.cfg"
	FolderConfigLegacy = ".env"
)

// FileInfo is the data-file metadata.
type FileInfo struct {
	Sha256  string
	Size    int64
	Mtime   time.Time
	Hashed  time.Time
	Host    string
	AbsPath string
}

// IRODS captures iRODS upload state.
type IRODS struct {
	URL        string // path-based browse URL — convenient but breaks on moves
	PURL       string // persistent URL keyed on catalog id — survives moves
	Path       string // canonical iRODS path
	Status     string // ok, mismatch, pending, uploaded, ...
	UploadedAt time.Time
	UploadedBy string
	// Message is the depositor's `-m` upload note from the most recent
	// `mus irods upload`. Like a git commit message — describes WHY this
	// upload happened. Last-write-wins per file across multiple uploads.
	Message string
}

// ELN captures eLabJournal experiment context.
type ELN struct {
	ExperimentID   string
	ExperimentName string
	StudyID        string
	StudyName      string
	ProjectID      string
	ProjectName    string
	JournalID      string
}

// S3 captures S3 upload state (`mus s3 upload`).
type S3 struct {
	URL        string
	Bucket     string
	Key        string
	Etag       string
	UploadedAt time.Time
}

// Bio captures broadly-applicable experiment metadata — organism,
// tissue, cell_type, etc. — that typically applies to ALL data files
// in a project, not just h5ad files. These fields are written as
// bare KEY=VALUE lines (no prefix) so the same `.env` cascade keys
// also apply when mus grows non-h5ad data tools.
//
// Read order at deposit time (lowest → highest precedence): cascade
// .env (bare keys), then this struct (per-file override in sidecar).
//
// Schema v2 (joint vocabulary, doc/joint_vocabulary_proposal.md):
// CellxGene-aligned naming. Field renames from v1: Celltype →
// CellType; Preparation → SuspensionType. Removed: Region (collapsed
// into Tissue per UBERON's flat anatomy). Split: Internal (yes/no)
// → Access + Source.
type Bio struct {
	// CellxGene-aligned biological annotation.
	Organism       string
	Tissue         []string // UBERON labels (may include brain regions)
	CellType       []string // CL labels
	Technology     string   // protocol/chemistry (mus-curated)
	SuspensionType string   // cell / nucleus / na
	Disease        []string // MONDO labels
	Condition      []string // freeform; experimental condition
	Sex            string
	Age            string // freeform with unit: 87y, 18m, P30
	Strain         string // mouse strain (C57BL/6 etc.)

	// Provenance.
	Title             string
	Contributor       string
	Year              int
	DOI               []string // multiple papers allowed
	PMID              []string
	Link              []string // generic URLs
	Source            string   // internal / external
	License           string
	SourceDescription string   // "downloaded from CELLxGENE 2026-04-12"

	// Deposit status.
	Access    string // internal / public
	Accession string

	// Optional ontology-ID companions for unambiguous identification.
	// mus stores them but does not currently resolve them — labels are
	// the validation primary; IDs are the depositor's hint for future
	// downstream tools.
	OrganismOntologyTermID string
	TissueOntologyTermID   []string // one per Tissue entry, in order
	CellTypeOntologyTermID []string // one per CellType entry, in order
	AssayOntologyTermID    string   // EFO for technology+suspension
	SexOntologyTermID      string
	DiseaseOntologyTermID  []string // one per Disease entry, in order
}

// H5ad captures the genuinely .h5ad-file-specific fields stamped at
// inspection time: file shape, anndata version, sha pin for the
// "cached pass" optimisation. Persisted under the h5ad_ prefix so it
// can't collide with future per-file blocks for other formats.
//
// All fields are optional; an empty H5ad block means no inspection
// has been recorded yet.
type H5ad struct {
	NObs           int64
	NVars          int64
	Layers         []string
	Embedding      string // "X_umap" preferred, else "X_tsne"
	AnnDataVersion string
	GeneIDType     string

	InspectedAt time.Time

	// InspectedSha256 is the sha256 of the .h5ad file at the moment
	// `mus h5ad check` ran. `mus check` compares the file's current
	// sha against this: equal → cached pass; differ → re-inspect.
	InspectedSha256 string

	// InspectedVocabSha256 is the sha256 of the raw vocabulary YAML
	// in effect at check time (mus's vocab.LoadResult.ContentSha256
	// for whichever source — codeberg / cache / embedded — actually
	// fed the validator). If the vocab has changed since, the
	// recorded PASS may no longer reflect today's rules; the cached
	// pass is invalidated and mus re-runs.
	InspectedVocabSha256 string

	// InspectedCheckVersion records mus's h5ad.CheckVersion at check
	// time. Bumped when the structural / soft / derived rules
	// change. A sidecar whose value is < the running constant is
	// stale; the cached pass is invalidated and mus re-runs.
	InspectedCheckVersion int
}

// Doc is the full sidecar document.
type Doc struct {
	Version     int
	Kind        string // "" (default; file sidecar) or "folder" (folder-level sidecar)
	Note        string
	Tags        []string
	DataProject string // NameYear group label (e.g. "Fiers2025"); empty if not set
	Created     time.Time
	Updated     time.Time
	File        FileInfo
	Folder      *FolderInfo
	Archive     *ArchiveInfo
	IRODS       *IRODS
	ELN         *ELN
	S3          *S3
	Bio         *Bio
	H5ad        *H5ad
	DE          *DE
}

// DE is populated on sidecars for differential-expression result
// files (.tsv / .csv that pass `mus de check`). Persisted with the
// `de_*` key prefix so multiple per-file annotation layers (Bio,
// H5ad, DE) can coexist in one envformat document without name
// collisions.
//
// Term / Contrast are LIST-typed when the depositor sets
// de_term=auto / de_contrast=auto on a `long`-format file —
// `mus de check` computes the distinct {term × contrast} set from
// the body and stamps it sorted+comma-joined. For one-term /
// one-contrast presets (deseq2 etc) they're single-value strings.
type DE struct {
	Format             string   // de_format: deseq2 | edger | scanpy | seurat | glmmseq | glmmtmb | long
	Method             string   // de_method: auto-filled from Format if unset (DESeq2, edgeR, …)
	Term               []string // de_term: stratification term(s); list when long-format auto
	Contrast           []string // de_contrast: contrast label(s); list when long-format auto
	Cluster            string   // de_cluster: per-file cluster label, e.g. "microglia.DAM" (free text)
	DatasetID           string   // de_dataset_id: 3-char anthive id of the target h5ad (preferred)
	DatasetH5ad         string   // de_dataset_h5ad: basename within data_project (escape hatch for co-ingest)
	ClusterObsColumn   string   // de_cluster_obs_column: obs column in target h5ad for cluster validation
	SoftwareVersion    string   // de_software_version: e.g. "DESeq2 1.42.0"
	ReferenceType      string   // de_reference_type: e.g. "vsreference" / "pairwise" / "vsgrandmean"
	Covariates         []string // de_covariates: e.g. ["sex", "age", "batch"]
	FilterCriteria     string   // de_filter_criteria: e.g. "expressed in ≥3 cells per sample"
	Description        string   // de_description: free text
	RowCount           int64    // de_row_count: data rows in the file at check time

	// Cache keys — same shape as the H5ad block.
	InspectedAt           time.Time // de_check_passed_at
	InspectedSha256       string    // de_inspected_sha256: file sha at check time
	InspectedCheckVersion int       // de_check_version: bumps invalidate the cached PASS
}

// FolderInfo is populated on folder-level sidecars (Kind=="folder").
type FolderInfo struct {
	FileCount       int64
	DirCount        int64   // subdirectories (recursive, root excluded)
	TotalBytes      int64
	MedianBytes     int64   // 0 if FileCount == 0
	Density         float64 // MedianBytes / FileCount²
	RecursiveSha256 string  // Merkle-style hash of (sorted relpath, sha256) lines
}

// ArchiveInfo is populated when a folder was packed before upload.
// It describes the archive object that lives on iRODS.
type ArchiveInfo struct {
	Format   string // "tar.gz"
	Filename string // e.g. "scripts.tar.gz"
	Size     int64
	Sha256   string
	PURL     string // persistent URL of the archive object
}

// SidecarPath returns the sidecar path for a given data file.
func SidecarPath(dataFile string) string { return dataFile + Suffix }

// DataPath returns the data-file path for a given sidecar.
func DataPath(sidecar string) string {
	if !strings.HasSuffix(sidecar, Suffix) {
		return sidecar
	}
	return sidecar[:len(sidecar)-len(Suffix)]
}

// IsSidecar reports whether `path` looks like a sidecar (ends in
// .mus) AND has a non-empty base name (i.e. is neither the folder
// config `mus.cfg` nor its legacy alias `.env`).
func IsSidecar(path string) bool {
	base := filepath.Base(path)
	if base == FolderConfig || base == FolderConfigLegacy {
		return false
	}
	return strings.HasSuffix(base, Suffix) && len(base) > len(Suffix)
}

// sidecarListKeys lists the sidecar fields that are comma-separated lists.
var sidecarListKeys = map[string]bool{
	"tags": true,
	// Bio (bare; broadly applicable across data files):
	"tissue":                     true,
	"cell_type":                  true,
	"disease":                    true,
	"condition":                  true,
	"doi":                        true,
	"pmid":                       true,
	"link":                       true,
	"tissue_ontology_term_id":    true,
	"cell_type_ontology_term_id": true,
	"disease_ontology_term_id":   true,
	// H5ad (file-shape; truly h5ad-specific):
	"h5ad_layers": true,
	// DE (differential-expression result files). de_term + de_contrast
	// become multi-value when `de_format=long` and the depositor set
	// term=auto / contrast=auto — `mus de check` computes the distinct
	// set across the body and stamps it sorted+comma-joined. For
	// single-term presets (deseq2 etc) they're still single strings
	// but the list-typed path serialises a 1-element list just fine.
	"de_term":       true,
	"de_contrast":   true,
	"de_covariates": true,
}

func parseOpts() envformat.Options {
	return envformat.Options{ListKeys: sidecarListKeys}
}

// ParseOpts returns the envformat options used when parsing sidecar
// files. Exposed so external callers (e.g. the S3 upload's collapsed
// sidecar builder) can re-use the same list-key discipline when
// merging cascade keys with sidecar keys.
func ParseOpts() envformat.Options { return parseOpts() }

// Read parses a sidecar file. Returns os.IsNotExist-compatible error if
// missing.
func Read(path string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values, err := envformat.Parse(raw, parseOpts())
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Single-file: resolve any `-prefix` items to canonical form.
	envformat.Resolve(values, parseOpts())
	return docFromMap(values)
}

// Write atomically replaces the sidecar at `path` with `d`. Updates the
// Updated field to now. If d.Created is zero, sets it to now.
func Write(path string, d *Doc) error {
	if d.Version == 0 {
		d.Version = 1
	}
	now := time.Now().UTC().Truncate(time.Second)
	if d.Created.IsZero() {
		d.Created = now
	}
	d.Updated = now

	values := docToMap(d)
	raw, err := envformat.Marshal(values)
	if err != nil {
		return err
	}
	header := []byte("# mus sidecar — generated, do not hand-edit unless you know what you're doing\n")
	out := append(header, raw...)

	return atomicfile.Replace(path, out, 0o644)
}

// Stale returns true if the sidecar's recorded size or mtime no longer
// matches the actual file, meaning the sha256 in the sidecar can no longer be
// trusted.
func (d *Doc) Stale(st os.FileInfo) bool {
	if d == nil {
		return true
	}
	if d.File.Size != st.Size() {
		return true
	}
	if !d.File.Mtime.IsZero() && !d.File.Mtime.Equal(st.ModTime().UTC().Truncate(time.Second)) {
		return true
	}
	return false
}

// --- serialisation ----------------------------------------------------------

// docToMap flattens a Doc to a key=value map for envformat.Marshal.
// Empty fields are omitted to keep output compact.
func docToMap(d *Doc) map[string]any {
	out := map[string]any{}

	out["version"] = strconv.Itoa(d.Version)
	putTime(out, "created", d.Created)
	putTime(out, "updated", d.Updated)
	putStr(out, "kind", d.Kind)
	putStr(out, "note", d.Note)
	putStr(out, "data_project", d.DataProject)
	if len(d.Tags) > 0 {
		out["tags"] = append([]string(nil), d.Tags...)
	}

	if d.Folder != nil {
		if d.Folder.FileCount > 0 {
			out["file_count"] = strconv.FormatInt(d.Folder.FileCount, 10)
		}
		if d.Folder.DirCount > 0 {
			out["dir_count"] = strconv.FormatInt(d.Folder.DirCount, 10)
		}
		if d.Folder.TotalBytes > 0 {
			out["total_size"] = strconv.FormatInt(d.Folder.TotalBytes, 10)
		}
		if d.Folder.MedianBytes > 0 {
			out["median_size"] = strconv.FormatInt(d.Folder.MedianBytes, 10)
		}
		putStr(out, "recursive_sha256", d.Folder.RecursiveSha256)
	}
	if d.Archive != nil {
		putStr(out, "archive_format", d.Archive.Format)
		putStr(out, "archive_filename", d.Archive.Filename)
		if d.Archive.Size > 0 {
			out["archive_size"] = strconv.FormatInt(d.Archive.Size, 10)
		}
		putStr(out, "archive_sha256", d.Archive.Sha256)
		putStr(out, "archive_purl", d.Archive.PURL)
	}

	// [file] — always emit sha256/size/mtime if set; they are the load-bearing
	// fields that `mus check` looks at.
	putStr(out, "sha256", d.File.Sha256)
	if d.File.Size > 0 {
		out["size"] = strconv.FormatInt(d.File.Size, 10)
	}
	putTime(out, "mtime", d.File.Mtime)
	putTime(out, "hashed", d.File.Hashed)
	putStr(out, "host", d.File.Host)
	putStr(out, "abspath", d.File.AbsPath)

	if d.IRODS != nil {
		putStr(out, "irods_url", d.IRODS.URL)
		putStr(out, "irods_purl", d.IRODS.PURL)
		putStr(out, "irods_path", d.IRODS.Path)
		putStr(out, "irods_status", d.IRODS.Status)
		putTime(out, "irods_uploaded_at", d.IRODS.UploadedAt)
		putStr(out, "irods_uploaded_by", d.IRODS.UploadedBy)
		putStr(out, "irods_message", d.IRODS.Message)
	}
	if d.ELN != nil {
		putStr(out, "eln_experiment_id", d.ELN.ExperimentID)
		putStr(out, "eln_experiment_name", d.ELN.ExperimentName)
		putStr(out, "eln_study_id", d.ELN.StudyID)
		putStr(out, "eln_study_name", d.ELN.StudyName)
		putStr(out, "eln_project_id", d.ELN.ProjectID)
		putStr(out, "eln_project_name", d.ELN.ProjectName)
		putStr(out, "eln_journal_id", d.ELN.JournalID)
	}
	if d.S3 != nil {
		putStr(out, "s3_url", d.S3.URL)
		putStr(out, "s3_bucket", d.S3.Bucket)
		putStr(out, "s3_key", d.S3.Key)
		putStr(out, "s3_etag", d.S3.Etag)
		putTime(out, "s3_uploaded_at", d.S3.UploadedAt)
	}
	if d.Bio != nil {
		// Bare keys — same names a project's .env would use.
		putStr(out, "organism", d.Bio.Organism)
		putList(out, "tissue", d.Bio.Tissue)
		putList(out, "cell_type", d.Bio.CellType)
		putStr(out, "technology", d.Bio.Technology)
		putStr(out, "suspension_type", d.Bio.SuspensionType)
		putList(out, "disease", d.Bio.Disease)
		putList(out, "condition", d.Bio.Condition)
		putStr(out, "sex", d.Bio.Sex)
		putStr(out, "age", d.Bio.Age)
		putStr(out, "strain", d.Bio.Strain)
		putStr(out, "title", d.Bio.Title)
		putStr(out, "contributor", d.Bio.Contributor)
		if d.Bio.Year > 0 {
			out["year"] = strconv.Itoa(d.Bio.Year)
		}
		putList(out, "doi", d.Bio.DOI)
		putList(out, "pmid", d.Bio.PMID)
		putList(out, "link", d.Bio.Link)
		putStr(out, "source", d.Bio.Source)
		putStr(out, "license", d.Bio.License)
		putStr(out, "source_description", d.Bio.SourceDescription)
		putStr(out, "access", d.Bio.Access)
		putStr(out, "accession", d.Bio.Accession)
		// Optional ontology-ID companions.
		putStr(out, "organism_ontology_term_id", d.Bio.OrganismOntologyTermID)
		putList(out, "tissue_ontology_term_id", d.Bio.TissueOntologyTermID)
		putList(out, "cell_type_ontology_term_id", d.Bio.CellTypeOntologyTermID)
		putStr(out, "assay_ontology_term_id", d.Bio.AssayOntologyTermID)
		putStr(out, "sex_ontology_term_id", d.Bio.SexOntologyTermID)
		putList(out, "disease_ontology_term_id", d.Bio.DiseaseOntologyTermID)
	}
	if d.H5ad != nil {
		if d.H5ad.NObs > 0 {
			out["h5ad_n_obs"] = strconv.FormatInt(d.H5ad.NObs, 10)
		}
		if d.H5ad.NVars > 0 {
			out["h5ad_n_vars"] = strconv.FormatInt(d.H5ad.NVars, 10)
		}
		putList(out, "h5ad_layers", d.H5ad.Layers)
		putStr(out, "h5ad_embedding", d.H5ad.Embedding)
		putStr(out, "h5ad_anndata_version", d.H5ad.AnnDataVersion)
		putStr(out, "h5ad_gene_id_type", d.H5ad.GeneIDType)
		putTime(out, "h5ad_inspected_at", d.H5ad.InspectedAt)
		putStr(out, "h5ad_inspected_sha256", d.H5ad.InspectedSha256)
		putStr(out, "h5ad_inspected_vocab_sha256", d.H5ad.InspectedVocabSha256)
		if d.H5ad.InspectedCheckVersion > 0 {
			out["h5ad_inspected_check_version"] = strconv.Itoa(d.H5ad.InspectedCheckVersion)
		}
	}
	if d.DE != nil {
		putStr(out, "de_format", d.DE.Format)
		putStr(out, "de_method", d.DE.Method)
		putList(out, "de_term", d.DE.Term)
		putList(out, "de_contrast", d.DE.Contrast)
		putStr(out, "de_cluster", d.DE.Cluster)
		putStr(out, "de_dataset_id", d.DE.DatasetID)
		putStr(out, "de_dataset_h5ad", d.DE.DatasetH5ad)
		putStr(out, "de_cluster_obs_column", d.DE.ClusterObsColumn)
		putStr(out, "de_software_version", d.DE.SoftwareVersion)
		putStr(out, "de_reference_type", d.DE.ReferenceType)
		putList(out, "de_covariates", d.DE.Covariates)
		putStr(out, "de_filter_criteria", d.DE.FilterCriteria)
		putStr(out, "de_description", d.DE.Description)
		if d.DE.RowCount > 0 {
			out["de_row_count"] = strconv.FormatInt(d.DE.RowCount, 10)
		}
		putTime(out, "de_check_passed_at", d.DE.InspectedAt)
		putStr(out, "de_inspected_sha256", d.DE.InspectedSha256)
		if d.DE.InspectedCheckVersion > 0 {
			out["de_check_version"] = strconv.Itoa(d.DE.InspectedCheckVersion)
		}
	}
	return out
}

func docFromMap(in map[string]any) (*Doc, error) {
	d := &Doc{}
	d.Version = parseInt(getStr(in, "version"), 1)
	d.Created = parseTime(getStr(in, "created"))
	d.Updated = parseTime(getStr(in, "updated"))
	d.Kind = getStr(in, "kind")
	d.Note = getStr(in, "note")
	d.DataProject = getStr(in, "data_project")
	if d.Kind == "folder" || hasPrefix(in, "recursive_sha256") || getStr(in, "file_count") != "" {
		d.Folder = &FolderInfo{
			FileCount:       parseInt64(getStr(in, "file_count"), 0),
			DirCount:        parseInt64(getStr(in, "dir_count"), 0),
			TotalBytes:      parseInt64(getStr(in, "total_size"), 0),
			MedianBytes:     parseInt64(getStr(in, "median_size"), 0),
			RecursiveSha256: getStr(in, "recursive_sha256"),
		}
	}
	if hasPrefix(in, "archive_") {
		d.Archive = &ArchiveInfo{
			Format:   getStr(in, "archive_format"),
			Filename: getStr(in, "archive_filename"),
			Size:     parseInt64(getStr(in, "archive_size"), 0),
			Sha256:   getStr(in, "archive_sha256"),
			PURL:     getStr(in, "archive_purl"),
		}
	}
	if v, ok := in["tags"].([]string); ok {
		d.Tags = append([]string(nil), v...)
	}

	d.File = FileInfo{
		Sha256:  getStr(in, "sha256"),
		Size:    parseInt64(getStr(in, "size"), 0),
		Mtime:   parseTime(getStr(in, "mtime")),
		Hashed:  parseTime(getStr(in, "hashed")),
		Host:    getStr(in, "host"),
		AbsPath: getStr(in, "abspath"),
	}

	if hasPrefix(in, "irods_") {
		d.IRODS = &IRODS{
			URL:        getStr(in, "irods_url"),
			PURL:       getStr(in, "irods_purl"),
			Path:       getStr(in, "irods_path"),
			Status:     getStr(in, "irods_status"),
			UploadedAt: parseTime(getStr(in, "irods_uploaded_at")),
			UploadedBy: getStr(in, "irods_uploaded_by"),
			Message:    getStr(in, "irods_message"),
		}
	}
	if hasPrefix(in, "eln_") {
		d.ELN = &ELN{
			ExperimentID:   getStr(in, "eln_experiment_id"),
			ExperimentName: getStr(in, "eln_experiment_name"),
			StudyID:        getStr(in, "eln_study_id"),
			StudyName:      getStr(in, "eln_study_name"),
			ProjectID:      getStr(in, "eln_project_id"),
			ProjectName:    getStr(in, "eln_project_name"),
			JournalID:      getStr(in, "eln_journal_id"),
		}
	}
	if hasPrefix(in, "s3_") {
		d.S3 = &S3{
			URL:        getStr(in, "s3_url"),
			Bucket:     getStr(in, "s3_bucket"),
			Key:        getStr(in, "s3_key"),
			Etag:       getStr(in, "s3_etag"),
			UploadedAt: parseTime(getStr(in, "s3_uploaded_at")),
		}
	}
	if hasBioFields(in) {
		d.Bio = &Bio{
			Organism:               getStr(in, "organism"),
			Tissue:                 getList(in, "tissue"),
			CellType:               getList(in, "cell_type"),
			Technology:             getStr(in, "technology"),
			SuspensionType:         getStr(in, "suspension_type"),
			Disease:                getList(in, "disease"),
			Condition:              getList(in, "condition"),
			Sex:                    getStr(in, "sex"),
			Age:                    getStr(in, "age"),
			Strain:                 getStr(in, "strain"),
			Title:                  getStr(in, "title"),
			Contributor:            getStr(in, "contributor"),
			Year:                   parseInt(getStr(in, "year"), 0),
			DOI:                    getList(in, "doi"),
			PMID:                   getList(in, "pmid"),
			Link:                   getList(in, "link"),
			Source:                 getStr(in, "source"),
			License:                getStr(in, "license"),
			SourceDescription:      getStr(in, "source_description"),
			Access:                 getStr(in, "access"),
			Accession:              getStr(in, "accession"),
			OrganismOntologyTermID: getStr(in, "organism_ontology_term_id"),
			TissueOntologyTermID:   getList(in, "tissue_ontology_term_id"),
			CellTypeOntologyTermID: getList(in, "cell_type_ontology_term_id"),
			AssayOntologyTermID:    getStr(in, "assay_ontology_term_id"),
			SexOntologyTermID:      getStr(in, "sex_ontology_term_id"),
			DiseaseOntologyTermID:  getList(in, "disease_ontology_term_id"),
		}
	}
	if hasPrefix(in, "h5ad_") {
		d.H5ad = &H5ad{
			NObs:            parseInt64(getStr(in, "h5ad_n_obs"), 0),
			NVars:           parseInt64(getStr(in, "h5ad_n_vars"), 0),
			Layers:          getList(in, "h5ad_layers"),
			Embedding:       getStr(in, "h5ad_embedding"),
			AnnDataVersion:        getStr(in, "h5ad_anndata_version"),
			GeneIDType:            getStr(in, "h5ad_gene_id_type"),
			InspectedAt:           parseTime(getStr(in, "h5ad_inspected_at")),
			InspectedSha256:       getStr(in, "h5ad_inspected_sha256"),
			InspectedVocabSha256:  getStr(in, "h5ad_inspected_vocab_sha256"),
			InspectedCheckVersion: parseInt(getStr(in, "h5ad_inspected_check_version"), 0),
		}
	}
	if hasPrefix(in, "de_") {
		d.DE = &DE{
			Format:                getStr(in, "de_format"),
			Method:                getStr(in, "de_method"),
			Term:                  getList(in, "de_term"),
			Contrast:              getList(in, "de_contrast"),
			Cluster:               getStr(in, "de_cluster"),
			DatasetID:              getStr(in, "de_dataset_id"),
			DatasetH5ad:            getStr(in, "de_dataset_h5ad"),
			ClusterObsColumn:      getStr(in, "de_cluster_obs_column"),
			SoftwareVersion:       getStr(in, "de_software_version"),
			ReferenceType:         getStr(in, "de_reference_type"),
			Covariates:            getList(in, "de_covariates"),
			FilterCriteria:        getStr(in, "de_filter_criteria"),
			Description:           getStr(in, "de_description"),
			RowCount:              parseInt64(getStr(in, "de_row_count"), 0),
			InspectedAt:           parseTime(getStr(in, "de_check_passed_at")),
			InspectedSha256:       getStr(in, "de_inspected_sha256"),
			InspectedCheckVersion: parseInt(getStr(in, "de_check_version"), 0),
		}
	}
	return d, nil
}

// --- helpers ----------------------------------------------------------------

func putStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func putList(m map[string]any, k string, v []string) {
	if len(v) > 0 {
		m[k] = append([]string(nil), v...)
	}
}

func getList(m map[string]any, k string) []string {
	if v, ok := m[k].([]string); ok {
		return append([]string(nil), v...)
	}
	return nil
}

func putTime(m map[string]any, k string, t time.Time) {
	if !t.IsZero() {
		m[k] = t.UTC().Format(time.RFC3339)
	}
}

func getStr(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func hasPrefix(m map[string]any, prefix string) bool {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// bioFieldKeys lists the bare-named Bio fields. Used by hasBioFields
// to detect whether to instantiate the Bio block on read. Matches the
// schema-v2 vocabulary (joint vocabulary proposal §3.1, §3.2).
var bioFieldKeys = []string{
	"organism", "tissue", "cell_type", "technology", "suspension_type",
	"disease", "condition", "sex", "age", "strain",
	"title", "contributor", "year",
	"doi", "pmid", "link", "source", "license", "source_description",
	"access", "accession",
	"organism_ontology_term_id", "tissue_ontology_term_id",
	"cell_type_ontology_term_id", "assay_ontology_term_id",
	"sex_ontology_term_id", "disease_ontology_term_id",
}

func hasBioFields(m map[string]any) bool {
	for _, k := range bioFieldKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}
