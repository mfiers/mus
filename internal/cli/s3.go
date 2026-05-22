package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// S3 sync (for .h5ad and similar) is planned. Stubbed for now so the command
// shape is locked in and downstream scripts can be authored against it.
func newS3Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "s3",
		Short: "sync to S3 (planned — not yet implemented)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "upload FILE [FILE...]",
		Short: "upload .h5ad files to the lab S3 bucket",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("not implemented: see roadmap in CLAUDE.md")
		},
	})
	return cmd
}
