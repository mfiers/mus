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

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/iron"
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
			"adds the iRODS owner (uploader who created the subcollection)\n" +
			"and the creation date.\n\n" +
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
	writeProjects(stdout, rows, details)
	return nil
}

// projectRow holds one collection's metadata. Only `Name` is
// populated unless --details was requested.
type projectRow struct {
	Name       string
	Owner      string
	CreateTime string
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
	rows := make([]projectRow, 0, len(cells))
	for _, row := range cells {
		if len(row) == 0 {
			continue
		}
		name := path.Base(row[0])
		if name == "" || name == "." || name == "/" {
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
		rows = append(rows, entry)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// writeProjects renders rows to w. Plain list when !details;
// aligned table when details — owner column is unbounded length so
// we compute the width up front.
func writeProjects(w io.Writer, rows []projectRow, details bool) {
	if !details {
		for _, r := range rows {
			fmt.Fprintln(w, r.Name)
		}
		return
	}
	nameWidth := 0
	ownerWidth := 0
	for _, r := range rows {
		if len(r.Name) > nameWidth {
			nameWidth = len(r.Name)
		}
		if len(r.Owner) > ownerWidth {
			ownerWidth = len(r.Owner)
		}
	}
	if nameWidth < 4 {
		nameWidth = 4
	}
	if ownerWidth < 5 {
		ownerWidth = 5
	}
	fmt.Fprintf(w, "%-*s  %-*s  %s\n", nameWidth, "name", ownerWidth, "owner", "created")
	fmt.Fprintln(w, strings.Repeat("-", nameWidth+ownerWidth+22))
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s  %-*s  %s\n",
			nameWidth, r.Name, ownerWidth, r.Owner, r.CreateTime)
	}
}

// escapeGenquerySingleQuotes doubles every ' so the value is safe
// to embed inside a single-quoted genquery literal. iCAT genquery
// has no proper parameter binding — this is the standard defense.
func escapeGenquerySingleQuotes(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
