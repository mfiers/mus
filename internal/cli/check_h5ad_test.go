package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

// stubCmd returns a cobra.Command whose stderr is a *bytes.Buffer we
// can inspect after running the freshness check.
func stubCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	cmd.SetOut(&bytes.Buffer{})
	return cmd, stderr
}

func TestH5adFreshnessCached(t *testing.T) {
	// Doc with matching InspectedSha256 → cached pass, no inspector
	// called, no file touched.
	sha := "abc123def456"
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: sha},
		H5ad: &sidecar.H5ad{
			InspectedSha256: sha,
			InspectedAt:     time.Now().UTC().Truncate(time.Second),
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
	// No H5ad block → "no inspection recorded" notice, still ok.
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
	if !strings.Contains(stderr.String(), "mus s3 inspect") {
		t.Errorf("notice should point at `mus s3 inspect`, got:\n%s", stderr.String())
	}
}

func TestH5adFreshnessEmptyInspectedSha(t *testing.T) {
	// H5ad block exists but InspectedSha256 is "" (legacy sidecar) →
	// treated same as no recorded inspection.
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
