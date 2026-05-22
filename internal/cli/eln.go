package cli

import (
	"fmt"
	"strconv"

	"codeberg.org/atrxia/mus/internal/config"
	"codeberg.org/atrxia/mus/internal/eln"
	"codeberg.org/atrxia/mus/internal/secret"
	"github.com/spf13/cobra"
)

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
		Short: "link the current folder to an ELN experiment (writes .mus)",
		RunE: func(cmd *cobra.Command, args []string) error {
			expID, err := parseELNExperimentID(expIDStr)
			if err != nil {
				return err
			}
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			client, err := openELN()
			if err != nil {
				return err
			}
			info, err := client.ExpInfo(expID)
			if err != nil {
				return err
			}
			kv := map[string]string{
				"eln.experiment_id":   strconv.FormatInt(info.ExperimentID, 10),
				"eln.experiment_name": info.ExperimentName,
				"eln.study_id":        strconv.FormatInt(info.StudyID, 10),
				"eln.study_name":      info.StudyName,
				"eln.project_id":      strconv.FormatInt(info.ProjectID, 10),
				"eln.project_name":    info.ProjectName,
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
		Short: "refresh ELN metadata in local .mus from the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			env, err := config.Load(dir)
			if err != nil {
				return err
			}
			idStr := env.String("eln.experiment_id")
			if idStr == "" {
				return fmt.Errorf("no eln.experiment_id in .mus cascade — run `mus eln tag-folder -x ID` first")
			}
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid eln.experiment_id %q: %w", idStr, err)
			}
			client, err := openELN()
			if err != nil {
				return err
			}
			info, err := client.ExpInfo(id)
			if err != nil {
				return err
			}
			return config.Save(dir, map[string]string{
				"eln.experiment_id":   strconv.FormatInt(info.ExperimentID, 10),
				"eln.experiment_name": info.ExperimentName,
				"eln.study_id":        strconv.FormatInt(info.StudyID, 10),
				"eln.study_name":      info.StudyName,
				"eln.project_id":      strconv.FormatInt(info.ProjectID, 10),
				"eln.project_name":    info.ProjectName,
			})
		},
	}
}

func openELN() (*eln.Client, error) {
	s, err := secret.Open()
	if err != nil {
		return nil, err
	}
	url, err := s.Get("eln_url")
	if err != nil {
		return nil, fmt.Errorf("eln_url not in secret store — `mus secret set eln_url <URL>`")
	}
	key, err := s.Get("eln_apikey")
	if err != nil {
		return nil, fmt.Errorf("eln_apikey not in secret store — `mus secret set eln_apikey <KEY>`")
	}
	return eln.New(url, key), nil
}
