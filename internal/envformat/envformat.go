// Package envformat reads and writes the flat KEY=VALUE files used both as
// folder-cascading config (`.env`) and per-file sidecars (`*.mus`).
//
// Format (matches the legacy Python `mus` config exactly, plus a `#` comment
// extension):
//
//   - One key=value pair per line. The first `=` splits key from value; the
//     value may itself contain `=` characters.
//   - Whitespace around key and value is trimmed.
//   - Blank lines are skipped.
//   - Lines whose first non-whitespace character is `#` are skipped as
//     comments.
//   - For "list keys" (declared by the caller, e.g. `tag`, `collaborator`,
//     `tags`) the value is split on `,`. A value `-foo` removes a prior
//     `foo` from the list — useful when a deeper file in the cascade wants
//     to drop something a shallower one set.
//
// Anything outside that grammar (a line with no `=`, an unknown escape) is a
// parse error.
package envformat

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// Options control how Parse/Marshal behave.
type Options struct {
	// ListKeys are keys whose values are comma-separated lists. A value
	// `-foo` removes a previously-added `foo` from the list. Keys not in
	// this set are treated as plain string scalars.
	ListKeys map[string]bool
}

// Parse decodes raw .env-style content into a map. Scalar keys map to
// `string`; list keys (per opts) map to `[]string` — items preserved in
// source order, *including* `-prefix` removal markers.
//
// To get a "resolved" list (with -prefix items applied), either:
//   - call MergeInto with an empty dst (typical for sidecar single-file
//     consumers), or
//   - call Resolve on the returned map.
//
// Cross-file cascading uses MergeInto directly: shallower files load first,
// deeper files apply on top via MergeInto.
func Parse(raw []byte, opts Options) (map[string]any, error) {
	result := map[string]any{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			return nil, fmt.Errorf("line %d: missing '=' in %q", lineNum, line)
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNum)
		}
		if opts.ListKeys[key] {
			cur, _ := result[key].([]string)
			for _, item := range strings.Split(value, ",") {
				item = strings.TrimSpace(item)
				if item != "" {
					cur = append(cur, item)
				}
			}
			result[key] = cur
			continue
		}
		result[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Resolve folds in-place: for every list key, applies `-prefix` removals
// against an empty starting list. Useful when you have a single-file map and
// want the canonical, removal-applied form (no `-` items in the output).
func Resolve(values map[string]any, opts Options) {
	for k, v := range values {
		if !opts.ListKeys[k] {
			continue
		}
		items, ok := v.([]string)
		if !ok {
			continue
		}
		var resolved []string
		for _, item := range items {
			applyListItem(&resolved, item)
		}
		values[k] = resolved
	}
}

// MergeInto folds src into dst with list-key semantics: list values append
// (with `-prefix` removal); scalar values overwrite. Used by the config
// cascade to merge a deeper file's contents on top of shallower ones.
func MergeInto(dst, src map[string]any, opts Options) {
	for k, v := range src {
		if opts.ListKeys[k] {
			cur, _ := dst[k].([]string)
			switch sv := v.(type) {
			case []string:
				for _, item := range sv {
					applyListItem(&cur, item)
				}
			case string:
				applyListItem(&cur, sv)
			}
			dst[k] = cur
			continue
		}
		dst[k] = v
	}
}

// Marshal writes a map back to .env format. Keys are emitted in sorted order
// for deterministic output. List values are comma-joined.
//
// Returns an error if any scalar value contains a newline (would corrupt the
// line-based grammar) or if any key contains `=` / whitespace / newline.
func Marshal(values map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, k := range keys {
		if err := validateKey(k); err != nil {
			return nil, err
		}
		switch v := values[k].(type) {
		case string:
			if strings.ContainsRune(v, '\n') {
				return nil, fmt.Errorf("value for %q contains newline", k)
			}
			fmt.Fprintf(&buf, "%s=%s\n", k, v)
		case []string:
			for _, item := range v {
				if strings.ContainsAny(item, ",\n") {
					return nil, fmt.Errorf("list item %q for %q contains ',' or newline", item, k)
				}
			}
			fmt.Fprintf(&buf, "%s=%s\n", k, strings.Join(v, ","))
		default:
			return nil, fmt.Errorf("unsupported value type %T for key %q", v, k)
		}
	}
	return buf.Bytes(), nil
}

func validateKey(k string) error {
	if k == "" {
		return fmt.Errorf("empty key")
	}
	if strings.ContainsAny(k, "=\n\t ") {
		return fmt.Errorf("key %q contains forbidden character", k)
	}
	return nil
}

func applyListItem(lst *[]string, v string) {
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
			return // dedupe
		}
	}
	*lst = append(*lst, v)
}
