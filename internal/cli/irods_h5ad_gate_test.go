package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/h5ad"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// embeddedVocabShaForGate reads internal/vocab/embedded.yaml and
// returns its hex sha. Used as the "current vocab sha" in tests so
// the sidecar's stamped value matches what the gate computes.
func embeddedVocabShaForGate(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../vocab/embedded.yaml")
	if err != nil {
		t.Fatalf("read embedded.yaml: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// gateTestSetup writes a stub .h5ad file under an isolated HOME +
// hashcache. Optionally writes a sidecar with the given fields.
// Returns the h5ad path and the cache so the gate can run.
func gateTestSetup(t *testing.T, sidecarFields *sidecar.H5ad) (string, *hashcache.Cache) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))

	h5adPath := filepath.Join(dir, "mydata.h5ad")
	if err := os.WriteFile(h5adPath, []byte("stub h5ad bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := hashcache.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })

	if sidecarFields != nil {
		sum, err := cache.Sum(h5adPath)
		if err != nil {
			t.Fatal(err)
		}
		doc := &sidecar.Doc{
			File: sidecar.FileInfo{Sha256: sum},
			H5ad: sidecarFields,
		}
		// If the test wants the sha to MATCH, accept the current sum.
		// Otherwise it set InspectedSha256 explicitly to something
		// else and we leave it.
		if sidecarFields.InspectedSha256 == "" {
			doc.H5ad.InspectedSha256 = sum
		}
		if err := sidecar.Write(sidecar.SidecarPath(h5adPath), doc); err != nil {
			t.Fatal(err)
		}
	}
	return h5adPath, cache
}

func TestGateH5adForUpload_PassesWhenAllKeysMatch(t *testing.T) {
	vocabSha := embeddedVocabShaForGate(t)
	h5adPath, cache := gateTestSetup(t, &sidecar.H5ad{
		InspectedAt:           time.Now().UTC(),
		InspectedVocabSha256:  vocabSha,
		InspectedCheckVersion: h5ad.CheckVersion,
	})
	failures, err := gateH5adForUpload(context.Background(), cache, []string{h5adPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures; got %+v", failures)
	}
}

func TestGateH5adForUpload_RejectsUnchecked(t *testing.T) {
	// .h5ad with NO sidecar at all.
	h5adPath, cache := gateTestSetup(t, nil)
	failures, err := gateH5adForUpload(context.Background(), cache, []string{h5adPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure; got %d (%+v)", len(failures), failures)
	}
	if !strings.Contains(failures[0].Reason, "no h5ad inspection recorded") {
		t.Errorf("wrong reason: %q", failures[0].Reason)
	}
}

func TestGateH5adForUpload_RejectsStaleVocab(t *testing.T) {
	h5adPath, cache := gateTestSetup(t, &sidecar.H5ad{
		InspectedAt:           time.Now().UTC(),
		InspectedVocabSha256:  "0000000000000000000000000000000000000000000000000000000000000000",
		InspectedCheckVersion: h5ad.CheckVersion,
	})
	failures, err := gateH5adForUpload(context.Background(), cache, []string{h5adPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Reason, "vocabulary changed") {
		t.Errorf("expected vocabulary-changed failure; got %+v", failures)
	}
}

func TestGateH5adForUpload_RejectsStaleCheckVersion(t *testing.T) {
	vocabSha := embeddedVocabShaForGate(t)
	h5adPath, cache := gateTestSetup(t, &sidecar.H5ad{
		InspectedAt:           time.Now().UTC(),
		InspectedVocabSha256:  vocabSha,
		InspectedCheckVersion: 0, // older than runtime
	})
	failures, err := gateH5adForUpload(context.Background(), cache, []string{h5adPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Reason, "h5ad-check code version") {
		t.Errorf("expected check-version failure; got %+v", failures)
	}
}

func TestGateH5adForUpload_ForceBypassesEverything(t *testing.T) {
	// .h5ad with no sidecar — would normally fail. force=true skips
	// the check entirely.
	h5adPath, cache := gateTestSetup(t, nil)
	failures, err := gateH5adForUpload(context.Background(), cache, []string{h5adPath}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Errorf("expected 0 failures with force=true; got %+v", failures)
	}
}

func TestGateH5adForUpload_IgnoresNonH5adArgs(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(csv, []byte("a,b\n1,2"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := hashcache.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	failures, err := gateH5adForUpload(context.Background(), cache, []string{csv}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Errorf("non-.h5ad args should not trigger the gate; got %+v", failures)
	}
}

func TestGateH5adForUpload_FolderArgWalksForH5ad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))

	subFolder := filepath.Join(dir, "project")
	if err := os.MkdirAll(subFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	h5adPath := filepath.Join(subFolder, "deep.h5ad")
	if err := os.WriteFile(h5adPath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	// CSV and txt sit alongside; gate should ignore them.
	if err := os.WriteFile(filepath.Join(subFolder, "data.csv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := hashcache.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	failures, err := gateH5adForUpload(context.Background(), cache, []string{subFolder}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].Path != h5adPath {
		t.Errorf("expected gate to find deep.h5ad inside folder; got %+v", failures)
	}
}

func TestFormatH5adGateError_ListsEveryFailure(t *testing.T) {
	err := formatH5adGateError([]h5adGateFailure{
		{Path: "/a/x.h5ad", Reason: "no h5ad inspection recorded"},
		{Path: "/a/y.h5ad", Reason: "file sha changed since inspection on 2026-05-29"},
	})
	msg := err.Error()
	for _, want := range []string{
		"/a/x.h5ad", "no h5ad inspection recorded",
		"/a/y.h5ad", "file sha changed",
		"mus h5ad check", "--force-h5ad-upload",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in error message:\n%s", want, msg)
		}
	}
}
