// Package localprojects keeps a tiny per-machine log:
//
//	data_project  →  (last_folder, last_used)
//
// so commands like `mus irods projects --details` can show the
// depositor "the most recent local folder you used for project X."
// Useful when the same data_project is spread across multiple work
// directories on a shared HPC home.
//
// Backing store: $XDG_DATA_HOME/mus/local_projects.json (or
// ~/.local/share/mus/local_projects.json). Atomic temp+rename on
// write; missing-file is an empty map. Best-effort — all errors
// are silently swallowed by the Record helper so a write failure
// never disrupts an upload.
package localprojects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"codeberg.org/mfiers/mus/internal/atomicfile"
)

// Entry is one stored project → folder mapping.
type Entry struct {
	Folder   string    `json:"folder"`
	LastUsed time.Time `json:"last_used"`
}

// Store is the file-backed map. Process-local mutex; no inter-
// process locking — racing writes from two mus invocations land
// with last-writer-wins (acceptable for an audit-trail-style log).
type Store struct {
	path string
	mu   sync.Mutex
}

// payload is the on-disk JSON shape.
type payload struct {
	Entries map[string]Entry `json:"entries"`
}

// DefaultPath returns the canonical location: $XDG_DATA_HOME/mus
// or ~/.local/share/mus. Empty string if neither $HOME nor
// $XDG_DATA_HOME is reachable.
func DefaultPath() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "mus", "local_projects.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "mus", "local_projects.json")
}

// Open returns a Store for path. Empty path defaults to
// DefaultPath. The file itself doesn't need to exist; reads return
// an empty map if missing.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return nil, errors.New("could not determine local-projects path (no $HOME / $XDG_DATA_HOME)")
	}
	return &Store{path: path}, nil
}

// Path returns the file the Store reads / writes.
func (s *Store) Path() string { return s.path }

// All returns a copy of every entry. Missing-file ⇒ empty map.
func (s *Store) All() (map[string]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, err
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Entries == nil {
		return map[string]Entry{}, nil
	}
	out := make(map[string]Entry, len(p.Entries))
	for k, v := range p.Entries {
		out[k] = v
	}
	return out, nil
}

// Record updates project → {folder, now}. Folder is normalised to
// an absolute path. Errors are returned to the caller; the
// convenience wrapper RecordBestEffort swallows them.
func (s *Store) Record(project, folder string) error {
	if project == "" {
		return errors.New("project must be non-empty")
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readUnlocked()
	if err != nil {
		return err
	}
	entries[project] = Entry{Folder: abs, LastUsed: time.Now().UTC()}
	return s.writeUnlocked(entries)
}

// RecordBestEffort is the convenience wrapper called from upload
// paths — silently swallows every error so a write failure can
// never disrupt a successful upload. The caller has already done
// the load-bearing work (data, AVU, sidecar); the log is just a
// nicety on top.
func (s *Store) RecordBestEffort(project, folder string) {
	if s == nil {
		return
	}
	_ = s.Record(project, folder)
}

func (s *Store) readUnlocked() (map[string]Entry, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, err
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Entries == nil {
		return map[string]Entry{}, nil
	}
	return p.Entries, nil
}

func (s *Store) writeUnlocked(entries map[string]Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(payload{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicfile.Replace(s.path, body, 0o644)
}
