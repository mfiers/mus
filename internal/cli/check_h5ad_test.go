package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/h5ad"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"codeberg.org/mfiers/mus/internal/vocab"
	"github.com/spf13/cobra"
)

// stubCmd returns a cobra.Command whose stderr is a *bytes.Buffer we
// can inspect after running the freshness check. Context is set to
// Background so the vocab loader inside checkH5adFreshness has a
// usable context.
func stubCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	cmd.SetOut(&bytes.Buffer{})
	return cmd, stderr
}

// isolateHome points HOME/XDG at a temp dir so vocab.Load picks up a
// fresh local cache (or falls through to the embedded vocab when
// there's no network — fine for tests).
func isolateHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
}

// embeddedVocabSha returns the sha256 of the bytes vocab.Load would
// return when no network is available — used by the cached-pass
// test so the sidecar's stamped vocab sha matches what
// checkH5adFreshness computes.
func embeddedVocabSha(t *testing.T) string {
	t.Helper()
	// vocab.Embedded() parses the embedded bytes; we need the sha of
	// THE BYTES themselves. Read the same file the package embeds.
	raw, err := os.ReadFile("../vocab/embedded.yaml")
	if err != nil {
		t.Fatalf("read embedded.yaml: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestH5adFreshnessCached(t *testing.T) {
	// All three cache keys match → cached pass, no inspector call.
	isolateHome(t)
	sha := "abc123def456"
	vocabSha := embeddedVocabSha(t)
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: sha},
		H5ad: &sidecar.H5ad{
			InspectedSha256:       sha,
			InspectedVocabSha256:  vocabSha,
			InspectedCheckVersion: h5ad.CheckVersion,
			InspectedAt:           time.Now().UTC().Truncate(time.Second),
		},
	}
	cmd, stderr := stubCmd()
	res := checkH5adFreshness(cmd, nil, "/tmp/fake.h5ad.mus", "/tmp/fake.h5ad", doc)
	if res.status != "ok" {
		t.Errorf("expected ok status, got %q (detail=%q)", res.status, res.detail)
	}
	if !strings.Contains(stderr.String(), "cached pass") {
		t.Errorf("expected 'cached pass' message, got:\n%s", stderr.String())
	}
}

func TestH5adFreshnessNoRecordedInspection(t *testing.T) {
	// No H5ad block → notice, still ok. Bails BEFORE vocab.Load so
	// no isolateHome needed and no vocab sha to compute.
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: "abc123"},
	}
	cmd, stderr := stubCmd()
	res := checkH5adFreshness(cmd, nil, "/tmp/fake.h5ad.mus", "/tmp/fake.h5ad", doc)
	if res.status != "ok" {
		t.Errorf("expected ok (notice only), got %q", res.status)
	}
	if !strings.Contains(stderr.String(), "no inspection recorded") {
		t.Errorf("expected 'no inspection recorded' notice, got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "mus h5ad check") {
		t.Errorf("notice should point at `mus h5ad check`, got:\n%s", stderr.String())
	}
}

func TestH5adFreshnessEmptyInspectedSha(t *testing.T) {
	// H5ad block exists but InspectedSha256 is "" → treated like no
	// recorded inspection. Bails before vocab.Load.
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: "abc123"},
		H5ad: &sidecar.H5ad{NObs: 100}, // present but no InspectedSha256
	}
	cmd, stderr := stubCmd()
	res := checkH5adFreshness(cmd, nil, "/tmp/fake.h5ad.mus", "/tmp/fake.h5ad", doc)
	if res.status != "ok" {
		t.Errorf("expected ok, got %q", res.status)
	}
	if !strings.Contains(stderr.String(), "no inspection recorded") {
		t.Errorf("expected 'no inspection recorded' notice, got:\n%s", stderr.String())
	}
}

func TestIsH5adData(t *testing.T) {
	cases := map[string]bool{
		"/tmp/x.h5ad":        true,
		"x.h5ad":             true,
		"/tmp/x.h5ad.mus":    false, // sidecar, not data
		"/tmp/x.H5AD":        false, // spec demands lowercase
		"/tmp/x.csv":         false,
		"/tmp/h5ad/file.txt": false,
	}
	for path, want := range cases {
		if got := isH5adData(path); got != want {
			t.Errorf("isH5adData(%q) = %v, want %v", path, got, want)
		}
	}
}

// silence unused-imports if a test branch is trimmed.
var _ = vocab.Load
