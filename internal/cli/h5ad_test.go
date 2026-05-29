package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

// newTestCmd returns a *cobra.Command with a usable context + IO
// buffers — enough to drive runH5adCheck for the tests that don't
// need to actually run the Python inspector.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

// withH5adFile writes a stub .h5ad file under a temp dir + the
// sidecar described by `doc` (nil = no sidecar). Returns the data
// path. Hashcache lives in dir so we don't pollute the user's home.
func withH5adFile(t *testing.T, contents []byte, doc *sidecar.Doc) (string, *hashcache.Cache) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))

	h5adPath := filepath.Join(dir, "mydata.h5ad")
	if err := os.WriteFile(h5adPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if doc != nil {
		if err := sidecar.Write(sidecar.SidecarPath(h5adPath), doc); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := hashcache.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	return h5adPath, cache
}

func TestCachedH5adPass_ShaMatches(t *testing.T) {
	contents := []byte("stub h5ad bytes")
	// Pre-compute the sha so we can stamp it into the sidecar.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	cache, err := hashcache.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	h5adPath := filepath.Join(dir, "mydata.h5ad")
	if err := os.WriteFile(h5adPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := cache.Sum(h5adPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sidecar.Write(sidecar.SidecarPath(h5adPath), &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: sha},
		H5ad: &sidecar.H5ad{
			InspectedAt:     time.Now().UTC().Truncate(time.Second),
			InspectedSha256: sha,
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	hit, err := cachedH5adPass(cache, h5adPath, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Errorf("expected cache hit when sha matches; stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "cached pass") {
		t.Errorf("expected 'cached pass' message, got: %s", stdout.String())
	}
}

func TestCachedH5adPass_ShaDiffers(t *testing.T) {
	contents := []byte("stub h5ad bytes")
	h5adPath, cache := withH5adFile(t, contents, &sidecar.Doc{
		H5ad: &sidecar.H5ad{
			InspectedAt:     time.Now().UTC().Truncate(time.Second),
			InspectedSha256: "deadbeefcafebabe", // wrong sha
		},
	})
	var stdout bytes.Buffer
	hit, err := cachedH5adPass(cache, h5adPath, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("expected cache miss when sha differs; stdout=%q", stdout.String())
	}
}

func TestCachedH5adPass_NoSidecar(t *testing.T) {
	h5adPath, cache := withH5adFile(t, []byte("stub"), nil)
	var stdout bytes.Buffer
	hit, err := cachedH5adPass(cache, h5adPath, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("expected cache miss with no sidecar; stdout=%q", stdout.String())
	}
}

func TestCachedH5adPass_SidecarWithoutH5adBlock(t *testing.T) {
	h5adPath, cache := withH5adFile(t, []byte("stub"), &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: "abc"},
	})
	var stdout bytes.Buffer
	hit, err := cachedH5adPass(cache, h5adPath, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("expected cache miss with no H5ad block; stdout=%q", stdout.String())
	}
}

func TestCachedH5adPass_EmptyInspectedSha(t *testing.T) {
	h5adPath, cache := withH5adFile(t, []byte("stub"), &sidecar.Doc{
		H5ad: &sidecar.H5ad{NObs: 100}, // H5ad block present but no InspectedSha256
	})
	var stdout bytes.Buffer
	hit, err := cachedH5adPass(cache, h5adPath, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("expected cache miss with empty InspectedSha256; stdout=%q", stdout.String())
	}
}

// TestRunH5adCheck_NonH5adArg pins the §1.4 extension check at the
// CLI entry point — the error should fire BEFORE any expensive
// vocab fetch or anndata spin-up.
func TestRunH5adCheck_NonH5adArg(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(csv, []byte("a,b\n1,2"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runH5adCheck(newTestCmd(), csv, false)
	if err == nil {
		t.Fatal("expected error on non-.h5ad arg, got nil")
	}
	if !strings.Contains(err.Error(), ".h5ad") {
		t.Errorf("error should mention .h5ad, got: %v", err)
	}
}

// TestRunH5adCheck_SidecarArgResolvesToData verifies that passing
// `mydata.h5ad.mus` to `mus h5ad check` resolves to the underlying
// .h5ad data file (and then fails the non-h5ad check because the
// .h5ad doesn't exist). The real PASS path needs Python; this just
// pins the resolution.
func TestRunH5adCheck_SidecarArgResolvesToData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	// Sidecar exists but the .h5ad data file doesn't — runH5adCheck
	// should report the missing .h5ad, not the missing sidecar.
	scPath := filepath.Join(dir, "mydata.h5ad.mus")
	if err := os.WriteFile(scPath, []byte("# empty"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runH5adCheck(newTestCmd(), scPath, false)
	if err == nil {
		t.Fatal("expected error when data file is missing")
	}
	// Must mention the .h5ad path (or stat error), not the .mus path.
	if strings.Contains(err.Error(), ".mus") {
		t.Errorf("error should resolve sidecar arg to data path before stat; got: %v", err)
	}
}
