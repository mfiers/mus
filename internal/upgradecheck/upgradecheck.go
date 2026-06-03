// Package upgradecheck pings Codeberg once per day to see whether a
// newer release of mus is available, and prints a single-line stderr
// warning when one is.
//
// Design constraints:
//
//   - At most one network call per 24 hours, ever — the result is
//     cached on disk and the cache's mtime drives the rate limit.
//   - All errors are silent. Network down, Codeberg down, malformed
//     JSON, unwritable cache dir: every failure path returns and the
//     surrounding mus invocation continues normally. An upgrade
//     check that loudly fails would be worse than no check at all.
//   - Dev builds (Version == "dev") never check — they're maintainer
//     local-builds with no release to compare against.
//   - The Codeberg API used is the public Forgejo REST endpoint
//     (no auth required); the timeout is short (5s) so a hanging
//     network can't perceptibly slow a `mus` invocation.
package upgradecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/atomicfile"
)

// Endpoint is the Codeberg Forgejo "latest release" REST endpoint
// for the mus repo. Overridable from tests.
var Endpoint = "https://codeberg.org/api/v1/repos/mfiers/mus/releases/latest"

// CheckTTL is how long a cached check result is reused before
// refreshing from the network. Exactly one day.
const CheckTTL = 24 * time.Hour

// fetchTimeout caps the network call so a slow Codeberg can't
// perceptibly delay a `mus` invocation. 5s is conservative; the
// API typically responds in 100-500ms.
const fetchTimeout = 5 * time.Second

// cacheRecord is what gets persisted to disk between invocations.
// LatestTag is the upstream tag (e.g. "v0.2.0"); CheckedAt is when
// we last successfully queried Codeberg.
type cacheRecord struct {
	LatestTag string    `json:"latest_tag"`
	CheckedAt time.Time `json:"checked_at"`
}

// Notify prints a one-line warning to stderr if a newer release of
// mus exists on Codeberg. Returns silently on every error path.
//
// currentVersion is the ldflags-injected `cli.Version` constant —
// pass "dev" to disable the check.
func Notify(ctx context.Context, currentVersion string, stderr io.Writer) {
	if currentVersion == "" || currentVersion == "dev" {
		return
	}

	latest, ok := latestTag(ctx)
	if !ok {
		return
	}

	if compareVersions(currentVersion, stripV(latest)) >= 0 {
		return // already at or ahead of upstream
	}
	fmt.Fprintf(stderr,
		"mus: a newer release is available (current=%s, latest=%s) — run `mus upgrade`\n",
		currentVersion, latest)
}

// latestTag returns the latest release tag, using the on-disk cache
// when fresh and refreshing it from Codeberg otherwise. The bool is
// false when no usable result could be obtained (silently, by
// design).
func latestTag(ctx context.Context) (string, bool) {
	cachePath := cacheFile()

	// Cache hit: file exists and was written within TTL.
	if rec, ok := readCache(cachePath); ok {
		if time.Since(rec.CheckedAt) < CheckTTL {
			return rec.LatestTag, true
		}
	}

	// Cache miss or stale: hit Codeberg with a tight timeout.
	tag, ok := fetchLatestTag(ctx)
	if !ok {
		// Keep using the stale cache if any — better than nothing.
		if rec, ok := readCache(cachePath); ok && rec.LatestTag != "" {
			return rec.LatestTag, true
		}
		return "", false
	}
	writeCache(cachePath, cacheRecord{LatestTag: tag, CheckedAt: time.Now().UTC()})
	return tag, true
}

func fetchLatestTag(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", Endpoint, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", false
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	if payload.TagName == "" {
		return "", false
	}
	return payload.TagName, true
}

func cacheFile() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "mus", "upgrade_check.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "mus", "upgrade_check.json")
}

func readCache(path string) (cacheRecord, bool) {
	if path == "" {
		return cacheRecord{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cacheRecord{}, false
	}
	var rec cacheRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return cacheRecord{}, false
	}
	return rec, true
}

func writeCache(path string, rec cacheRecord) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = atomicfile.Replace(path, raw, 0o600)
}

// stripV trims a leading "v" from a release tag, returning the bare
// version string (e.g. "v0.2.0" → "0.2.0").
func stripV(tag string) string {
	if len(tag) > 0 && (tag[0] == 'v' || tag[0] == 'V') {
		return tag[1:]
	}
	return tag
}

// compareVersions returns -1/0/+1 for a < b, a == b, a > b. Splits
// each on `.` and compares dotted components numerically.
//
// mus tags are always plain X.Y.Z; the embedded cli.Version is
// either X.Y.Z (release) or X.Y.Z-dev (working tree between two
// ships). A trailing `-dev` does NOT change ordering for the
// upgrade check — a user running 0.2.1-dev has the 0.2.1 content
// and shouldn't be told 0.2.1 is "available". That falls out of
// the numeric-prefix strip in parseComponent: "1-dev" parses to 1,
// same as "1".
func compareVersions(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	n := len(aParts)
	if len(bParts) < n {
		n = len(bParts)
	}
	for i := 0; i < n; i++ {
		ai, aErr := parseComponent(aParts[i])
		bi, bErr := parseComponent(bParts[i])
		if aErr == nil && bErr == nil {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		// Non-numeric component on one side — compare as strings.
		if aParts[i] != bParts[i] {
			if aParts[i] < bParts[i] {
				return -1
			}
			return 1
		}
	}
	if len(aParts) != len(bParts) {
		if len(aParts) < len(bParts) {
			return -1
		}
		return 1
	}
	return 0
}

// parseComponent parses the leading numeric prefix of a version
// component. "1" → 1, "1-dev" → 1, "abc" → err. The caller falls
// back to string compare on err.
func parseComponent(s string) (int, error) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("no numeric prefix in %q", s)
	}
	return strconv.Atoi(s[:end])
}
