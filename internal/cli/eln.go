package cli

import (
	"fmt"
	"strconv"

	"codeberg.org/atrxia/mus/internal/config"
	"github.com/spf13/cobra"
)

// NOTE: the eln HTTP client lives in internal/eln/ and is fully tested, but
// it is NOT wired into the CLI right now. The eLabNext API endpoint location
// is currently unsettled (legacy elabjournal.com/doc deprecated; new
// developer.elabnext.com restructured — see top-level CLAUDE.md). When the
// API is reachable again, restore openELN() and re-enable the ExpInfo call
// path in newELNUpdateCmd / newELNTagFolderCmd.

func newELNCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eln",
		Short: "eLabJournal integration",
	}
	cmd.AddCommand(newELNTagFolderCmd(), newELNUpdateCmd())
	return cmd
}

func newELNTagFolderCmd() *cobra.Command {
	// Flag is a string, not Int64, because cobra's Int64Var parses with
	// strconv.ParseInt(s, 0, 64) — base-0 means a leading "0" is interpreted
	// as octal, which breaks on eLabJournal's long-form IDs that copy-paste
	// in with leading zeros (e.g. "001000000001303549" contains a "9", which
	// is not a valid octal digit). We parse with base 10 explicitly.
	var expIDStr string
	cmd := &cobra.Command{
		Use:   "tag-folder",
		Short: "record an ELN experiment ID for the current folder",
		Long: "Writes eln_experiment_id into the local .mus so subsequent commands\n" +
			"(notably `mus irods upload`) can stamp sidecars with the experiment ID\n" +
			"and pick a stable remote subfolder (exp_<id>).\n\n" +
			"This command does NOT contact the ELN API — the eLabNext API endpoint\n" +
			"is currently unsettled (see CLAUDE.md). When/if ELN is reachable again,\n" +
			"`mus eln update` will be re-enabled to enrich .mus with project/study/\n" +
			"experiment names.",
		RunE: func(cmd *cobra.Command, args []string) error {
			expID, err := parseELNExperimentID(expIDStr)
			if err != nil {
				return err
			}
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			kv := map[string]string{
				"eln_experiment_id": strconv.FormatInt(expID, 10),
			}
			for k, v := range kv {
				fmt.Printf("%-25s : %s\n", k, v)
			}
			return config.Save(dir, kv)
		},
	}
	cmd.Flags().StringVarP(&expIDStr, "experiment-id", "x", "", "ELN experiment ID (digits; leading zeros tolerated)")
	_ = cmd.MarkFlagRequired("experiment-id")
	return cmd
}

// parseELNExperimentID accepts a digit string and returns an int64. Leading
// zeros are stripped — eLabJournal's web UI occasionally copy-pastes IDs with
// extra leading zeros. Returns an error for anything that isn't a non-empty
// run of decimal digits.
func parseELNExperimentID(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("-x/--experiment-id is required")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("experiment ID %q contains non-digit %q", raw, string(r))
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("experiment ID %q: %w", raw, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("experiment ID must be positive, got %d", id)
	}
	return id, nil
}

func newELNUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "(disabled) would refresh ELN metadata from the server",
		Long: "Disabled. The eLabNext API endpoint location is currently unsettled\n" +
			"(elabjournal.com/doc deprecated, developer.elabnext.com restructured —\n" +
			"see CLAUDE.md). Re-enable this command by restoring the call to\n" +
			"eln.Client.ExpInfo and adjusting auth headers to match whatever the\n" +
			"new API expects (Authorization vs api_key).\n\n" +
			"The eln client code in internal/eln/ is intact and tested; only the\n" +
			"CLI wiring is removed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("`mus eln update` is currently disabled — see `mus eln update --help`")
		},
	}
}
