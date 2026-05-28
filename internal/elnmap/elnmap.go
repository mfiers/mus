// Package elnmap stores a tiny cross-folder mapping
//
//	eln_experiment_id  →  (data_project, experiment_name, set_at)
//
// so that when a user runs `mus eln tag <id>` in a *new* folder for an
// experiment they have linked before, mus can propose the same data_project
// rather than asking again from scratch.
//
// Backing store is a single JSON file at $XDG_DATA_HOME/mus/eln_mappings.json
// (or ~/.local/share/mus/eln_mappings.json). Atomic temp+rename on write;
// missing-file is treated as an empty map. Tiny by design — a few hundred
// entries at most — so JSON keeps it human-inspectable / hand-editable.
package elnmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Mapping is one stored entry.
type Mapping struct {
	DataProject    string    `json:"data_project"`
	ExperimentName string    `json:"experiment_name,omitempty"`
	SetAt          time.Time `json:"set_at"`
}

// Store is the file-backed map. Safe for concurrent use within a process via
// the embedded mutex; cross-process races are not handled (typical case is a
// single interactive user).
type Store struct {
	path string

	mu   sync.Mutex
	data map[string]Mapping // keyed by experiment ID as a string
}

// Open opens (or creates) the mapping store. If path is empty, the default
// $XDG_DATA_HOME/mus/eln_mappings.json (or ~/.local/share/mus/...) is used.
func Open(path string) (*Store, error) {
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
	s := &Store{path: path, data: map[string]Mapping{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func defaultPath() (string, error) {
	if p := os.Getenv("MUS_ELN_MAPPINGS"); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "mus", "eln_mappings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "mus", "eln_mappings.json"), nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // empty store
	}
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	return nil
}

func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "eln_mappings.json.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Lookup returns the stored mapping for an experiment ID, or (nil, nil) if
// not present. Pass the ID as a string to avoid int64/long-form parsing
// noise — eln_experiment_id is stored as a string elsewhere in mus too.
func (s *Store) Lookup(experimentID string) (*Mapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data[experimentID]
	if !ok {
		return nil, nil
	}
	cp := m
	return &cp, nil
}

// Remember persists an experiment_id → data_project association.
// Overwrites any prior mapping for the same ID.
func (s *Store) Remember(experimentID, dataProject, experimentName string) error {
	if experimentID == "" {
		return errors.New("empty experiment ID")
	}
	if dataProject == "" {
		return errors.New("empty data_project")
	}
	s.mu.Lock()
	s.data[experimentID] = Mapping{
		DataProject:    dataProject,
		ExperimentName: experimentName,
		SetAt:          time.Now().UTC().Truncate(time.Second),
	}
	s.mu.Unlock()
	return s.save()
}

// Forget removes the mapping for an experiment ID (no error if missing).
func (s *Store) Forget(experimentID string) error {
	s.mu.Lock()
	delete(s.data, experimentID)
	s.mu.Unlock()
	return s.save()
}

// Save persists the in-memory map to the store's backing file. Called
// automatically by Remember / Forget; exposed here so callers can persist
// after a bulk MergeIn (Stage 2 sync from iRODS).
func (s *Store) Save() error { return s.save() }

// All returns a snapshot copy of every mapping. Read-only — mutate by going
// through Remember / Forget.
func (s *Store) All() map[string]Mapping {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Mapping, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// MergeIn applies remote into the in-memory store. Per key:
//
//   - present locally only      → keep local
//   - present remote only       → take remote
//   - present both, remote newer → take remote
//   - present both, local newer → keep local
//
// Returns the number of mappings added/replaced from remote (informational).
// Caller is responsible for persisting via save().
func (s *Store) MergeIn(remote map[string]Mapping) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := 0
	for k, r := range remote {
		local, exists := s.data[k]
		if !exists || r.SetAt.After(local.SetAt) {
			s.data[k] = r
			changed++
		}
	}
	return changed
}

// SaveToFile writes the current in-memory map to an arbitrary path (atomic).
// Used by Stage 2 sync to stage a merged copy before pushing to iRODS.
func (s *Store) SaveToFile(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(path, s.data, 0o600)
}

// ReadFromFile loads mappings from an arbitrary path (e.g. a temp file just
// downloaded from iRODS). Returns the parsed map without touching the
// in-memory state — use MergeIn() to apply it.
func ReadFromFile(path string) (map[string]Mapping, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]Mapping{}, nil
	}
	out := map[string]Mapping{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// writeJSONAtomic encodes data as indented JSON and writes via temp+rename
// so partial writes are never visible.
func writeJSONAtomic(path string, data any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".elnmap.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
