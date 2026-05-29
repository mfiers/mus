// Package h5ad implements the sanity checks defined in the anthive
// H5AD ingestion spec (doc/H5AD_INGESTION_SPEC.md). It validates both
// the structure of the .h5ad file (via an embedded Python inspector
// that uses anndata) and metadata declared by the user (via the
// `.env` cascade + per-file sidecar) against a controlled vocabulary
// served from this repo on Codeberg.
//
// This file: the vocabulary itself — controlled-value lists plus the
// required/soft field partitioning. The canonical source lives at
// https://codeberg.org/mfiers/mus/raw/branch/main/vocabulary/h5ad.yaml
// and is mirrored locally at vocabulary_embedded.yaml for the
// offline / first-run fallback. The two files are kept byte-identical
// by a sync test.
package h5ad

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CacheTTL controls how long a downloaded vocabulary is treated as
// fresh before mus tries to re-fetch from Codeberg. Hard-coded by
// design — see CLAUDE.md "Things deliberately NOT done".
const CacheTTL = 24 * time.Hour

// DefaultVocabURL is the public HTTPS URL serving the on-main copy of
// vocabulary/h5ad.yaml. Overridable via LoadOptions.URL for tests.
const DefaultVocabURL = "https://codeberg.org/mfiers/mus/raw/branch/main/vocabulary/h5ad.yaml"

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

// embeddedYAML is the build-time fallback. Identical to
// vocabulary/h5ad.yaml at the repo root (sync enforced by test).
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
// the field is not under vocabulary control.
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

// LoadOptions configures Load. Tests override URL and HTTPClient to
// point at an httptest.Server.
type LoadOptions struct {
	URL         string       // "" → DefaultVocabURL
	HTTPClient  *http.Client // nil → default with 30s timeout
	CachePath   string       // "" → LocalCachePath()
	Now         func() time.Time
	IgnoreCache bool // force a re-fetch (skip TTL)
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
	Source     string // "codeberg" | "cache" | "embedded"
	Path       string // file path for "cache" / "embedded"; URL for "codeberg"
	Warnings   []string
}

// Load returns the active Vocabulary, trying in order: live HTTPS
// fetch (if fresh-enough cache doesn't exist), local cache (within
// TTL), embedded fallback. Network failures and stale cache fall
// through silently — only embedded-and-corrupted is a hard error.
func Load(ctx context.Context, opts LoadOptions) (*LoadResult, error) {
	res := &LoadResult{}
	cachePath := opts.CachePath
	if cachePath == "" {
		cachePath = LocalCachePath()
	}
	url := opts.URL
	if url == "" {
		url = DefaultVocabURL
	}

	// 1. Cache freshness check (skip if --ignore-cache).
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
						fmt.Sprintf("cached vocab parse error (falling through): %v", err))
				}
			}
		}
	}

	// 2. Try Codeberg HTTPS.
	if url != "" {
		client := opts.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 30 * time.Second}
		}
		if vocab, warns, err := pullFromURL(ctx, client, url, cachePath); err == nil {
			res.Vocabulary = vocab
			res.Source = "codeberg"
			res.Path = url
			res.Warnings = append(res.Warnings, warns...)
			return res, nil
		} else {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("HTTPS fetch (%s) failed: %v", url, err))
		}
	}

	// 3. Stale cache (TTL expired but file present) beats embedded —
	//    we know the network just failed but the cache has real data
	//    from a previous run.
	if cachePath != "" {
		if raw, err := os.ReadFile(cachePath); err == nil {
			if vocab, err := ParseVocabulary(raw); err == nil {
				res.Vocabulary = vocab
				res.Source = "cache"
				res.Path = cachePath
				res.Warnings = append(res.Warnings,
					"using stale cache (Codeberg unreachable)")
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
		"using embedded fallback (Codeberg unreachable and no cache)")
	return res, nil
}

// pullFromURL does the HTTPS fetch, parses, and on success atomically
// writes to cachePath. Returns the parsed vocabulary.
func pullFromURL(ctx context.Context, client *http.Client,
	url, cachePath string) (*Vocabulary, []string, error) {

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/x-yaml, text/yaml, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, nil, fmt.Errorf("read body: %w", err)
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
