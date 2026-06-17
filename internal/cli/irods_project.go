package cli

// `mus irods project <NAME>` — list the most recent uploads inside
// one project's iRODS subtree. Companion to `mus irods projects`:
// the plural one picks a name, the singular one drills in.
//
// One genquery, sorted DESC by DATA_MODIFY_TIME, capped at N rows
// (default 20). Fast even on busy projects because we never walk
// the full tree client-side.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/iron"
)

func newIRODSProjectCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "project NAME",
		Short: "list the most recent uploads to one data_project on iRODS",
		Long: "Drills into one project's iRODS subtree at\n" +
			"<irods_home>/project/<NAME>/ and shows the most recently\n" +
			"modified data objects, newest first.\n\n" +
			"NAME must be a valid data_project (NameYear format) — same\n" +
			"validation `mus eln link` / `mus config set data_project`\n" +
			"applies. Use `mus irods projects` to list all known names.\n\n" +
			"One genquery (recursive LIKE-prefix), sorted DESC by\n" +
			"DATA_MODIFY_TIME, capped at -n N (default 20). Useful for a\n" +
			"quick 'what landed lately?' over a project.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSProject(cmd, args[0], limit)
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20,
		"how many recent objects to show")
	return cmd
}

func runIRODSProject(cmd *cobra.Command, name string, limit int) error {
	if err := dataproject.ValidateName(name); err != nil {
		return err
	}
	if limit < 1 {
		limit = 20
	}
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
	projectPath := strings.TrimRight(home, "/") + "/project/" + name

	client, err := iron.New()
	if err != nil {
		return err
	}

	rows, err := queryRecentInProject(ctx, client, projectPath, limit)
	if err != nil {
		return fmt.Errorf("querying iRODS for recent objects under %s: %w",
			projectPath, err)
	}

	stdout := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "no data objects found under %s\n", projectPath)
		return nil
	}
	writeProjectObjects(stdout, projectPath, rows)
	return nil
}

// objectRow is one row of `mus irods project NAME` output. RelPath
// is the path BELOW the projectPath (e.g. "microglia_pilot/foo.h5ad"
// when the project is "Fiers2025"), so the project name itself
// doesn't repeat on every line. rawMtime keeps the unparsed iCAT
// timestamp string for sorting (formatICATTime produces a
// human-friendly form that loses the time-of-day precision).
type objectRow struct {
	ModifyTime string
	rawMtime   string
	Size       int64
	Owner      string
	RelPath    string
}

// queryRecentInProject walks the project's iRODS subtree via
// `iron tree --json`, filters to data objects, sorts client-side
// by modified time desc, and returns the top `limit`.
//
// We use `iron tree` instead of `iron query` because older iron
// releases (≤ 0.4.67) reject the SQL forms we need (LIKE filter
// on COLL_NAME, ORDER BY / LIMIT clauses) with
// SYS_INVALID_INPUT_PARAM. Tree is the reliable cross-version
// primitive — `mus irods scan` already uses the same parser.
//
// Performance: tree walks the whole project subtree. Per-project
// scope keeps this bounded (one project's worth of objects, not
// the whole lab). For multi-thousand-object projects the network
// part dominates; the client-side sort + cap is trivial.
func queryRecentInProject(ctx context.Context, client *iron.Client, projectPath string, limit int) ([]objectRow, error) {
	raw, err := client.TreeJSON(ctx, projectPath, "name", "size", "checksum", "date", "creator")
	if err != nil {
		return nil, err
	}
	objects, err := parseIronTreeJSON(strings.NewReader(raw), projectPath)
	if err != nil {
		return nil, fmt.Errorf("parsing iron tree output: %w", err)
	}
	rows := make([]objectRow, 0, len(objects))
	for _, obj := range objects {
		if !obj.IsObject {
			continue // skip collections; we only show data objects
		}
		rel := strings.TrimPrefix(obj.Path, projectPath)
		rel = strings.TrimPrefix(rel, "/")
		rows = append(rows, objectRow{
			ModifyTime: obj.Modified.UTC().Format("2006-01-02"),
			rawMtime:   obj.Modified.UTC().Format(time.RFC3339),
			Size:       obj.Size,
			Owner:      obj.Creator,
			RelPath:    rel,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].rawMtime > rows[j].rawMtime
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// writeProjectObjects renders rows to w. Heading + aligned table.
// Size column is right-aligned + human-readable; everything else
// left-aligned with computed widths.
func writeProjectObjects(w io.Writer, projectPath string, rows []objectRow) {
	fmt.Fprintf(w, "%s/\n", projectPath)
	fmt.Fprintln(w)

	// Pre-render size strings so the width computation is honest.
	sizeStr := make([]string, len(rows))
	for i, r := range rows {
		sizeStr[i] = humanBytes(r.Size)
	}

	dateW := len("modified")
	sizeW := len("size")
	ownerW := len("owner")
	for i, r := range rows {
		if len(r.ModifyTime) > dateW {
			dateW = len(r.ModifyTime)
		}
		if len(sizeStr[i]) > sizeW {
			sizeW = len(sizeStr[i])
		}
		if len(r.Owner) > ownerW {
			ownerW = len(r.Owner)
		}
	}
	fmt.Fprintf(w, "%-*s  %*s  %-*s  %s\n",
		dateW, "modified",
		sizeW, "size",
		ownerW, "owner",
		"path")
	fmt.Fprintln(w, strings.Repeat("-",
		dateW+sizeW+ownerW+10))
	for i, r := range rows {
		fmt.Fprintf(w, "%-*s  %*s  %-*s  %s\n",
			dateW, r.ModifyTime,
			sizeW, sizeStr[i],
			ownerW, r.Owner,
			r.RelPath)
	}
}

// (humanBytes lives in irods_scan.go and is shared across the
// irods subcommands.)
