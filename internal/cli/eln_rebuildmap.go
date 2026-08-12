package cli

// `mus eln rebuild-map` — reconstruct the experiment→data_project
// mapping store from the iRODS catalog.
//
// The mapping store (~/.local/share/mus/eln_mappings.json) is what the
// GUI's data_project auto-proposer consults. On a fresh machine, or
// after the store schema gained study/project tiers, it can be empty or
// missing study/project IDs even though iRODS already holds thousands of
// objects tagged with mus_data_project + mus_eln_experiment_id.
//
// This command walks the catalog: every object carrying BOTH a
// mus_data_project AND a mus_eln_experiment_id AVU yields one
// experiment→data_project fact. For each distinct experiment it then
// resolves the ELN study/project IDs (one ELN call per experiment,
// cached) so the study/project suggestion tiers work retroactively.
//
// Dry-run by default — prints what it would write. Pass --apply to
// persist. Conflicts (one experiment tagged with two different
// data_projects across objects) are reported and, unless --force,
// left for the operator to resolve.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/elnmap"
	"codeberg.org/mfiers/mus/internal/iron"
)

func newELNRebuildMapCmd() *cobra.Command {
	var apply, force bool
	cmd := &cobra.Command{
		Use:   "rebuild-map",
		Short: "rebuild the experiment→data_project map by scanning iRODS",
		Long: "Scan the iRODS catalog for every object tagged with both\n" +
			"mus_data_project and mus_eln_experiment_id, and rebuild the local\n" +
			"mapping store the GUI uses to auto-propose a data_project. For each\n" +
			"distinct experiment the ELN is queried once to resolve its study and\n" +
			"project ids, so the study/project suggestion tiers work retroactively.\n\n" +
			"Dry-run by default; pass --apply to write. Existing store entries are\n" +
			"kept unless the scan finds a value for the same experiment (scan wins\n" +
			"on --apply).\n\n" +
			"Examples:\n" +
			"  mus eln rebuild-map            # preview\n" +
			"  mus eln rebuild-map --apply    # write the reconstructed map",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNRebuildMap(cmd.Context(), cmd, apply, force)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "persist the rebuilt map (default: dry-run preview only)")
	cmd.Flags().BoolVar(&force, "force", false, "on a data_project conflict for one experiment, take the most common value instead of skipping")
	return cmd
}

// rebuiltEntry is one reconstructed experiment→data_project fact plus
// the ELN-resolved study/project ids.
type rebuiltEntry struct {
	ExperimentID string
	DataProject  string
	StudyID      string
	ProjectID    string
	ExpName      string
	ObjectCount  int // how many iRODS objects supported this fact
}

func runELNRebuildMap(ctx context.Context, cmd *cobra.Command, apply, force bool) error {
	stdout := cmd.OutOrStdout()

	client, err := iron.New()
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, "▶ scanning iRODS catalog for mus_data_project + mus_eln_experiment_id AVUs…")
	dpByPath, err := queryPathValueByAVU(ctx, client, "mus_data_project")
	if err != nil {
		return fmt.Errorf("query mus_data_project: %w", err)
	}
	expByPath, err := queryPathValueByAVU(ctx, client, "mus_eln_experiment_id")
	if err != nil {
		return fmt.Errorf("query mus_eln_experiment_id: %w", err)
	}
	fmt.Fprintf(stdout, "  %d objects with data_project, %d with eln_experiment_id\n",
		len(dpByPath), len(expByPath))

	// Join on path: an object contributes a fact only when it carries
	// both AVUs. Tally data_project votes per experiment so a conflict
	// (same experiment, different data_project across objects) is
	// visible rather than silently last-write-wins.
	votes := map[string]map[string]int{} // expID -> dataProject -> count
	for path, expID := range expByPath {
		dp, ok := dpByPath[path]
		if !ok || dp == "" || expID == "" {
			continue
		}
		if votes[expID] == nil {
			votes[expID] = map[string]int{}
		}
		votes[expID][dp]++
	}
	if len(votes) == 0 {
		fmt.Fprintln(stdout, "  no objects carry both AVUs — nothing to rebuild.")
		return nil
	}

	// Resolve each experiment's winning data_project + conflicts.
	elnClient, elnErr := openELN()
	if elnErr != nil {
		fmt.Fprintf(stdout, "  ⚠ ELN unavailable (%v) — study/project tiers will be left empty.\n", elnErr)
	}
	infoCache := map[string]*rebuiltEntry{}

	expIDs := make([]string, 0, len(votes))
	for expID := range votes {
		expIDs = append(expIDs, expID)
	}
	sort.Strings(expIDs)

	var entries []rebuiltEntry
	var conflicts []string
	for _, expID := range expIDs {
		dp, total, conflicted := winningDataProject(votes[expID])
		if conflicted && !force {
			conflicts = append(conflicts, fmt.Sprintf(
				"  ⚠ experiment %s tagged with multiple data_projects %v — skipped (use --force to take the most common)",
				expID, votes[expID]))
			continue
		}
		entry := rebuiltEntry{
			ExperimentID: expID,
			DataProject:  dp,
			ObjectCount:  total,
		}
		// Resolve study/project ids via the ELN (cached per experiment).
		if elnClient != nil {
			if cached, ok := infoCache[expID]; ok {
				entry.StudyID, entry.ProjectID, entry.ExpName = cached.StudyID, cached.ProjectID, cached.ExpName
			} else if parsed, perr := parseELNExperimentID(expID); perr == nil {
				if info, ierr := elnClient.ExpInfo(parsed); ierr == nil {
					entry.StudyID = elnStudyIDStr(info)
					entry.ProjectID = elnProjectIDStr(info)
					entry.ExpName = info.ExperimentName
				} else {
					fmt.Fprintf(stdout, "  ⚠ could not resolve experiment %s in ELN: %v\n", expID, ierr)
				}
				cp := entry
				infoCache[expID] = &cp
			}
		}
		entries = append(entries, entry)
	}

	// Report.
	fmt.Fprintf(stdout, "\n▶ reconstructed %d experiment→data_project mappings:\n", len(entries))
	for _, e := range entries {
		tier := "experiment-only"
		if e.StudyID != "" || e.ProjectID != "" {
			tier = fmt.Sprintf("study=%s project=%s", nonEmpty(e.StudyID), nonEmpty(e.ProjectID))
		}
		fmt.Fprintf(stdout, "  exp %-12s → %-24s [%d objs; %s]%s\n",
			e.ExperimentID, e.DataProject, e.ObjectCount, tier, nameSuffix(e.ExpName))
	}
	for _, c := range conflicts {
		fmt.Fprintln(stdout, c)
	}

	if !apply {
		fmt.Fprintf(stdout, "\n(dry-run — %d mappings NOT written; re-run with --apply to persist)\n", len(entries))
		return nil
	}

	// Persist.
	store, err := elnmap.Open("")
	if err != nil {
		return fmt.Errorf("open mapping store: %w", err)
	}
	written := 0
	for _, e := range entries {
		if err := store.RememberFull(e.ExperimentID, e.DataProject, e.ExpName, e.StudyID, e.ProjectID); err != nil {
			fmt.Fprintf(stdout, "  ⚠ could not store experiment %s: %v\n", e.ExperimentID, err)
			continue
		}
		written++
	}
	fmt.Fprintf(stdout, "\n✓ wrote %d mappings to the store.\n", written)
	return nil
}

// winningDataProject returns the most-voted data_project for an
// experiment, the total object count, and whether there was a conflict
// (more than one distinct data_project seen).
func winningDataProject(counts map[string]int) (winner string, total int, conflicted bool) {
	best := -1
	for dp, n := range counts {
		total += n
		if n > best {
			best, winner = n, dp
		}
	}
	return winner, total, len(counts) > 1
}

// queryPathValueByAVU returns path→value for every object carrying the
// given AVU attribute. Uses one genquery selecting the attribute's
// value alongside COLL_NAME/DATA_NAME.
func queryPathValueByAVU(ctx context.Context, client *iron.Client, attr string) (map[string]string, error) {
	sql := fmt.Sprintf(
		"select COLL_NAME, DATA_NAME, META_DATA_ATTR_VALUE where META_DATA_ATTR_NAME = '%s'",
		escapeGenquery(attr))
	raw, err := client.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		if strings.TrimSpace(raw) == "" {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("decode iron query result: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		path := strings.TrimRight(row[0], "/") + "/" + row[1]
		out[path] = row[2]
	}
	return out, nil
}

func nonEmpty(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func nameSuffix(name string) string {
	if name == "" {
		return ""
	}
	return " — " + name
}
