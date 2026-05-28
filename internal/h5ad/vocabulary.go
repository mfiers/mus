// Package h5ad implements the sanity checks defined in the anthive
// H5AD ingestion spec (doc/H5AD_INGESTION_SPEC.md). It validates both
// the structure of the .h5ad file (via an embedded Python inspector
// that uses anndata) and the sibling .metadata.yaml against a
// controlled vocabulary served from iRODS.
//
// This file: the vocabulary itself — controlled-value lists plus the
// required/soft field partitioning. The canonical source lives on
// iRODS at <irods_home>/project/h5ad_vocabulary.yaml. mus pulls that
// best-effort on each invocation (with a TTL-cached local copy), and
// falls back to a copy embedded at build time when iRODS is
// unreachable.
package h5ad

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/iron"
	"gopkg.in/yaml.v3"
)

// CacheTTL controls how long a downloaded vocabulary is treated as
// fresh before mus tries to re-pull from iRODS. Hard-coded by design —
// see CLAUDE.md "Things deliberately NOT done".
const CacheTTL = 24 * time.Hour

// RemoteVocabPath returns the iRODS path where the controlled
// vocabulary YAML lives. Mirrors stage-2 elnmap's
// <irods_home>/project/eln_mappings.json shape.
func RemoteVocabPath(irodsHome string) string {
	if irodsHome == "" {
		return ""
	}
	return strings.TrimRight(irodsHome, "/") + "/project/h5ad_vocabulary.yaml"
}

// LocalCachePath returns the user-level cache file where mus stores
// the most recently downloaded vocabulary. Same XDG conventions as
// other mus caches.
func LocalCachePath() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "mus", "h5ad_vocabulary.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "mus", "h5ad_vocabulary.yaml")
}

// embeddedYAML is the build-time fallback. It mirrors the spec at
// /data/1/users/mark/anthive4/doc/H5AD_INGESTION_SPEC.md §3 and §4.
// Touch this only when the spec changes and the iRODS YAML hasn't
// caught up yet.
//
//go:embed vocabulary_embedded.yaml
var embeddedYAML []byte

// Vocabulary holds the controlled-value lists plus the
// required/soft field partitioning.
type Vocabulary struct {
	SchemaVersion int                 `yaml:"schema_version"`
	Required      []string            `yaml:"required"`
	Soft          []string            `yaml:"soft"`
	Vocab         map[string][]string `yaml:"vocabulary"`
}

// IsRequired reports whether field is in the required list.
func (v *Vocabulary) IsRequired(field string) bool {
	for _, r := range v.Required {
		if r == field {
			return true
		}
	}
	return false
}

// AllowedValues returns the controlled values for field, or nil if
// the field is not under vocabulary control. The match used in
// Validate is case-insensitive (per spec §3) but underscore-vs-space
// is strict.
func (v *Vocabulary) AllowedValues(field string) []string {
	if v.Vocab == nil {
		return nil
	}
	return v.Vocab[field]
}

// ValueAllowed reports whether `value` matches one of the allowed
// values for `field`. Case-insensitive; underscores in stored values
// are NOT collapsed with spaces — the spec is explicit on this.
// Returns true if the field has no controlled vocabulary.
func (v *Vocabulary) ValueAllowed(field, value string) bool {
	allowed := v.AllowedValues(field)
	if allowed == nil {
		return true
	}
	value = strings.ToLower(strings.TrimSpace(value))
	for _, a := range allowed {
		if strings.ToLower(a) == value {
			return true
		}
	}
	return false
}

// ParseVocabulary unmarshals YAML bytes into a Vocabulary, applying
// minimal sanity checks (schema_version positive, at least one field
// in required).
func ParseVocabulary(raw []byte) (*Vocabulary, error) {
	v := &Vocabulary{}
	if err := yaml.Unmarshal(raw, v); err != nil {
		return nil, fmt.Errorf("parse vocabulary YAML: %w", err)
	}
	if v.SchemaVersion < 1 {
		return nil, fmt.Errorf("vocabulary YAML: schema_version must be ≥ 1 (got %d)", v.SchemaVersion)
	}
	if len(v.Required) == 0 {
		return nil, fmt.Errorf("vocabulary YAML: required list is empty")
	}
	return v, nil
}

// LoadOptions configures Load. Tests use Loader to inject a fake
// iron.Client; production callers leave it nil for the default.
type LoadOptions struct {
	IRODSHome  string         // <irods_home> from the cascade; "" disables iRODS pull
	Iron       *iron.Client   // pre-built iron client; nil = construct via iron.New()
	CachePath  string         // override cache file location (tests)
	Now        func() time.Time
	IgnoreCache bool          // force a re-pull (used by `mus s3 vocab refresh`)
}

func (o *LoadOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// LoadResult records where the returned vocabulary came from.
type LoadResult struct {
	Vocabulary *Vocabulary
	Source     string // "irods" | "cache" | "embedded"
	Path       string // file path for "cache" or "embedded"; remote path for "irods"
	Warnings   []string
}

// Load returns the active Vocabulary, trying in order: live iRODS pull
// (if fresh-enough cache doesn't exist), local cache (if within TTL),
// embedded fallback. Network failures and stale cache fall through
// silently — only embedded-and-corrupted is a hard error.
func Load(ctx context.Context, opts LoadOptions) (*LoadResult, error) {
	res := &LoadResult{}
	cachePath := opts.CachePath
	if cachePath == "" {
		cachePath = LocalCachePath()
	}

	// 1. Cache freshness check (skip if --ignore-cache or if iRODS
	//    is unreachable; iRODS reachability is implicit via the
	//    iron.New() attempt below).
	if !opts.IgnoreCache && cachePath != "" {
		if st, err := os.Stat(cachePath); err == nil {
			age := opts.now().Sub(st.ModTime())
			if age < CacheTTL {
				raw, err := os.ReadFile(cachePath)
				if err == nil {
					vocab, err := ParseVocabulary(raw)
					if err == nil {
						res.Vocabulary = vocab
						res.Source = "cache"
						res.Path = cachePath
						return res, nil
					}
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("cached vocab parse error (using older fallback): %v", err))
				}
			}
		}
	}

	// 2. Try iRODS.
	remotePath := RemoteVocabPath(opts.IRODSHome)
	if remotePath != "" {
		client := opts.Iron
		if client == nil {
			c, err := iron.New()
			if err == nil {
				client = c
			} else {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("iron unavailable: %v", err))
			}
		}
		if client != nil {
			if vocab, warns, err := pullFromIRODS(ctx, client, remotePath, cachePath); err == nil {
				res.Vocabulary = vocab
				res.Source = "irods"
				res.Path = remotePath
				res.Warnings = append(res.Warnings, warns...)
				return res, nil
			} else {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("iRODS pull (%s) failed: %v", remotePath, err))
			}
		}
	}

	// 3. Stale cache (TTL expired but file present) is better than
	//    embedded — we know iRODS just failed but the cache is real
	//    data from a previous run.
	if cachePath != "" {
		if raw, err := os.ReadFile(cachePath); err == nil {
			if vocab, err := ParseVocabulary(raw); err == nil {
				res.Vocabulary = vocab
				res.Source = "cache"
				res.Path = cachePath
				res.Warnings = append(res.Warnings,
					"using stale cache (iRODS unreachable)")
				return res, nil
			}
		}
	}

	// 4. Embedded fallback.
	vocab, err := ParseVocabulary(embeddedYAML)
	if err != nil {
		return nil, fmt.Errorf("embedded vocabulary corrupt: %w", err)
	}
	res.Vocabulary = vocab
	res.Source = "embedded"
	res.Warnings = append(res.Warnings,
		"using embedded fallback (iRODS and cache both unavailable)")
	return res, nil
}

// pullFromIRODS downloads remotePath to a temp file, parses, and on
// success atomically writes to cachePath. Returns the parsed vocab.
func pullFromIRODS(ctx context.Context, client *iron.Client,
	remotePath, cachePath string) (*Vocabulary, []string, error) {

	if _, err := client.Stat(ctx, remotePath); err != nil {
		return nil, nil, fmt.Errorf("stat: %w", err)
	}
	tmp, err := os.CreateTemp("", "h5ad_vocab-*.yaml")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if err := client.Download(ctx, remotePath, tmpName, iron.DownloadOpts{
		Exclusive: false,
	}); err != nil {
		return nil, nil, fmt.Errorf("download: %w", err)
	}
	raw, err := os.ReadFile(tmpName)
	if err != nil {
		return nil, nil, fmt.Errorf("read downloaded: %w", err)
	}
	vocab, err := ParseVocabulary(raw)
	if err != nil {
		return nil, nil, err
	}
	var warns []string
	if cachePath != "" {
		if err := writeCache(cachePath, raw); err != nil {
			warns = append(warns,
				fmt.Sprintf("could not update local cache %s: %v", cachePath, err))
		}
	}
	return vocab, warns, nil
}

// writeCache writes raw to cachePath atomically (mkdir-all + temp-rename).
func writeCache(cachePath string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), ".h5ad_vocab.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, cachePath)
}
