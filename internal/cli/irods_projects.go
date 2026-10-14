package cli

// `mus irods projects` — list distinct data_project names that
// already exist on iRODS, merged across the new
// <irods_home>/projects/<YEAR>/<data_project> layout AND the
// legacy <irods_home>/project/<data_project> layout. Used by
// depositors picking a NEW data_project name to avoid colliding
// with one that's already in use.
//
// No vocabulary; the source of truth is whatever subcollections
// happen to exist on iRODS. Two ls passes (one per layout root)
// merged client-side, deduplicated by name. Either tree may be
// missing entirely (legacy never used / new tree predates the
// upgrade) — both walks are best-effort.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/localprojects"
)

func newIRODSProjectsCmd() *cobra.Command {
	var details bool
	cmd := &cobra.Command{
		Use:   "projects [PATTERN]",
		Short: "list (or search) data_project names already in use on iRODS",
		Long: "Lists the data_project names that have already been deposited\n" +
			"on the lab's iRODS catalog. Walks both layouts:\n" +
			"  - new:    <irods_home>/projects/<YEAR>/<NAME>\n" +
			"  - legacy: <irods_home>/project/<NAME>\n" +
			"and merges (deduplicates by name). Run this before\n" +
			"`mus eln link` / `mus config set data_project` to avoid\n" +
			"colliding with an existing project.\n\n" +
			"Output is one project per line, alphabetically. With --details,\n" +
			"runs one `iron tree` per project to summarise:\n" +
			"  - files       data-object count in the project subtree\n" +
			"  - size        sum of DATA_SIZE across those objects\n" +
			"  - uploaders   comma-joined sorted u-numbers of the people\n" +
			"                whose creator stamp shows up in the subtree\n" +
			"  - last upload max modified time across the data objects\n" +
			"  - local folder the most recent folder on THIS machine that\n" +
			"                uploaded to the project (from\n" +
			"                ~/.local/share/mus/local_projects.json — written\n" +
			"                automatically on every `mus irods upload` /\n" +
			"                `mus s3 upload` going forward)\n\n" +
			"--details touches each project's iRODS subtree once, so the\n" +
			"command is noticeably slower with many large projects.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := ""
			if len(args) == 1 {
				pattern = args[0]
			}
			return runIRODSProjects(cmd, details, pattern)
		},
	}
	cmd.Flags().BoolVar(&details, "details", false,
		"include uploader (iRODS owner) and creation date per project")
	return cmd
}

func runIRODSProjects(cmd *cobra.Command, details bool, pattern string) error {
	ctx := cmd.Context()
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	home := env.String("irods_home")
	if home == "" {
		home = defaults.IRODSHome
	}
	if home == "" {
		return fmt.Errorf("irods_home not set (no default available); " +
			"`mus config set irods_home /your/zone/home/lab`")
	}
	homeTrim := strings.TrimRight(home, "/")
	// Two layouts to walk:
	//   - NEW: <home>/projects/<YEAR>/<data_project>
	//   - LEGACY: <home>/project/<data_project>  (uploaded before 0.2.33)
	// Both are merged into one deduplicated list; row.Path keeps the
	// real iRODS path so the detail-stats walker hits the right tree.
	newParent := homeTrim + "/projects"
	legacyParent := homeTrim + "/project"

	client, err := iron.New()
	if err != nil {
		return err
	}

	rows, err := queryProjectsBothLayouts(ctx, client, newParent, legacyParent)
	if err != nil {
		return fmt.Errorf("querying iRODS for projects: %w", err)
	}

	// Optional case-insensitive substring filter on the project name.
	if pattern != "" {
		needle := strings.ToLower(pattern)
		filtered := rows[:0]
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r.Name), needle) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	stdout := cmd.OutOrStdout()
	if len(rows) == 0 {
		if pattern != "" {
			fmt.Fprintf(stdout, "no projects matching %q under %s or %s\n", pattern, newParent, legacyParent)
		} else {
			fmt.Fprintf(stdout, "no projects found under %s or %s\n", newParent, legacyParent)
		}
		return nil
	}
	if details {
		enrichWithLocalFolders(rows)
		enrichWithIRODSTreeStatsPerRow(ctx, client, rows)
	}
	writeProjects(stdout, rows, details)
	return nil
}

// queryProjectsBothLayouts merges projects from the new
// <home>/projects/<YEAR>/<name> tree with the legacy
// <home>/project/<name> tree. Same-named project in both layouts
// keeps the NEW path (where future uploads go).
//
// New layout walk: list <home>/projects/, each child is a YEAR
// directory; list each YEAR's children to get the data_projects.
// Legacy walk: list <home>/project/ directly.
//
// Either tree may be missing entirely (legacy never used on a
// fresh deployment; new tree absent on a site that hasn't
// upgraded yet) — both are best-effort, errors are swallowed and
// reported via empty results.
func queryProjectsBothLayouts(ctx context.Context, client *iron.Client,
	newParent, legacyParent string) ([]projectRow, error) {
	byName := map[string]projectRow{}

	// New layout: /projects/<YEAR>/<name>
	if yearEntries, err := listCollections(ctx, client, newParent); err == nil {
		for _, yr := range yearEntries {
			yearPath := newParent + "/" + yr
			projEntries, err := listCollections(ctx, client, yearPath)
			if err != nil {
				continue
			}
			for _, p := range projEntries {
				if _, exists := byName[p]; !exists {
					byName[p] = projectRow{
						Name: p,
						Path: yearPath + "/" + p,
					}
				}
			}
		}
	}
	// Legacy layout: /project/<name>. Only fill in names we didn't
	// already see in the new tree.
	if entries, err := listCollections(ctx, client, legacyParent); err == nil {
		for _, p := range entries {
			if _, exists := byName[p]; !exists {
				byName[p] = projectRow{
					Name: p,
					Path: legacyParent + "/" + p,
				}
			}
		}
	}
	rows := make([]projectRow, 0, len(byName))
	for _, r := range byName {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// listCollections returns the base names of every COLLECTION
// directly under `parent`. Wrapper around the same iron ls
// machinery queryProjects used.
func listCollections(ctx context.Context, client *iron.Client, parent string) ([]string, error) {
	raw, err := client.ListJSON(ctx, parent)
	if err != nil {
		return nil, err
	}
	entries, err := parseIronListJSON(raw)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range entries {
		if e.IsObject {
			continue
		}
		name := path.Base(e.Name)
		if name == "" || name == "." || name == "/" || name == path.Base(parent) {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// enrichWithIRODSTreeStatsPerRow walks each row's stored Path
// individually — replaces the old parent-based enrichment now that
// rows can live under different parents (new vs legacy layout).
func enrichWithIRODSTreeStatsPerRow(ctx context.Context, client *iron.Client, rows []projectRow) {
	for i := range rows {
		enrichOneRow(ctx, client, &rows[i])
	}
}

// enrichWithLocalFolders fills LocalFolder on each row from the
// local projects log. Best-effort — missing log / read errors
// leave every row's LocalFolder empty.
func enrichWithLocalFolders(rows []projectRow) {
	store, err := localprojects.Open("")
	if err != nil {
		return
	}
	all, err := store.All()
	if err != nil {
		return
	}
	for i := range rows {
		entry, ok := all[rows[i].Name]
		if !ok {
			continue
		}
		// Stamp folder + a compact "YYYY-MM-DD" suffix from LastUsed.
		// Skip the suffix if LastUsed is zero (shouldn't happen but
		// keep robust to hand-edits of the JSON).
		if entry.LastUsed.IsZero() {
			rows[i].LocalFolder = entry.Folder
		} else {
			rows[i].LocalFolder = fmt.Sprintf("%s (%s)",
				entry.Folder, entry.LastUsed.Format("2006-01-02"))
		}
	}
}

// enrichOneRow fills the per-row stats by walking row.Path. Each
// row stores its own iRODS path (new vs legacy layout), so this
// works for the mixed-layout listing without a shared parent.
//
// Best-effort: a tree failure leaves the row's stats empty.
func enrichOneRow(ctx context.Context, client *iron.Client, row *projectRow) {
	if row.Path == "" {
		return
	}
	raw, err := client.TreeJSON(ctx, row.Path, "name", "size", "checksum", "date", "creator")
	if err != nil {
		return
	}
	stats := projectStatsFromTreeJSON(raw)
	row.FileCount = stats.fileCount
	row.TotalBytes = stats.totalBytes
	row.Uploaders = stats.uploaders
	row.LastUpload = stats.lastUpload
}

// projectTreeStats is the per-project summary extracted from one
// `iron tree --json` response. Internal — fields are wired into
// projectRow by enrichWithIRODSTreeStats.
type projectTreeStats struct {
	fileCount  int
	totalBytes int64
	uploaders  string // comma-joined sorted set
	lastUpload string // formatted YYYY-MM-DD or empty
}

// projectStatsFromTreeJSON parses iron tree's NDJSON and rolls up
// stats across all DATA OBJECTS (not collections) in the subtree.
// Collections are identified by absent `checksum`; data objects by
// presence of one.
func projectStatsFromTreeJSON(raw string) projectTreeStats {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var stats projectTreeStats
	uploaders := map[string]bool{}
	var bestRawMod string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		checksum, _ := m["checksum"].(string)
		if checksum == "" {
			continue // collection
		}
		stats.fileCount++
		if sz, ok := m["size"].(float64); ok {
			stats.totalBytes += int64(sz)
		}
		if creator, ok := m["creator"].(string); ok && creator != "" {
			uploaders[creator] = true
		}
		if mod, ok := m["modified"].(string); ok && mod > bestRawMod {
			bestRawMod = mod
		}
	}
	if len(uploaders) > 0 {
		keys := make([]string, 0, len(uploaders))
		for k := range uploaders {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		stats.uploaders = strings.Join(keys, ", ")
	}
	if bestRawMod != "" {
		stats.lastUpload = formatICATTime(bestRawMod)
	}
	return stats
}

// formatICATTime renders an iCAT DATA_MODIFY_TIME string compactly.
// iCAT emits either Unix epoch seconds ("1718000000") or ISO-ish
// strings depending on the iron version — handle both, fall back
// to the raw value on parse failure.
func formatICATTime(raw string) string {
	if raw == "" {
		return ""
	}
	// Try Unix seconds.
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	if t, err := time.Parse("2006-01-02T15:04:05", raw); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	// Some iron builds emit unix seconds as a decimal string.
	var secs int64
	if _, err := fmt.Sscanf(raw, "%d", &secs); err == nil && secs > 0 {
		return time.Unix(secs, 0).UTC().Format("2006-01-02")
	}
	return raw
}

// projectRow holds one project's display fields. Only `Name` is
// populated unless --details was requested.
type projectRow struct {
	Name        string
	Path        string // full iRODS path to the project's collection
	FileCount   int    // data objects in the project subtree (--details)
	TotalBytes  int64  // sum of DATA_SIZE across the subtree (--details)
	Uploaders   string // comma-joined sorted set of `creator` u-numbers (--details)
	LastUpload  string // max modified across data objects (--details)
	LocalFolder string // ~/.local/share/mus/local_projects.json (--details)
}

// listCollections + queryProjectsBothLayouts above replaced the
// single-parent queryProjects helper that lived here. The
// historical comments about iron 0.4.67 genquery rejection and
// the NDJSON-vs-array shape carry over: listCollections still
// uses iron ls --json + parseIronListJSON for the same reasons.

// ironListEntry is one record from `iron ls --json`. Different iron
// versions emit slightly different field shapes — we accept the
// most common ones. An entry is a "data object" if it carries a
// checksum or has type == "data"/"object"; otherwise it's a
// collection (or unknown, which we treat as collection).
type ironListEntry struct {
	Name     string
	IsObject bool
}

// parseIronListJSON handles both NDJSON and JSON-array variants of
// `iron ls --json`. Different iron versions emit different shapes;
// the leading character distinguishes them: `[` ⇒ array, `{` ⇒
// NDJSON. Empty input is a successful zero-result.
func parseIronListJSON(raw string) ([]ironListEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	switch raw[0] {
	case '[':
		var arr []map[string]any
		if err := json.Unmarshal([]byte(raw), &arr); err != nil {
			return nil, fmt.Errorf("decoding iron ls --json array: %w", err)
		}
		out := make([]ironListEntry, 0, len(arr))
		for _, m := range arr {
			out = append(out, ironListEntryFromMap(m))
		}
		return out, nil
	case '{':
		entries := parseIronListNDJSON(raw)
		return entries, nil
	default:
		return nil, fmt.Errorf("unrecognised iron ls --json shape (first 200 bytes: %.200s)", raw)
	}
}

func parseIronListNDJSON(raw string) []ironListEntry {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []ironListEntry
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			// One bad line shouldn't kill the whole listing —
			// just skip it.
			continue
		}
		out = append(out, ironListEntryFromMap(m))
	}
	return out
}

func ironListEntryFromMap(m map[string]any) ironListEntry {
	name, _ := m["name"].(string)
	checksum, _ := m["checksum"].(string)
	typeStr, _ := m["type"].(string)
	isObject := checksum != "" ||
		strings.EqualFold(typeStr, "data") ||
		strings.EqualFold(typeStr, "dataobject") ||
		strings.EqualFold(typeStr, "data_object") ||
		strings.EqualFold(typeStr, "object") ||
		strings.EqualFold(typeStr, "file")
	return ironListEntry{Name: name, IsObject: isObject}
}

// writeProjects renders rows to w. Plain list when !details;
// aligned table when details. The "uploaders" column tends to be
// the widest after "name" (multi-author projects). "local folder"
// is the unbounded trailing column.
func writeProjects(w io.Writer, rows []projectRow, details bool) {
	if !details {
		for _, r := range rows {
			fmt.Fprintln(w, r.Name)
		}
		return
	}

	// Pre-render the right-aligned numeric / size cells so width
	// computation matches what we actually print.
	files := make([]string, len(rows))
	sizes := make([]string, len(rows))
	for i, r := range rows {
		if r.FileCount > 0 {
			files[i] = fmt.Sprintf("%d", r.FileCount)
		}
		if r.TotalBytes > 0 {
			sizes[i] = humanBytes(r.TotalBytes)
		}
	}

	nameW := len("name")
	filesW := len("files")
	sizeW := len("size")
	uploadersW := len("uploaders")
	dateW := len("last upload")
	for i, r := range rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(files[i]) > filesW {
			filesW = len(files[i])
		}
		if len(sizes[i]) > sizeW {
			sizeW = len(sizes[i])
		}
		if len(r.Uploaders) > uploadersW {
			uploadersW = len(r.Uploaders)
		}
		if len(r.LastUpload) > dateW {
			dateW = len(r.LastUpload)
		}
	}

	header := fmt.Sprintf("%-*s  %*s  %*s  %-*s  %-*s  %s",
		nameW, "name",
		filesW, "files",
		sizeW, "size",
		uploadersW, "uploaders",
		dateW, "last upload",
		"local folder")
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)))
	for i, r := range rows {
		fmt.Fprintf(w, "%-*s  %*s  %*s  %-*s  %-*s  %s\n",
			nameW, r.Name,
			filesW, files[i],
			sizeW, sizes[i],
			uploadersW, r.Uploaders,
			dateW, r.LastUpload,
			r.LocalFolder)
	}
}

// escapeGenquerySingleQuotes doubles every ' so the value is safe
// to embed inside a single-quoted genquery literal. iCAT genquery
// has no proper parameter binding — this is the standard defense.
func escapeGenquerySingleQuotes(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
