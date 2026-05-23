// Package irodscache stores `iron tree --json` snapshots locally so a user
// can browse the full contents of an iRODS collection (with checksums) over
// and over without re-querying the server every time.
//
// One SQLite database, two tables:
//   - scans (root_path PRIMARY KEY, scanned_at, object_count, bytes_total)
//   - objects (root_path, path PRIMARY KEY together, is_object, size, checksum,
//     modified, creator)
//
// A scan is replace-only: re-scanning a path drops the previous row set in a
// single transaction.
package irodscache

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Object mirrors one record from `iron tree --json`. is_object=true means
// data object (has checksum); false means collection.
type Object struct {
	Path     string
	IsObject bool
	Size     int64
	Checksum string
	Modified time.Time
	Creator  string
}

// ScanInfo summarises one stored scan.
type ScanInfo struct {
	RootPath    string
	ScannedAt   time.Time
	ObjectCount int
	BytesTotal  int64
}

// Cache is a SQLite-backed scan store.
type Cache struct {
	db *sql.DB
}

// Open opens (or creates) the scan database. If path is empty, the default
// $XDG_DATA_HOME/mus/irods-scans.db (or ~/.local/share/mus/irods-scans.db)
// is used.
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
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}

func defaultPath() (string, error) {
	if p := os.Getenv("MUS_IRODS_SCAN_DB"); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "mus", "irods-scans.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "mus", "irods-scans.db"), nil
}

// Close releases the database handle.
func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func initSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS scans (
			root_path     TEXT PRIMARY KEY,
			scanned_at    INTEGER NOT NULL,
			object_count  INTEGER NOT NULL,
			bytes_total   INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS objects (
			root_path  TEXT NOT NULL,
			path       TEXT NOT NULL,
			is_object  INTEGER NOT NULL,
			size       INTEGER,
			checksum   TEXT,
			modified   TEXT,
			creator    TEXT,
			PRIMARY KEY (root_path, path),
			FOREIGN KEY (root_path) REFERENCES scans(root_path) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_objects_checksum ON objects(checksum)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Replace atomically replaces the stored scan for rootPath with the given
// objects. Returns a ScanInfo summarising the result.
func (c *Cache) Replace(rootPath string, objects []Object) (ScanInfo, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return ScanInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM scans WHERE root_path = ?", rootPath); err != nil {
		return ScanInfo{}, err
	}

	objectCount := 0
	var bytesTotal int64
	for _, obj := range objects {
		if obj.IsObject {
			objectCount++
			bytesTotal += obj.Size
		}
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(
		"INSERT INTO scans(root_path, scanned_at, object_count, bytes_total) VALUES (?, ?, ?, ?)",
		rootPath, now, objectCount, bytesTotal); err != nil {
		return ScanInfo{}, err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO objects(root_path, path, is_object, size, checksum, modified, creator)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return ScanInfo{}, err
	}
	defer stmt.Close()

	for _, obj := range objects {
		var modified any
		if !obj.Modified.IsZero() {
			modified = obj.Modified.UTC().Format(time.RFC3339)
		}
		isObj := 0
		if obj.IsObject {
			isObj = 1
		}
		var checksum any
		if obj.Checksum != "" {
			checksum = obj.Checksum
		}
		if _, err := stmt.Exec(rootPath, obj.Path, isObj, obj.Size, checksum, modified, obj.Creator); err != nil {
			return ScanInfo{}, fmt.Errorf("insert %s: %w", obj.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ScanInfo{}, err
	}
	return ScanInfo{
		RootPath:    rootPath,
		ScannedAt:   time.Unix(now, 0),
		ObjectCount: objectCount,
		BytesTotal:  bytesTotal,
	}, nil
}

// GetScan returns metadata for the given scan root. Returns nil if no scan
// exists; an error only on I/O failure.
func (c *Cache) GetScan(rootPath string) (*ScanInfo, error) {
	row := c.db.QueryRow(
		"SELECT root_path, scanned_at, object_count, bytes_total FROM scans WHERE root_path = ?",
		rootPath)
	var info ScanInfo
	var scannedAt int64
	err := row.Scan(&info.RootPath, &scannedAt, &info.ObjectCount, &info.BytesTotal)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info.ScannedAt = time.Unix(scannedAt, 0)
	return &info, nil
}

// ListScans returns metadata for all stored scans, most-recent first.
func (c *Cache) ListScans() ([]ScanInfo, error) {
	rows, err := c.db.Query(
		"SELECT root_path, scanned_at, object_count, bytes_total FROM scans ORDER BY scanned_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanInfo
	for rows.Next() {
		var info ScanInfo
		var scannedAt int64
		if err := rows.Scan(&info.RootPath, &scannedAt, &info.ObjectCount, &info.BytesTotal); err != nil {
			return nil, err
		}
		info.ScannedAt = time.Unix(scannedAt, 0)
		out = append(out, info)
	}
	return out, rows.Err()
}

// ListObjectsOpts filter what ListObjects returns.
type ListObjectsOpts struct {
	OnlyDataObjects bool
	OnlyCollections bool
	PathPrefix      string // only entries whose path starts with this
}

// ListObjects returns every object in a scan, optionally filtered.
func (c *Cache) ListObjects(rootPath string, opts ListObjectsOpts) ([]Object, error) {
	query := "SELECT path, is_object, size, COALESCE(checksum, ''), COALESCE(modified, ''), COALESCE(creator, '') FROM objects WHERE root_path = ?"
	args := []any{rootPath}
	if opts.OnlyDataObjects {
		query += " AND is_object = 1"
	}
	if opts.OnlyCollections {
		query += " AND is_object = 0"
	}
	if opts.PathPrefix != "" {
		query += " AND path LIKE ?"
		args = append(args, opts.PathPrefix+"%")
	}
	query += " ORDER BY path"
	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObjects(rows)
}

// FindByChecksum returns every object across all scans whose server-side
// checksum matches. Useful for "is this file already on iRODS?" / duplicate
// hunting.
func (c *Cache) FindByChecksum(checksum string) ([]Object, error) {
	rows, err := c.db.Query(
		`SELECT path, is_object, size, COALESCE(checksum, ''), COALESCE(modified, ''), COALESCE(creator, '')
		 FROM objects WHERE checksum = ? ORDER BY path`,
		checksum)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObjects(rows)
}

// Delete drops a stored scan and all its objects.
func (c *Cache) Delete(rootPath string) error {
	res, err := c.db.Exec("DELETE FROM scans WHERE root_path = ?", rootPath)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("no such scan")
	}
	return nil
}

func scanObjects(rows *sql.Rows) ([]Object, error) {
	var out []Object
	for rows.Next() {
		var obj Object
		var isObj int
		var modStr string
		if err := rows.Scan(&obj.Path, &isObj, &obj.Size, &obj.Checksum, &modStr, &obj.Creator); err != nil {
			return nil, err
		}
		obj.IsObject = isObj == 1
		if modStr != "" {
			if t, err := time.Parse(time.RFC3339, modStr); err == nil {
				obj.Modified = t
			}
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}
