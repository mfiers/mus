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

	"codeberg.org/mfiers/mus/internal/atomicfile"
)

// Mapping is one stored entry.
type Mapping struct {
	DataProject    string    `json:"data_project"`
	ExperimentName string    `json:"experiment_name,omitempty"`
	// StudyID / ProjectID record the experiment's ELN study and project
	// so a data_project can be proposed for a *different* experiment that
	// shares the same study or project (see LookupTiered). Both omitempty
	// for backward compatibility — entries written before this field
	// existed simply lack them and never match the study/project tiers.
	StudyID   string    `json:"study_id,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	SetAt     time.Time `json:"set_at"`
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
	return atomicfile.Replace(s.path, raw, 0o600)
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

// TieredMatch is the result of LookupTiered: the proposed data_project
// plus which tier it came from ("experiment", "study", or "project").
type TieredMatch struct {
	DataProject string
	Tier        string // "experiment" | "study" | "project"
}

// LookupTiered proposes a data_project for an experiment by checking, in
// order: (1) an exact mapping for experimentID; (2) any mapping whose
// StudyID equals studyID; (3) any mapping whose ProjectID equals
// projectID. Returns (nil, nil) when nothing matches. studyID/projectID
// may be empty to skip that tier. When several entries match a tier, the
// most recently set one wins (deterministic and "freshest intent").
func (s *Store) LookupTiered(experimentID, studyID, projectID string) (*TieredMatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m, ok := s.data[experimentID]; ok && m.DataProject != "" {
		return &TieredMatch{DataProject: m.DataProject, Tier: "experiment"}, nil
	}

	// newestMatch returns the freshest mapping satisfying pred.
	newestMatch := func(pred func(Mapping) bool) *Mapping {
		var best *Mapping
		for key := range s.data {
			m := s.data[key]
			if m.DataProject == "" || !pred(m) {
				continue
			}
			if best == nil || m.SetAt.After(best.SetAt) {
				cp := m
				best = &cp
			}
		}
		return best
	}

	if studyID != "" {
		if m := newestMatch(func(m Mapping) bool { return m.StudyID == studyID }); m != nil {
			return &TieredMatch{DataProject: m.DataProject, Tier: "study"}, nil
		}
	}
	if projectID != "" {
		if m := newestMatch(func(m Mapping) bool { return m.ProjectID == projectID }); m != nil {
			return &TieredMatch{DataProject: m.DataProject, Tier: "project"}, nil
		}
	}
	return nil, nil
}

// Remember persists an experiment_id → data_project association.
// Overwrites any prior mapping for the same ID. Study/project are left
// empty; use RememberFull to record them and enable the study/project
// suggestion tiers.
func (s *Store) Remember(experimentID, dataProject, experimentName string) error {
	return s.RememberFull(experimentID, dataProject, experimentName, "", "")
}

// RememberFull is Remember plus the experiment's ELN study and project
// IDs, so LookupTiered can propose this data_project for sibling
// experiments in the same study or project. Empty studyID/projectID are
// stored as absent (they simply won't match those tiers).
func (s *Store) RememberFull(experimentID, dataProject, experimentName, studyID, projectID string) error {
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
		StudyID:        studyID,
		ProjectID:      projectID,
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Replace(path, raw, mode)
}
