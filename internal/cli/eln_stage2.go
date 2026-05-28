package cli

// Stage 2 of the experiment_id → data_project mapping store: sync a single
// shared file at <irods_home>/project/eln_mappings.json across all team
// members so when one user runs `mus eln tag X → Fiers2025` in a folder,
// every other user proposing the same X gets Fiers2025 as the cached
// suggestion.
//
// All Stage 2 operations are BEST-EFFORT — iRODS being unreachable or the
// shared file being absent never blocks the local-only flow. We log a
// one-line warning and continue. The local cache at
// ~/.local/share/mus/eln_mappings.json remains the canonical "what I've
// seen" record on this machine.
//
// Concurrency: optimistic last-write-wins per key. Before pushing we
// re-pull, merge our new keys into the latest remote, then upload. Two
// users adding the SAME experiment_id at the same time → whoever uploads
// second wins for that key; other keys are not affected.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/elnmap"
	"codeberg.org/mfiers/mus/internal/iron"
)

// stageTwoRemotePath computes <irods_home>/project/eln_mappings.json or
// returns "" if no irods_home is reachable from the cascade or defaults.
func stageTwoRemotePath(env *config.Env) string {
	home := env.String("irods_home")
	if home == "" {
		home = defaults.IRODSHome
	}
	if home == "" {
		return ""
	}
	return strings.TrimRight(home, "/") + "/project/eln_mappings.json"
}

// pullStage2 downloads the shared mappings file from iRODS (if it exists),
// merges its entries into the local store, and persists. No-ops if the
// shared file isn't there yet.
//
// Best-effort: warnings to stderr on partial failure but never returns an
// error to the caller — callers should NOT abort their operation on a sync
// hiccup.
func pullStage2(ctx context.Context, client *iron.Client, store *elnmap.Store,
	remotePath string, stderr io.Writer) {
	if client == nil || remotePath == "" {
		return
	}
	// Does the shared file exist yet?
	if _, err := client.Stat(ctx, remotePath); err != nil {
		// Treat any stat failure as "no remote yet" — first user to push
		// will create the file. Verbose users can re-run with -v for
		// detail.
		return
	}
	tmp, err := os.CreateTemp("", "eln_mappings-pull-*.json")
	if err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) could not create temp file: %v\n", err)
		return
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if err := client.Download(ctx, remotePath, tmpName, iron.DownloadOpts{
		Exclusive: false, // we own the temp, overwrite ok
	}); err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) iron download %s: %v\n", remotePath, err)
		return
	}
	remote, err := elnmap.ReadFromFile(tmpName)
	if err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) parse remote: %v\n", err)
		return
	}
	if n := store.MergeIn(remote); n > 0 {
		if err := store.Save(); err != nil {
			fmt.Fprintf(stderr, "  (stage2 sync) save local: %v\n", err)
			return
		}
		fmt.Fprintf(stderr, "  pulled %d mapping(s) from %s\n", n, remotePath)
	}
}

// pushStage2 uploads the local store's contents back to iRODS. Best-effort:
// warnings on failure, never aborts the caller.
//
// Pre-pull is repeated to minimise the window for concurrent writes: we
// fetch the latest remote, merge our local on top, upload merged result.
// "Last-write-wins per key" means: if two users add the same experiment_id
// at roughly the same time, the second uploader's value wins for that key.
// Other keys are preserved.
func pushStage2(ctx context.Context, client *iron.Client, store *elnmap.Store,
	remotePath string, stderr io.Writer) {
	if client == nil || remotePath == "" {
		return
	}
	// Refresh once more — minimise concurrent-write conflicts.
	pullStage2(ctx, client, store, remotePath, stderr)

	tmp, err := os.CreateTemp("", "eln_mappings-push-*.json")
	if err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) could not create temp file: %v\n", err)
		return
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if err := store.SaveToFile(tmpName); err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) stage local file: %v\n", err)
		return
	}
	// Make sure the parent collection exists.
	if err := client.Mkdir(ctx, filepath.Dir(remotePath)); err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) iron mkdir %s: %v\n",
			filepath.Dir(remotePath), err)
		return
	}
	if err := client.Upload(ctx, tmpName, remotePath, iron.UploadOpts{
		VerifyChecksum: true,
	}); err != nil {
		fmt.Fprintf(stderr, "  (stage2 sync) iron upload %s: %v\n", remotePath, err)
		return
	}
	fmt.Fprintf(stderr, "  pushed local mappings to %s\n", remotePath)
}
