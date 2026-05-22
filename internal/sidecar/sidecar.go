// Package sidecar reads and writes `*.mus` TOML sidecar files that record a
// single data file's checksum, size, mtime, and remote-storage state.
//
// Naming: for a data file `foo.h5ad`, the sidecar is `foo.h5ad.mus`. The
// sidecar lives next to the data file. A `.mus` file at the root of a folder
// is folder-level config, not a sidecar — sidecars always carry a base name.
package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Suffix is the sidecar filename suffix (note: the leading dot is preserved
// for the data file, so "foo.h5ad" -> "foo.h5ad.mus").
const Suffix = ".mus"

// FileInfo is the [file] section of a sidecar.
// Note: `omitempty` is intentionally NOT used on time.Time fields — go-toml/v2
// treats any time.Time as "empty" under omitempty (issue with reflect.Value
// emptiness check), which drops timestamps from the output. Always-emit is
// fine because we always populate these on Write.
type FileInfo struct {
	Sha256  string    `toml:"sha256,omitempty"`
	Size    int64     `toml:"size,omitempty"`
	Mtime   time.Time `toml:"mtime"`
	Hashed  time.Time `toml:"hashed"`
	Host    string    `toml:"host,omitempty"`
	AbsPath string    `toml:"abspath,omitempty"`
}

// IRODS is the [irods] section.
type IRODS struct {
	URL        string    `toml:"url,omitempty"`
	Path       string    `toml:"path,omitempty"`
	Status     string    `toml:"status,omitempty"` // ok, mismatch, pending, ...
	UploadedAt time.Time `toml:"uploaded_at"`
	UploadedBy string    `toml:"uploaded_by,omitempty"`
}

// ELN is the [eln] section.
type ELN struct {
	ExperimentID   string `toml:"experiment_id,omitempty"`
	ExperimentName string `toml:"experiment_name,omitempty"`
	StudyID        string `toml:"study_id,omitempty"`
	StudyName      string `toml:"study_name,omitempty"`
	ProjectID      string `toml:"project_id,omitempty"`
	ProjectName    string `toml:"project_name,omitempty"`
	JournalID      string `toml:"journal_id,omitempty"`
}

// S3 is the [s3] section (planned).
type S3 struct {
	URL        string    `toml:"url,omitempty"`
	Bucket     string    `toml:"bucket,omitempty"`
	Key        string    `toml:"key,omitempty"`
	Etag       string    `toml:"etag,omitempty"`
	UploadedAt time.Time `toml:"uploaded_at"`
}

// Doc is the full sidecar document.
type Doc struct {
	Version int       `toml:"version"`
	Note    string    `toml:"note,omitempty"`
	Tags    []string  `toml:"tags,omitempty"`
	Created time.Time `toml:"created"`
	Updated time.Time `toml:"updated"`
	File    FileInfo  `toml:"file"`
	IRODS   *IRODS    `toml:"irods,omitempty"`
	ELN     *ELN      `toml:"eln,omitempty"`
	S3      *S3       `toml:"s3,omitempty"`
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
// a non-empty base name (i.e. is not a folder-level `.mus`).
func IsSidecar(path string) bool {
	base := filepath.Base(path)
	if base == FolderConfig {
		return false
	}
	return strings.HasSuffix(base, Suffix) && len(base) > len(Suffix)
}

// FolderConfig is the folder-level config filename (handled by package
// config, not by this package).
const FolderConfig = ".mus"

// Read parses a sidecar file. Returns os.IsNotExist-compatible error if
// missing.
func Read(path string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Doc
	if err := toml.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &d, nil
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

	raw, err := toml.Marshal(d)
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
