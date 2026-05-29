package cli

// `mus s3` subtree. Phase 1 shipped the validator under `mus h5ad
// check`; the actual S3 transport (`login` / `check` / `upload`) is
// Phase 2 and isn't wired yet. This file currently only exposes a
// helpful stub.

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newS3Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "s3",
		Short: "(planned) S3 deposit pipeline — Phase 2",
		Long: "S3 transport (`login` / `check` / `upload`) is Phase 2 and not\n" +
			"wired yet. Run `mus h5ad check FILE` for the standalone validator\n" +
			"(the sanity-check pipeline that S3 upload will gate on).",
	}
	cmd.AddCommand(newS3UploadStubCmd())
	return cmd
}

func newS3UploadStubCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upload FILE [FILE...]",
		Short: "(planned) upload h5ad files to S3 — Phase 2",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"mus s3 upload is not implemented yet (Phase 2).")
			fmt.Fprintln(cmd.ErrOrStderr(),
				"For now run `mus h5ad check FILE` to validate; uploads stay manual.")
			return fmt.Errorf("not implemented")
		},
	}
}
