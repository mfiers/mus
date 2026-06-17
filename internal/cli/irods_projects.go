package cli

// `mus irods projects` — list distinct data_project names that
// already exist on iRODS under <irods_home>/project/. Used by
// depositors picking a NEW data_project name to avoid colliding
// with one that's already in use.
//
// No vocabulary; the source of truth is whatever subcollections
// happen to exist on iRODS. Fast: one genquery against COLL_NAME
// keyed on COLL_PARENT_NAME (no recursion, no AVU filter).

import (
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
		Use:   "projects",
		Short: "list data_project names already in use on iRODS",
		Long: "Lists the data_project names that have already been deposited\n" +
			"under <irods_home>/project/ on the lab's iRODS catalog. Run\n" +
			"this before `mus eln link` / `mus config set data_project` to\n" +
			"avoid colliding with an existing project.\n\n" +
			"Output is one project per line, alphabetically. With --details,\n" +
			"adds:\n" +
			"  - iRODS owner (u-number of whoever created the subcollection)\n" +
			"  - iRODS creation date\n" +
			"  - iRODS last-modified time (max DATA_MODIFY_TIME under the\n" +
			"    project; one extra genquery per project — slower with many)\n" +
			"  - local folder you most recently uploaded from for this\n" +
			"    project on this machine (from ~/.local/share/mus/local_projects.json,\n" +
			"    updated automatically on every `mus irods upload` / `mus s3 upload`)\n\n" +
			"This is a passive read against the iCAT — no AVU stamping is\n" +
			"required for a project to show up; the subcollection's mere\n" +
			"existence under <irods_home>/project/ is the source of truth.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSProjects(cmd, details)
		},
	}
	cmd.Flags().BoolVar(&details, "details", false,
		"include uploader (iRODS owner) and creation date per project")
	return cmd
}

func runIRODSProjects(cmd *cobra.Command, details bool) error {
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
	parent := strings.TrimRight(home, "/") + "/project"

	client, err := iron.New()
	if err != nil {
		return err
	}

	rows, err := queryProjects(ctx, client, parent, details)
	if err != nil {
		return fmt.Errorf("querying iRODS for projects under %s: %w", parent, err)
	}

	stdout := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "no projects found under %s\n", parent)
		return nil
	}
	if details {
		enrichWithLocalFolders(rows)
		enrichWithIRODSLastModified(ctx, client, parent, rows)
	}
	writeProjects(stdout, rows, details)
	return nil
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

// enrichWithIRODSLastModified asks the iCAT for the most recent
// DATA_MODIFY_TIME under each project's collection (recursive,
// LIKE-prefix match). One genquery per project — slow with many
// projects but bounded; called only when --details.
//
// Best-effort: a per-project failure leaves that row's LastUpload
// empty; the rest still get their value. Errors are silent.
func enrichWithIRODSLastModified(ctx context.Context, client *iron.Client, parent string, rows []projectRow) {
	for i := range rows {
		projectPath := parent + "/" + rows[i].Name
		raw, err := client.QueryJSON(ctx, fmt.Sprintf(
			"select DATA_MODIFY_TIME where COLL_NAME like '%s%%' "+
				"order by DATA_MODIFY_TIME desc limit 1",
			escapeGenquerySingleQuotes(projectPath),
		))
		if err != nil {
			continue
		}
		var cells [][]string
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &cells); err != nil {
			continue
		}
		if len(cells) == 0 || len(cells[0]) == 0 {
			continue
		}
		rows[i].LastUpload = formatICATTime(cells[0][0])
	}
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

// projectRow holds one collection's metadata. Only `Name` is
// populated unless --details was requested.
type projectRow struct {
	Name        string
	Owner       string
	CreateTime  string
	LastUpload  string // iCAT: max DATA_MODIFY_TIME under the project (--details)
	LocalFolder string // ~/.local/share/mus/local_projects.json (--details)
}

// queryProjects asks the iCAT for every subcollection one level
// below `parent`. Without `details` we project COLL_NAME only;
// with `details` we add COLL_OWNER_NAME + COLL_CREATE_TIME so the
// depositor sees who landed the project (and roughly when).
func queryProjects(ctx context.Context, client *iron.Client, parent string, details bool) ([]projectRow, error) {
	cols := "COLL_NAME"
	if details {
		cols = "COLL_NAME, COLL_OWNER_NAME, COLL_CREATE_TIME"
	}
	raw, err := client.QueryJSON(ctx, fmt.Sprintf(
		"select %s where COLL_PARENT_NAME = '%s'",
		cols, escapeGenquerySingleQuotes(parent),
	))
	if err != nil {
		return nil, err
	}
	var cells [][]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &cells); err != nil {
		return nil, fmt.Errorf("decoding iron query output: %w (raw=%q)", err, raw)
	}
	// iCAT genquery returns one row per (collection, owner) tuple
	// because the collection-owner relation is many-to-many via the
	// ACL table. Dedupe client-side, keyed on collection name —
	// keep the FIRST (owner, create_time) we see per name. Caller
	// won't usually care which ACL "wins" because the catalog's
	// real creator typically has the earliest entry in the
	// genquery result.
	seen := make(map[string]int, len(cells)) // name → index into `rows`
	rows := make([]projectRow, 0, len(cells))
	for _, row := range cells {
		if len(row) == 0 {
			continue
		}
		name := path.Base(row[0])
		if name == "" || name == "." || name == "/" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		entry := projectRow{Name: name}
		if details {
			if len(row) > 1 {
				entry.Owner = row[1]
			}
			if len(row) > 2 {
				entry.CreateTime = row[2]
			}
		}
		seen[name] = len(rows)
		rows = append(rows, entry)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// writeProjects renders rows to w. Plain list when !details;
// aligned table when details. Column widths are computed per call
// so a few unusually long owner / folder values don't push the
// other rows out of alignment.
func writeProjects(w io.Writer, rows []projectRow, details bool) {
	if !details {
		for _, r := range rows {
			fmt.Fprintln(w, r.Name)
		}
		return
	}
	widths := computeColumnWidths(rows,
		[]string{"name", "owner", "created", "last upload", "local folder"})
	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %s",
		widths[0], "name",
		widths[1], "owner",
		widths[2], "created",
		widths[3], "last upload",
		"local folder")
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)))
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %s\n",
			widths[0], r.Name,
			widths[1], r.Owner,
			widths[2], r.CreateTime,
			widths[3], r.LastUpload,
			r.LocalFolder)
	}
}

// computeColumnWidths returns the max-of(header-label, longest-cell)
// for the first 4 columns. The 5th (local folder) is unbounded —
// rendered without a fixed width.
func computeColumnWidths(rows []projectRow, headers []string) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		cells := []string{r.Name, r.Owner, r.CreateTime, r.LastUpload}
		for i, c := range cells {
			if i >= len(widths)-1 { // skip the unbounded trailing column
				break
			}
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	return widths
}

// escapeGenquerySingleQuotes doubles every ' so the value is safe
// to embed inside a single-quoted genquery literal. iCAT genquery
// has no proper parameter binding — this is the standard defense.
func escapeGenquerySingleQuotes(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
