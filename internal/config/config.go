// Package config reads cascading `.env` files using the legacy flat
// key=value format.
//
// Walking from the filesystem root *down* to the working directory, every
// `.env` along the way is loaded in order; later (deeper) files override or
// extend earlier (shallower) ones. List-valued keys (`tag`, `collaborator`)
// merge instead of overriding — a value prefixed with `-` removes a
// previously added entry.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"codeberg.org/mfiers/mus/internal/atomicfile"
	"codeberg.org/mfiers/mus/internal/envformat"
)

// FileName is the cascading folder-config file mus looks for.
const FileName = ".env"

// GlobalPath returns the user-level mus config file path:
// $XDG_CONFIG_HOME/mus/.env, defaulting to ~/.config/mus/.env. Returns "" if
// no home directory can be determined.
//
// This file is injected as the root-most entry of the cascade by Load() so a
// `mus eln disable --global` decision survives no matter where you run mus
// from (working dirs outside $HOME are common on HPC).
func GlobalPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "mus", FileName)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "mus", FileName)
}

// listKeys are the keys treated as merged string lists across the cascade.
// A value prefixed with `-` removes a previously added entry. Matches the
// legacy Python `mus` LIST_KEYS.
var listKeys = map[string]bool{
	"tag":          true,
	"collaborator": true,
	// Broadly-applicable experiment metadata that's documented as a
	// list (joint-vocabulary proposal §3.7). Bare keys, not prefixed —
	// set e.g. `cell_type=microglial cell,astrocyte` in .env and it
	// cascades + merges as expected. Same fields apply for non-h5ad
	// data files when mus grows them.
	"tissue":                     true,
	"cell_type":                  true,
	"disease":                    true,
	"condition":                  true,
	"doi":                        true,
	"pmid":                       true,
	"link":                       true,
	"tissue_ontology_term_id":    true,
	"cell_type_ontology_term_id": true,
	"disease_ontology_term_id":   true,
}

func parseOpts() envformat.Options {
	return envformat.Options{ListKeys: listKeys}
}

// Env is the merged configuration for a directory. Scalar values are stored
// as strings; list values as []string.
type Env struct {
	values map[string]any
	// files lists the cascade in load order (root-most first, deepest last).
	files []string
}

// String returns the scalar value for key, or "" if absent / not a scalar.
func (e *Env) String(key string) string {
	if e == nil {
		return ""
	}
	if v, ok := e.values[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Has reports whether key is set.
func (e *Env) Has(key string) bool {
	if e == nil {
		return false
	}
	_, ok := e.values[key]
	return ok
}

// List returns the list value for key (must be a list key).
func (e *Env) List(key string) []string {
	if e == nil {
		return nil
	}
	v, ok := e.values[key]
	if !ok {
		return nil
	}
	if lst, ok := v.([]string); ok {
		return append([]string(nil), lst...)
	}
	return nil
}

// All returns a copy of every key/value pair (scalars and lists). Useful for
// `mus config show`.
func (e *Env) All() map[string]any {
	out := make(map[string]any, len(e.values))
	for k, v := range e.values {
		switch tv := v.(type) {
		case []string:
			out[k] = append([]string(nil), tv...)
		default:
			out[k] = v
		}
	}
	return out
}

// Files returns the cascade of `.env` files that contributed to this Env,
// root-most first.
func (e *Env) Files() []string { return append([]string(nil), e.files...) }

// Set assigns key=value in-memory (does not persist). For tests / overrides.
func (e *Env) Set(key, value string) {
	if e.values == nil {
		e.values = map[string]any{}
	}
	if listKeys[key] {
		cur := e.List(key)
		envformat.MergeInto(map[string]any{key: cur}, map[string]any{key: []string{value}}, parseOpts())
		// Simpler: just apply directly.
		cur = e.List(key)
		out := []string{}
		envformat.MergeInto(map[string]any{key: out}, map[string]any{key: append(cur, value)}, parseOpts())
		// Honestly: easiest correct path is to round-trip through MergeInto
		// against a fresh empty list.
		merged := map[string]any{}
		envformat.MergeInto(merged, map[string]any{key: append(cur, value)}, parseOpts())
		e.values[key] = merged[key]
		return
	}
	e.values[key] = value
}

// fileCache memoises parsing per-path; invalidated by mtime change.
type cacheEntry struct {
	mtimeNs int64
	size    int64
	values  map[string]any
}

var (
	fileCacheMu sync.Mutex
	fileCache   = map[string]cacheEntry{}
)

// Load discovers every `.env` from filesystem root down to `dir`, parses each
// (with mtime-keyed memoisation), and returns the merged Env.
func Load(dir string) (*Env, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	files, err := discover(abs)
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for _, f := range files {
		single, err := loadSingle(f)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		envformat.MergeInto(merged, single, parseOpts())
	}
	return &Env{values: merged, files: files}, nil
}

// LoadLocal reads only the `.env` in `dir` itself (no cascade). Returns an
// empty Env if the file does not exist.
func LoadLocal(dir string) (*Env, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(abs, FileName)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return &Env{values: map[string]any{}}, nil
		}
		return nil, err
	}
	values, err := loadSingle(path)
	if err != nil {
		return nil, err
	}
	// Single-file: resolve -prefix items now so callers see canonical lists.
	envformat.Resolve(values, parseOpts())
	return &Env{values: values, files: []string{path}}, nil
}

// Save writes the given key/value pairs into `dir`/.env, merging with whatever
// is already there. For list keys the values are appended (with `-prefix`
// removal semantics).
func Save(dir string, kv map[string]string) error {
	local, err := LoadLocal(dir)
	if err != nil {
		return err
	}
	for k, v := range kv {
		if listKeys[k] {
			cur := local.List(k)
			// Append the new value and resolve via Resolve so any `-prefix`
			// in the new value takes effect against cur.
			tmp := map[string]any{k: append(cur, v)}
			envformat.Resolve(tmp, parseOpts())
			local.values[k] = tmp[k]
		} else {
			local.values[k] = v
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(abs, FileName)
	return atomicWrite(path, local.values)
}

// SaveToFile is like Save but writes to an explicit absolute path. Useful for
// the global config (which lives at GlobalPath(), not necessarily inside
// any `dir`). Creates the parent directory if missing.
func SaveToFile(path string, kv map[string]string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	// Load existing values from this exact file (LoadLocal expects a dir).
	values := map[string]any{}
	if _, err := os.Stat(abs); err == nil {
		parsed, err := loadSingle(abs)
		if err != nil {
			return err
		}
		envformat.Resolve(parsed, parseOpts())
		values = parsed
	} else if !os.IsNotExist(err) {
		return err
	}
	for k, v := range kv {
		if listKeys[k] {
			cur, _ := values[k].([]string)
			tmp := map[string]any{k: append(cur, v)}
			envformat.Resolve(tmp, parseOpts())
			values[k] = tmp[k]
		} else {
			values[k] = v
		}
	}
	return atomicWrite(abs, values)
}

// Unset removes `key` from `dir`/.env and rewrites the file. No-op if the
// file or the key does not exist. Removes the file entirely once empty.
func Unset(dir string, key string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(abs, FileName)
	return unsetAtPath(path, key)
}

// UnsetFromFile is the explicit-path variant of Unset, for the global config.
func UnsetFromFile(path string, key string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return unsetAtPath(abs, key)
}

func unsetAtPath(absPath, key string) error {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to unset
		}
		return err
	}
	// Walk the file line-by-line so we preserve user comments and
	// blank lines. The legacy code deflated the whole file through
	// envformat.Marshal which silently dropped comments — that lost
	// project documentation on every Unset call.
	surviving, removed := stripKeyLines(string(raw), key)
	if !removed {
		return nil // key wasn't present
	}
	if isEffectivelyEmpty(surviving) {
		// "If empty don't leave it floating around" — delete only when
		// the file truly has nothing useful left (no keys, no
		// comments, just whitespace).
		return os.Remove(absPath)
	}
	return atomicfile.Replace(absPath, []byte(surviving), 0o644)
}

// stripKeyLines returns the file content with every line that assigns
// `key` removed. A line "matches" when its trimmed prefix (before `=`)
// equals key. Blank lines and `#` comment lines pass through. Returns
// the surviving text and a bool indicating whether any line was
// dropped.
func stripKeyLines(content, key string) (string, bool) {
	var b strings.Builder
	b.Grow(len(content))
	removed := false
	// strings.SplitAfter keeps line terminators on each chunk so
	// reassembly preserves the original line-ending style and the
	// presence (or absence) of a final newline.
	for _, line := range strings.SplitAfter(content, "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			b.WriteString(line)
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx < 0 {
			// Malformed line — keep it; Unset isn't a syntax fixer.
			b.WriteString(line)
			continue
		}
		if strings.TrimSpace(trimmed[:idx]) == key {
			removed = true
			continue
		}
		b.WriteString(line)
	}
	return b.String(), removed
}

// isEffectivelyEmpty reports whether content has no meaningful payload
// — only whitespace lines, no keys, no comments. A file with even a
// single comment is NOT empty (the user wrote that comment on
// purpose).
func isEffectivelyEmpty(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

// discover walks from `/` down to `dir`, returning every `.env` in load order.
// Prepends the user-level GlobalPath() entry (if it exists) so global config
// is the most-overridable entry of the cascade.
func discover(dir string) ([]string, error) {
	var paths []string
	cur := dir
	for {
		candidate := filepath.Join(cur, FileName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			paths = append(paths, candidate)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// reverse to get root-most first (so deeper files MergeInto on top)
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	// Prepend the global config if present. Skip if its path is already in
	// the walk (i.e. ~/.config/mus/.env happens to be on the walked-up path).
	if gp := GlobalPath(); gp != "" {
		if st, err := os.Stat(gp); err == nil && !st.IsDir() {
			already := false
			for _, p := range paths {
				if p == gp {
					already = true
					break
				}
			}
			if !already {
				paths = append([]string{gp}, paths...)
			}
		}
	}
	return paths, nil
}

func loadSingle(path string) (map[string]any, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	fileCacheMu.Lock()
	if entry, ok := fileCache[path]; ok &&
		entry.mtimeNs == st.ModTime().UnixNano() &&
		entry.size == st.Size() {
		fileCacheMu.Unlock()
		return copyValues(entry.values), nil
	}
	fileCacheMu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := envformat.Parse(raw, parseOpts())
	if err != nil {
		return nil, err
	}

	fileCacheMu.Lock()
	fileCache[path] = cacheEntry{
		mtimeNs: st.ModTime().UnixNano(),
		size:    st.Size(),
		values:  parsed,
	}
	fileCacheMu.Unlock()
	return copyValues(parsed), nil
}

func copyValues(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if lst, ok := v.([]string); ok {
			out[k] = append([]string(nil), lst...)
		} else {
			out[k] = v
		}
	}
	return out
}

func atomicWrite(path string, values map[string]any) error {
	raw, err := envformat.Marshal(values)
	if err != nil {
		return err
	}
	return atomicfile.Replace(path, raw, 0o644)
}
