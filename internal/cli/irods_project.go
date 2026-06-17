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
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

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

// queryRecentInProject fetches every data object under projectPath
// via one genquery, sorts client-side by DATA_MODIFY_TIME desc,
// and returns the top `limit`.
//
// We sort + cap CLIENT-SIDE because iron's genquery (iquest) doesn't
// support `ORDER BY` / `LIMIT` clauses — those return
// SYS_INVALID_INPUT_PARAM. For typical projects (tens to a few
// thousand objects) the cost is negligible. For very large projects
// the whole row set is still streamed and we just discard the tail
// — fine for the "what landed lately?" use case.
//
// iCAT path-prefix matching: COLL_NAME LIKE 'projectPath/%' catches
// every data object in a subcollection. The trailing `/` means
// projectPath itself isn't matched (subcollections only). The
// `mus irods upload` layout always puts files under an experiment
// subcollection, so the root-level case is rare in practice.
func queryRecentInProject(ctx context.Context, client *iron.Client, projectPath string, limit int) ([]objectRow, error) {
	sql := fmt.Sprintf(
		"select DATA_NAME, COLL_NAME, DATA_MODIFY_TIME, DATA_OWNER_NAME, DATA_SIZE "+
			"where COLL_NAME like '%s/%%'",
		escapeGenquerySingleQuotes(projectPath),
	)
	raw, err := client.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	var cells [][]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &cells); err != nil {
		return nil, fmt.Errorf("decoding iron query output: %w (raw=%q)", err, raw)
	}
	rows := make([]objectRow, 0, len(cells))
	for _, row := range cells {
		if len(row) < 5 {
			continue
		}
		dataName := row[0]
		collName := row[1]
		mtime := row[2]
		owner := row[3]
		sizeStr := row[4]

		// Build the relative path: <coll under projectPath>/<dataName>.
		relColl := strings.TrimPrefix(collName, projectPath)
		relColl = strings.TrimPrefix(relColl, "/")
		var rel string
		if relColl == "" {
			rel = dataName
		} else {
			rel = relColl + "/" + dataName
		}

		var size int64
		if v, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			size = v
		}

		rows = append(rows, objectRow{
			ModifyTime: formatICATTime(mtime),
			rawMtime:   mtime,
			Size:       size,
			Owner:      owner,
			RelPath:    rel,
		})
	}
	// Sort newest-first by the RAW mtime string. iCAT emits either
	// RFC3339 / ISO timestamps or unix epoch seconds — both sort
	// correctly lexicographically WITHIN the same encoding (the
	// catalog never mixes encodings within a single query result).
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
