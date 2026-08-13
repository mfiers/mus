package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withShortStallTimeouts shrinks the download watchdog timeouts for the
// duration of a test and restores them after.
func withShortStallTimeouts(t *testing.T, stall, overall time.Duration) {
	t.Helper()
	origStall, origOverall := upgradeStallTimeout, upgradeOverallTimeout
	upgradeStallTimeout, upgradeOverallTimeout = stall, overall
	t.Cleanup(func() {
		upgradeStallTimeout, upgradeOverallTimeout = origStall, origOverall
	})
}

// TestUpgradeDownload_SlowButSteadySucceeds proves the fix: a download
// that keeps making progress must NOT be killed, even when its total
// duration exceeds the stall window. The server writes a chunk every
// ~20ms with a 60ms stall timeout — each chunk resets the watchdog, so
// the whole (slow) transfer completes.
func TestUpgradeDownload_SlowButSteadySucceeds(t *testing.T) {
	withShortStallTimeouts(t, 60*time.Millisecond, 10*time.Second)

	const chunks = 40 // ~800ms total — far longer than the 60ms stall window
	payload := strings.Repeat("mus-binary-bytes", 128) // >1KB per chunk
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			fmt.Fprint(w, payload)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(dest, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upgradeDownload(srv.URL, dest, false); err != nil {
		t.Fatalf("steady slow download should succeed, got: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(chunks * len(payload)); info.Size() != want {
		t.Errorf("downloaded %d bytes, want %d", info.Size(), want)
	}
}

// TestUpgradeDownload_StallFails proves the watchdog fires: the server
// sends a bit of data, then goes silent past the stall window. The
// download must abort with the actionable stall message, not hang.
func TestUpgradeDownload_StallFails(t *testing.T) {
	withShortStallTimeouts(t, 80*time.Millisecond, 10*time.Second)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, strings.Repeat("x", 2048))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Now stall well past the stall timeout.
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(dest, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := upgradeDownload(srv.URL, dest, false)
	if err == nil {
		t.Fatal("stalled download should fail, got nil")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("expected a 'stalled' error, got: %v", err)
	}
}
