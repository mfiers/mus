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

	"codeberg.org/mfiers/mus/internal/envformat"
)

// Suffix is the sidecar filename suffix.
const Suffix = ".mus"

// FolderConfig is the folder-level config filename. Handled by package
// config; named here only so IsSidecar can correctly reject it.
const FolderConfig = ".env"

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

// S3 captures S3 upload state (planned).
type S3 struct {
	URL        string
	Bucket     string
	Key        string
	Etag       string
	UploadedAt time.Time
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
}

// FolderInfo is populated on folder-level sidecars (Kind=="folder").
type FolderInfo struct {
	FileCount       int64
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

// IsSidecar reports whether `path` looks like a sidecar (ends in .mus) AND has
// a non-empty base name (i.e. is not the folder config `.env`).
func IsSidecar(path string) bool {
	base := filepath.Base(path)
	if base == FolderConfig {
		return false
	}
	return strings.HasSuffix(base, Suffix) && len(base) > len(Suffix)
}

// sidecarListKeys lists the sidecar fields that are comma-separated lists.
var sidecarListKeys = map[string]bool{"tags": true}

func parseOpts() envformat.Options {
	return envformat.Options{ListKeys: sidecarListKeys}
}

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

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mus.sidecar.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
	return d, nil
}

// --- helpers ----------------------------------------------------------------

func putStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
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
