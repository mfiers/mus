// Package config reads cascading `.mus` TOML files.
//
// On every call to Load(dir), config walks from `dir` upward to the filesystem
// root, collects every `.mus` file along the way, and merges them so that
// closer (deeper) files override / extend the ones above them. The result is a
// single Env keyed by lowercase string.
//
// List-valued keys (currently `tag` and `collaborator`) merge instead of
// overriding: a value prefixed with `-` removes a previously added entry.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the name of the cascading folder-config file mus looks for.
const FileName = ".mus"

// ListKeys are TOML keys treated as merged string lists across the cascade.
// A value prefixed with `-` removes a previously added entry.
var ListKeys = map[string]bool{
	"tag":          true,
	"collaborator": true,
}

// Env is the merged configuration for a directory. Scalar values are stored
// as strings; list values as []string. Callers should use the typed accessors
// (String / List) rather than poking at the map directly.
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

// List returns the list value for key (must be a ListKey).
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

// All returns a copy of every key/value (scalars and lists). Useful for
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

// Files returns the cascade of `.mus` files that contributed to this Env,
// root-most first.
func (e *Env) Files() []string { return append([]string(nil), e.files...) }

// Set assigns key=value in-memory (does not persist). For tests / overrides.
func (e *Env) Set(key, value string) {
	if e.values == nil {
		e.values = map[string]any{}
	}
	if ListKeys[key] {
		cur := e.List(key)
		applyListVal(&cur, value)
		e.values[key] = cur
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

// Load discovers every `.mus` from filesystem root down to `dir`, parses each
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
	env := &Env{values: map[string]any{}, files: files}
	for _, f := range files {
		single, err := loadSingle(f)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		mergeInto(env.values, single)
	}
	return env, nil
}

// LoadLocal reads only the `.mus` in `dir` itself (no cascade). Returns an
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
	return &Env{values: values, files: []string{path}}, nil
}

// Save writes the given key/value pairs into `dir`/.mus, merging with whatever
// is already there. For list keys the values are appended (with `-prefix`
// removal semantics).
func Save(dir string, kv map[string]string) error {
	local, err := LoadLocal(dir)
	if err != nil {
		return err
	}
	for k, v := range kv {
		if ListKeys[k] {
			cur := local.List(k)
			applyListVal(&cur, v)
			local.values[k] = cur
		} else {
			local.values[k] = v
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(abs, FileName)
	return atomicWriteToml(path, local.values)
}

// discover walks from `/` down to `dir`, returning every `.mus` in load order.
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
	// reverse to get root-most first
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
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
	parsed, err := parseTOML(raw)
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

// parseTOML accepts only top-level scalar string / array-of-string values.
// Nested tables are flattened with dot-separated keys (`[eln] experiment_id`
// becomes `eln.experiment_id`).
func parseTOML(raw []byte) (map[string]any, error) {
	var generic map[string]any
	if err := toml.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	flat := map[string]any{}
	flattenInto("", generic, flat)
	// normalise: any []any of strings -> []string
	for k, v := range flat {
		if arr, ok := v.([]any); ok {
			ss := make([]string, 0, len(arr))
			for _, e := range arr {
				ss = append(ss, fmt.Sprint(e))
			}
			flat[k] = ss
		}
	}
	return flat, nil
}

func flattenInto(prefix string, src, dst map[string]any) {
	for k, v := range src {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		switch tv := v.(type) {
		case map[string]any:
			flattenInto(full, tv, dst)
		default:
			dst[full] = tv
		}
	}
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if ListKeys[k] {
			cur, _ := dst[k].([]string)
			switch sv := v.(type) {
			case []string:
				for _, s := range sv {
					applyListVal(&cur, s)
				}
			case string:
				applyListVal(&cur, sv)
			}
			dst[k] = cur
			continue
		}
		dst[k] = v
	}
}

func applyListVal(lst *[]string, v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	if strings.HasPrefix(v, "-") {
		rm := v[1:]
		out := (*lst)[:0]
		for _, e := range *lst {
			if e != rm {
				out = append(out, e)
			}
		}
		*lst = out
		return
	}
	for _, e := range *lst {
		if e == v {
			return
		}
	}
	*lst = append(*lst, v)
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

// atomicWriteToml serialises values back out, preserving sorted top-level
// scalar keys and grouping dotted keys under a single `[section]`.
func atomicWriteToml(path string, values map[string]any) error {
	// rebuild a nested map for serialisation
	nested := map[string]any{}
	for k, v := range values {
		parts := strings.Split(k, ".")
		cur := nested
		for i, part := range parts {
			if i == len(parts)-1 {
				cur[part] = v
				break
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
	}
	// sort top-level keys for deterministic output
	sortedKeys := make([]string, 0, len(nested))
	for k := range nested {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	ordered := map[string]any{}
	for _, k := range sortedKeys {
		ordered[k] = nested[k]
	}

	buf, err := toml.Marshal(ordered)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mus.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
