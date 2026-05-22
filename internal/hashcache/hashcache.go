// Package hashcache stores sha256 checksums of local files, keyed on the
// absolute path. A cached entry is reused only when both size and mtime
// (nanoseconds) match the current file — otherwise the file is rehashed.
//
// Storage is a tiny SQLite database (pure-Go via modernc.org/sqlite) at
// $XDG_DATA_HOME/mus/hashcache.db (or ~/.local/share/mus/hashcache.db). All
// operations on a single *Cache are safe for concurrent use; the database
// itself uses WAL mode to allow multiple readers + one writer.
package hashcache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	_ "modernc.org/sqlite"
)

// Cache is a sha256 cache backed by SQLite.
type Cache struct {
	db *sql.DB

	stmtGetMu  sync.Mutex
	stmtGet    *sql.Stmt
	stmtUpsert *sql.Stmt
}

// Open opens (or creates) the on-disk cache.
func Open(path string) (*Cache, error) {
	if path == "" {
		var err error
		path, err = defaultPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	c := &Cache{db: db}
	if err := c.prepare(); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func defaultPath() (string, error) {
	if p := os.Getenv("MUS_HASHCACHE_DB"); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "mus", "hashcache.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "mus", "hashcache.db"), nil
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS hashcache (
			path     TEXT PRIMARY KEY,
			size     INTEGER NOT NULL,
			mtime_ns INTEGER NOT NULL,
			sha256   TEXT NOT NULL,
			updated  INTEGER NOT NULL
		);
	`)
	return err
}

func (c *Cache) prepare() error {
	var err error
	c.stmtGet, err = c.db.Prepare(
		`SELECT size, mtime_ns, sha256 FROM hashcache WHERE path = ?`)
	if err != nil {
		return err
	}
	c.stmtUpsert, err = c.db.Prepare(`
		INSERT INTO hashcache(path, size, mtime_ns, sha256, updated)
		VALUES(?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(path) DO UPDATE SET
			size=excluded.size,
			mtime_ns=excluded.mtime_ns,
			sha256=excluded.sha256,
			updated=excluded.updated`)
	return err
}

// Close releases the database handle.
func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Entry is a stored checksum.
type Entry struct {
	Path    string
	Size    int64
	MtimeNs int64
	Sha256  string
}

// Get returns the cached sha256 for absPath if the recorded size+mtime match
// `st`. If no usable cache entry exists, ok is false.
func (c *Cache) Get(absPath string, st os.FileInfo) (string, bool, error) {
	c.stmtGetMu.Lock()
	row := c.stmtGet.QueryRow(absPath)
	c.stmtGetMu.Unlock()
	var size, mtime int64
	var sum string
	if err := row.Scan(&size, &mtime, &sum); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if size != st.Size() || mtime != st.ModTime().UnixNano() {
		return "", false, nil
	}
	return sum, true, nil
}

// Put writes a cache entry.
func (c *Cache) Put(absPath string, size, mtimeNs int64, sum string) error {
	_, err := c.stmtUpsert.Exec(absPath, size, mtimeNs, sum)
	return err
}

// Sum returns the sha256 of the file at absPath, using the cache when fresh.
func (c *Cache) Sum(absPath string) (string, error) {
	st, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory", absPath)
	}
	if sum, ok, err := c.Get(absPath, st); err != nil {
		return "", err
	} else if ok {
		return sum, nil
	}
	sum, err := hashFile(absPath)
	if err != nil {
		return "", err
	}
	if err := c.Put(absPath, st.Size(), st.ModTime().UnixNano(), sum); err != nil {
		// non-fatal: returning the sum is still correct
		_ = err
	}
	return sum, nil
}

// SumMany computes sha256 for many files in parallel, returning a map keyed by
// the original input path (not the resolved absolute path). Order of results
// is unspecified.
func (c *Cache) SumMany(paths []string) map[string]struct {
	Sum string
	Err error
} {
	type out struct {
		Sum string
		Err error
	}
	results := make(map[string]struct {
		Sum string
		Err error
	}, len(paths))
	var resultsMu sync.Mutex

	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers == 0 {
		return results
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				abs, err := filepath.Abs(p)
				var o out
				if err != nil {
					o = out{Err: err}
				} else {
					sum, err := c.Sum(abs)
					o = out{Sum: sum, Err: err}
				}
				resultsMu.Lock()
				results[p] = struct {
					Sum string
					Err error
				}(o)
				resultsMu.Unlock()
			}
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return results
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20) // 1 MiB
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
