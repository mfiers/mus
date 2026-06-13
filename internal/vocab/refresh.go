package vocab

// Best-effort background refresh of the local vocabulary cache.
//
// On every `mus` invocation the root cobra PersistentPreRunE calls
// BackgroundRefresh. If the local cache is older than CacheTTL (or
// absent), a goroutine pulls a fresh copy from Codeberg and writes
// it atomically into the cache file. Errors are silent — they just
// mean the next foreground Load() will use the stale-but-readable
// cache (or hit the network synchronously itself).
//
// This is "best-effort" because the goroutine is killed if the
// process exits before it finishes. Long-running mus commands
// (h5ad check, uploads) have plenty of time; short commands like
// `mus version` may not finish the refresh — but the next long
// command will pick up where this one left off.
//
// Set MUS_SKIP_VOCAB_REFRESH=1 to opt out (CI / sandboxes /
// offline environments).

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"
)

// backgroundRefreshOnce guards against multiple BackgroundRefresh
// calls within a single process firing parallel refreshes. cobra
// PersistentPreRunE runs once per invocation but tests may exercise
// the path repeatedly.
var backgroundRefreshOnce sync.Once

// BackgroundRefresh kicks off a fire-and-forget vocabulary refresh
// when the local cache is older than CacheTTL or missing. Returns
// immediately; the goroutine never reports errors. Subsequent calls
// in the same process are no-ops (sync.Once-guarded).
func BackgroundRefresh() {
	backgroundRefreshOnce.Do(scheduleBackgroundRefresh)
}

func scheduleBackgroundRefresh() {
	if os.Getenv("MUS_SKIP_VOCAB_REFRESH") == "1" {
		return
	}
	cachePath := LocalCachePath()
	if cachePath == "" {
		return
	}
	// If the cache exists and is fresh, skip — no point spawning a
	// goroutine when the next Load() is going to hit the cache anyway.
	if st, err := os.Stat(cachePath); err == nil {
		if time.Since(st.ModTime()) < CacheTTL {
			return
		}
	}
	// Cache missing or stale: fetch in the background. Use a
	// detached context so foreground command cancellation doesn't
	// kill the refresh — Go runtime will still terminate the
	// goroutine if main() exits first, which is fine.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := &http.Client{Timeout: 30 * time.Second}
		_, _, _, _ = pullFromURL(ctx, client, DefaultURL, cachePath)
	}()
}
