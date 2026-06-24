// Package anthivecatalog fetches and caches the anthive catalog
// manifest — the cross-check mus de check uses to verify a DE
// deposit's de_dataset_id / de_dataset_h5ad matches a real dataset
// in the lab catalog.
//
// The manifest is a small (~50-200 KB) JSON written by anthive's
// ant-build-all after every successful ingest, published at:
//
//	s3://<bucket>/_catalog/manifest.json
//
// where <bucket> is the same bucket mus s3 upload writes to
// (defaults from the user's S3 config, typically "anthive4a").
//
// Cache layout: $XDG_CACHE_HOME/mus/anthive_catalog.json. Live for
// 5 minutes; older than that triggers a refetch. On refetch
// failure, the stale cache is used + a warning is printed — we
// never block a check on a transient network blip.
//
// Versioning: manifest carries `version: 1`. Unrecognised versions
// are refused with an "upgrade mus" hint (forward-compat).
package anthivecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the manifest format version this package
// understands. Manifests with a different version trigger an
// upgrade hint.
const SchemaVersion = 1

// DefaultManifestKey is the S3 key the manifest lives at, within
// whatever bucket the user's S3 config points to. Anthive's
// ant-build-all writes here.
const DefaultManifestKey = "_catalog/manifest.json"

// DefaultTTL — how long a cached manifest is considered fresh.
// Anthive refreshes after every ingest, but we cap the staleness
// window so a new dataset deposited 10 minutes ago is reliably
// visible to mus.
const DefaultTTL = 5 * time.Minute

// Manifest is the parsed top-level structure.
type Manifest struct {
	Version      int                    `json:"version"`
	UpdatedAt    string                 `json:"updated_at"`
	DataProjects map[string]DataProject `json:"data_projects"`
}

// DataProject is one entry in `data_projects`, keyed by the
// data_project folder name (e.g. "Sun2026").
type DataProject struct {
	GroupID  string             `json:"group_id"`
	Datasets map[string]Dataset `json:"datasets"`
}

// Dataset is one entry in `datasets`, keyed by the 3-char file_id
// (e.g. "AQR").
type Dataset struct {
	Basename string `json:"basename"`
	Title    string `json:"title"`
	Inactive bool   `json:"inactive"`
}

// Fetcher reads the raw manifest bytes. Real callers wrap the mus
// S3 client; tests inject a fake. Returning ([]byte, error) keeps
// the interface trivial — no need to abstract over the SDK's
// PutObjectOutput.
type Fetcher interface {
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
}

// LoadOptions bundles tunables for Load. Defaults cover the common
// case; tests + offline mode override pieces.
type LoadOptions struct {
	Fetcher  Fetcher       // required — usually an *s3client.Client
	Bucket   string        // S3 bucket name, e.g. "anthive4a"
	Key      string        // S3 key, defaults to DefaultManifestKey
	TTL      time.Duration // max age before refetch; 0 = DefaultTTL
	CacheDir string        // override cache dir; "" = $XDG_CACHE_HOME/mus
	Now      func() time.Time
}

// Load returns the manifest, preferring a fresh on-disk cache.
// Refetches when the cache is missing or older than TTL. On
// refetch failure with a stale cache available, uses the stale
// cache and emits a warning via warn (typically cmd.ErrOrStderr).
//
// Returns an error only when there's no path to a usable manifest
// at all — no cache + fetch fails, OR cache + fetch both fail OR
// the bytes don't parse as a recognised version.
func Load(ctx context.Context, opts LoadOptions, warn io.Writer) (*Manifest, error) {
	if opts.Fetcher == nil {
		return nil, errors.New("anthivecatalog.Load: Fetcher is required")
	}
	if opts.Bucket == "" {
		return nil, errors.New("anthivecatalog.Load: Bucket is required")
	}
	if opts.Key == "" {
		opts.Key = DefaultManifestKey
	}
	if opts.TTL == 0 {
		opts.TTL = DefaultTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cachePath, err := resolveCachePath(opts.CacheDir)
	if err != nil {
		return nil, err
	}

	// Try fresh cache first.
	if cached, age, ok := readCache(cachePath); ok && age < opts.TTL {
		return parseManifest(cached)
	}

	// Cache stale or missing — fetch.
	fresh, fetchErr := opts.Fetcher.GetObject(ctx, opts.Bucket, opts.Key)
	if fetchErr == nil {
		// Validate before caching — don't poison the cache with garbage.
		mf, err := parseManifest(fresh)
		if err != nil {
			return nil, fmt.Errorf("fetched manifest is invalid: %w", err)
		}
		// Best-effort cache write. Ignore failure — we have the manifest.
		_ = writeCache(cachePath, fresh)
		return mf, nil
	}

	// Fetch failed. Fall back to stale cache if we have one.
	if cached, age, ok := readCache(cachePath); ok {
		fmt.Fprintf(warn,
			"⚠ anthive catalog fetch failed (%v) — using stale cache (%s old)\n",
			fetchErr, age.Round(time.Second))
		return parseManifest(cached)
	}

	// No cache + no fetch → can't proceed.
	return nil, fmt.Errorf("fetch manifest from s3://%s/%s: %w",
		opts.Bucket, opts.Key, fetchErr)
}

// parseManifest decodes JSON + checks the schema version.
func parseManifest(raw []byte) (*Manifest, error) {
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, fmt.Errorf("parse manifest JSON: %w", err)
	}
	if mf.Version != SchemaVersion {
		return nil, fmt.Errorf(
			"anthive catalog manifest version %d not supported (this mus knows v%d) — "+
				"upgrade mus (`mus upgrade`) or pass --no-catalog-check",
			mf.Version, SchemaVersion)
	}
	return &mf, nil
}

// resolveCachePath returns the on-disk cache file path. Respects
// XDG_CACHE_HOME when set; otherwise ~/.cache/mus/.
func resolveCachePath(override string) (string, error) {
	if override != "" {
		return filepath.Join(override, "anthive_catalog.json"), nil
	}
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "mus", "anthive_catalog.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir for cache: %w", err)
	}
	return filepath.Join(home, ".cache", "mus", "anthive_catalog.json"), nil
}

// readCache returns (bytes, age, true) when the file exists and is
// readable. Returns (_, _, false) when the cache is missing or
// unreadable — never returns an error so callers can treat both
// the same.
func readCache(path string) ([]byte, time.Duration, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false
	}
	return data, time.Since(st.ModTime()), true
}

// writeCache atomically writes manifest bytes to the cache path.
// Creates parent dirs if needed.
func writeCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ResolveResult describes the outcome of looking up a
// (data_project, dataset_id) pair in the manifest.
type ResolveResult struct {
	// Status classifies the lookup outcome. The caller (mus de
	// check) renders different messages per status.
	Status ResolveStatus
	// Match is the dataset entry when Status == ResolveOK or
	// ResolveInactive. Nil otherwise.
	Match *Dataset
	// Hint carries auxiliary info for non-OK statuses: the
	// data_project's group_id, the foreign data_project that
	// contains the id, the top suggested file_ids, etc.
	Hint ResolveHint
}

// ResolveStatus is the classification of a manifest lookup.
type ResolveStatus int

const (
	ResolveOK              ResolveStatus = iota // exact match, dataset active
	ResolveInactive                             // exact match, dataset marked inactive → warn
	ResolveDataProjectNew                       // data_project not in manifest → first deposit?
	ResolveIsGroupID                            // dataset_id IS the data_project's group_id → typo
	ResolveCrossProject                         // dataset_id exists, but in a different data_project
	ResolveUnknown                              // no match → suggest by Levenshtein
)

// ResolveHint carries extra info for the renderer.
type ResolveHint struct {
	GroupID          string   // data_project's group_id (always populated when known)
	ForeignProject   string   // data_project containing the id (ResolveCrossProject only)
	Suggestions      []string // top file_ids by Levenshtein distance (ResolveUnknown)
	AvailableInProj  []string // ALL file_ids in this data_project (ResolveIsGroupID — used to render the "did you mean A/B/C" hint)
}

// ResolveDatasetID classifies (dataProject, datasetID) in the
// manifest and returns the structured outcome.
func (m *Manifest) ResolveDatasetID(dataProject, datasetID string) ResolveResult {
	dp, dpOK := m.DataProjects[dataProject]
	if !dpOK {
		return ResolveResult{Status: ResolveDataProjectNew}
	}
	hint := ResolveHint{GroupID: dp.GroupID}
	// Exact hit?
	if ds, ok := dp.Datasets[datasetID]; ok {
		if ds.Inactive {
			return ResolveResult{Status: ResolveInactive, Match: &ds, Hint: hint}
		}
		return ResolveResult{Status: ResolveOK, Match: &ds, Hint: hint}
	}
	// Is the id actually this project's group_id? Classic Sun2026/S26 mistake.
	if dp.GroupID != "" && datasetID == dp.GroupID {
		hint.AvailableInProj = sortedKeys(dp.Datasets)
		return ResolveResult{Status: ResolveIsGroupID, Hint: hint}
	}
	// Does the id live in a DIFFERENT data_project? Common when
	// the depositor copy-pastes a sidecar from another project.
	for otherName, other := range m.DataProjects {
		if otherName == dataProject {
			continue
		}
		if _, ok := other.Datasets[datasetID]; ok {
			hint.ForeignProject = otherName
			return ResolveResult{Status: ResolveCrossProject, Hint: hint}
		}
	}
	// No match anywhere → suggest by Levenshtein.
	hint.Suggestions = nearestFileIDs(dp.Datasets, datasetID, 3)
	hint.AvailableInProj = sortedKeys(dp.Datasets)
	return ResolveResult{Status: ResolveUnknown, Hint: hint}
}

// ResolveBasename is the de_dataset_h5ad variant. Walks the
// data_project's datasets looking for a matching basename. Returns
// ResolveOK / ResolveInactive / ResolveDataProjectNew / ResolveUnknown —
// IsGroupID and CrossProject don't apply to basenames.
//
// Caller is responsible for the co-ingest carve-out: BEFORE calling
// this, check whether a local sibling .h5ad with the requested
// basename exists next to the DE file. If so, skip the catalog
// lookup entirely (anthive will accept it at ingest time because
// the h5ad and DE arrive together).
func (m *Manifest) ResolveBasename(dataProject, basename string) ResolveResult {
	dp, ok := m.DataProjects[dataProject]
	if !ok {
		return ResolveResult{Status: ResolveDataProjectNew}
	}
	hint := ResolveHint{GroupID: dp.GroupID}
	for _, ds := range dp.Datasets {
		if ds.Basename == basename {
			if ds.Inactive {
				return ResolveResult{Status: ResolveInactive, Match: &ds, Hint: hint}
			}
			return ResolveResult{Status: ResolveOK, Match: &ds, Hint: hint}
		}
	}
	// Suggest by basename Levenshtein.
	hint.Suggestions = nearestBasenames(dp.Datasets, basename, 3)
	return ResolveResult{Status: ResolveUnknown, Hint: hint}
}

// sortedKeys returns a dataset map's keys in alphabetical order.
// Used for stable "available file_ids: X, Y, Z" rendering.
func sortedKeys(m map[string]Dataset) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scoredEntry is a (key, distance) pair used to rank suggestions.
type scoredEntry struct {
	key string
	d   int
}

// rankByDistance sorts in place by (distance asc, key asc) and
// returns the top-n keys. Stable on ties — alphabetical order.
func rankByDistance(scored []scoredEntry, n int) []string {
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].d != scored[j].d {
			return scored[i].d < scored[j].d
		}
		return scored[i].key < scored[j].key
	})
	if n > len(scored) {
		n = len(scored)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = scored[i].key
	}
	return out
}

// nearestFileIDs returns up to `n` file_ids most similar to `query`
// by Levenshtein distance.
func nearestFileIDs(datasets map[string]Dataset, query string, n int) []string {
	q := strings.ToLower(query)
	scored := make([]scoredEntry, 0, len(datasets))
	for k := range datasets {
		scored = append(scored, scoredEntry{k, levenshtein(q, strings.ToLower(k))})
	}
	return rankByDistance(scored, n)
}

// nearestBasenames — same as nearestFileIDs but on the basename field.
func nearestBasenames(datasets map[string]Dataset, query string, n int) []string {
	q := strings.ToLower(query)
	scored := make([]scoredEntry, 0, len(datasets))
	for _, ds := range datasets {
		scored = append(scored, scoredEntry{ds.Basename, levenshtein(q, strings.ToLower(ds.Basename))})
	}
	return rankByDistance(scored, n)
}

// levenshtein computes edit distance between a and b. Standard
// 2-row dynamic programming, allocates two int slices per call —
// fast enough for the small lists we feed it (typically <50
// datasets per data_project).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(
				prev[j]+1,        // deletion
				curr[j-1]+1,      // insertion
				prev[j-1]+cost,   // substitution
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func minInt(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
