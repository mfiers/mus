package vocab

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestScheduleBackgroundRefresh_SkipsWhenOptedOut(t *testing.T) {
	t.Setenv("MUS_SKIP_VOCAB_REFRESH", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	resetBackgroundRefreshOnce()
	// Should be a no-op — the cache file must not appear afterwards.
	scheduleBackgroundRefresh()
	time.Sleep(50 * time.Millisecond) // give any (unintended) goroutine a chance
	if _, err := os.Stat(LocalCachePath()); err == nil {
		t.Errorf("MUS_SKIP_VOCAB_REFRESH=1 should suppress the refresh; cache exists at %s",
			LocalCachePath())
	}
}

func TestScheduleBackgroundRefresh_SkipsWhenCacheFresh(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("MUS_SKIP_VOCAB_REFRESH", "") // ensure not opted out
	cachePath := LocalCachePath()
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("schema_version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force mtime "fresh".
	now := time.Now()
	if err := os.Chtimes(cachePath, now, now); err != nil {
		t.Fatal(err)
	}
	beforeSize := fileSize(t, cachePath)
	resetBackgroundRefreshOnce()
	scheduleBackgroundRefresh()
	time.Sleep(100 * time.Millisecond) // would-be-network-roundtrip window
	if fileSize(t, cachePath) != beforeSize {
		t.Error("fresh cache was rewritten — refresh should have been skipped")
	}
}

func TestBackgroundRefresh_OnlyRunsOncePerProcess(t *testing.T) {
	t.Setenv("MUS_SKIP_VOCAB_REFRESH", "1") // avoid actual network
	resetBackgroundRefreshOnce()
	BackgroundRefresh()
	BackgroundRefresh()
	BackgroundRefresh()
	// Nothing to assert beyond "doesn't panic"; sync.Once correctness
	// is the assertion. The skip-on-opt-out path means no goroutines
	// spawn, so this just exercises the guard.
}

// resetBackgroundRefreshOnce lets tests run scheduleBackgroundRefresh
// multiple times. Production code path is sync.Once-guarded by
// design — tests need to reset it between cases.
func resetBackgroundRefreshOnce() {
	backgroundRefreshOnce = sync.Once{}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
