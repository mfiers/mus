// Package vocab loads and validates the lab's controlled vocabulary.
//
// One canonical file at vocabulary/main.yaml in this repo defines
// the controlled value lists (organism, celltype, technology, …)
// and per-format required/soft field profiles. mus fetches the
// `main` version over HTTPS at runtime (with a 24h-cached local
// copy at ~/.local/share/mus/h5ad_vocabulary.yaml) and falls back
// to an embedded build-time mirror when the network is unreachable.
//
// The vocabulary is data-format-agnostic: h5ad is the first
// consumer, but any future mus tool validating annotations reads
// from the same single source.
package vocab

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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

// DefaultURL is the public HTTPS URL serving the on-main copy of
// vocabulary/main.yaml. Overridable via LoadOptions.URL for tests.
const DefaultURL = "https://codeberg.org/mfiers/mus/raw/branch/main/vocabulary/main.yaml"

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
// vocabulary/main.yaml at the repo root (sync enforced by test).
//
//go:embed embedded.yaml
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
// values for `field`. Case-insensitive, AND `_` ≡ ` ` ≡ `-` (so
// `prefrontal_cortex`, `prefrontal cortex`, and `Prefrontal-Cortex`
// all match a single canonical entry). Returns true if the field
// has no controlled vocabulary.
//
// Rationale: existing depositors used either form; locking to one
// is friction without payoff. The ontology-ID companion (when set)
// is the strict-match backstop.
func (v *Vocabulary) ValueAllowed(field, value string) bool {
	_, ok := v.Canonical(field, value)
	return ok
}

// Canonical returns the canonical (as-listed) form of `value` for
// `field` if a match exists modulo the case / `_`-`-`-space rules.
// The bool is false when:
//   - the field has no controlled vocabulary (any value is allowed)
//     — in which case the verbatim `value` is returned along with
//     true to signal "no normalisation needed"
//   - the field IS controlled but `value` doesn't match any entry
//     — empty string + false
//
// Lets callers auto-normalise depositor input ("HuMaN" → "human")
// before writing it to disk, instead of carrying a stylistic mess
// across sidecars.
func (v *Vocabulary) Canonical(field, value string) (string, bool) {
	allowed := v.AllowedValues(field)
	if allowed == nil {
		// Freeform field — no canonical form to enforce.
		return value, true
	}
	normalized := normalizeForMatch(value)
	for _, a := range allowed {
		if normalizeForMatch(a) == normalized {
			return a, true
		}
	}
	return "", false
}

// IsControlled reports whether `field` has a controlled vocabulary
// — i.e. whether Canonical / ValueAllowed will reject unknown values
// for it.
func (v *Vocabulary) IsControlled(field string) bool {
	return v.AllowedValues(field) != nil
}

// normalizeForMatch lower-cases s, trims surrounding whitespace, and
// collapses `_`, `-`, and runs of internal whitespace into a single
// space. Two values normalise to the same canonical form iff
// ValueAllowed considers them equivalent.
func normalizeForMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// First replace _ and - with a single space, then collapse
	// runs of whitespace.
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		switch r {
		case '_', '-':
			r = ' '
		}
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	// Second trim: a leading/trailing `_` or `-` became a single space
	// in the loop above. Without this second trim, "_microglia_"
	// normalises to " microglia " — which won't match "microglia"
	// already in the vocabulary.
	return strings.TrimSpace(b.String())
}

// Embedded returns the build-time vocabulary directly, without any
// network/cache work. Handy for tests and `mus vocab --embedded`.
func Embedded() (*Vocabulary, error) {
	return Parse(embeddedYAML)
}

// Parse unmarshals YAML bytes into a Vocabulary, applying minimal
// sanity checks (schema_version positive, at least one field in
// required).
func Parse(raw []byte) (*Vocabulary, error) {
	v := &Vocabulary{}
	if err := yaml.Unmarshal(raw, v); err != nil {
		return nil, fmt.Errorf("parse vocabulary YAML: %w", err)
	}
	if v.SchemaVersion < 1 {
		return nil, fmt.Errorf("vocabulary YAML: schema_version must be ≥ 1 (got %d)", v.SchemaVersion)
	}
	if v.SchemaVersion > 2 {
		return nil, fmt.Errorf("vocabulary YAML: schema_version %d not supported by this mus binary (max: 2)", v.SchemaVersion)
	}
	if len(v.Required) == 0 {
		return nil, fmt.Errorf("vocabulary YAML: required list is empty")
	}
	return v, nil
}

// LoadOptions configures Load. Tests override URL and HTTPClient to
// point at an httptest.Server.
type LoadOptions struct {
	URL         string       // "" → DefaultURL
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
	Vocabulary    *Vocabulary
	Source        string // "codeberg" | "cache" | "embedded"
	Path          string // file path for "cache" / "embedded"; URL for "codeberg"
	ContentSha256 string // hex sha256 of the raw YAML bytes — a vocab-version fingerprint
	Warnings      []string
}

// contentSha256 returns the hex-encoded sha256 of raw.
func contentSha256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
		url = DefaultURL
	}

	// 1. Cache freshness check (skip if --ignore-cache).
	if !opts.IgnoreCache && cachePath != "" {
		if st, err := os.Stat(cachePath); err == nil {
			age := opts.now().Sub(st.ModTime())
			if age < CacheTTL {
				raw, err := os.ReadFile(cachePath)
				if err == nil {
					vocab, err := Parse(raw)
					if err == nil {
						res.Vocabulary = vocab
						res.Source = "cache"
						res.Path = cachePath
						res.ContentSha256 = contentSha256(raw)
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
		if vocab, raw, warns, err := pullFromURL(ctx, client, url, cachePath); err == nil {
			res.Vocabulary = vocab
			res.Source = "codeberg"
			res.Path = url
			res.ContentSha256 = contentSha256(raw)
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
			if vocab, err := Parse(raw); err == nil {
				res.Vocabulary = vocab
				res.Source = "cache"
				res.Path = cachePath
				res.ContentSha256 = contentSha256(raw)
				res.Warnings = append(res.Warnings,
					"using stale cache (Codeberg unreachable)")
				return res, nil
			}
		}
	}

	// 4. Embedded fallback.
	vocab, err := Parse(embeddedYAML)
	if err != nil {
		return nil, fmt.Errorf("embedded vocabulary corrupt: %w", err)
	}
	res.Vocabulary = vocab
	res.Source = "embedded"
	res.ContentSha256 = contentSha256(embeddedYAML)
	res.Warnings = append(res.Warnings,
		"using embedded fallback (Codeberg unreachable and no cache)")
	return res, nil
}

// pullFromURL does the HTTPS fetch, parses, and on success atomically
// writes to cachePath. Returns the parsed vocabulary AND the raw
// bytes (so callers can compute the content fingerprint).
func pullFromURL(ctx context.Context, client *http.Client,
	url, cachePath string) (*Vocabulary, []byte, []string, error) {

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/x-yaml, text/yaml, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, nil, nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read body: %w", err)
	}
	vocab, err := Parse(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	var warns []string
	if cachePath != "" {
		if err := writeCache(cachePath, raw); err != nil {
			warns = append(warns,
				fmt.Sprintf("could not update local cache %s: %v", cachePath, err))
		}
	}
	return vocab, raw, warns, nil
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
