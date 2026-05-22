package cli

import (
	"fmt"
	"strconv"

	"github.com/mfiers/mus/internal/config"
	"github.com/mfiers/mus/internal/eln"
	"github.com/mfiers/mus/internal/secret"
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
	var expID int64
	cmd := &cobra.Command{
		Use:   "tag-folder",
		Short: "link the current folder to an ELN experiment (writes .mus)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if expID == 0 {
				return fmt.Errorf("-x/--experiment-id is required")
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
	cmd.Flags().Int64VarP(&expID, "experiment-id", "x", 0, "ELN experiment ID")
	_ = cmd.MarkFlagRequired("experiment-id")
	return cmd
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
