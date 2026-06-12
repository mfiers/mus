package cli

// `mus s3 check` — verify that the stored S3 credentials work AND
// that the bucket named after this folder's data_project actually
// exists / is reachable. Both required for `mus s3 upload` to
// succeed; nicer to surface the failure here than mid-upload.

import (
	"fmt"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/s3client"
)

func newS3CheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "verify S3 credentials + per-data_project bucket reachability",
		Long: "Runs three probes:\n" +
			"  1. mus secret store has all four S3 fields\n" +
			"  2. those credentials authenticate against the endpoint\n" +
			"  3. a bucket named after the current cascade's data_project\n" +
			"     exists and is HEAD-able\n\n" +
			"Failure of any probe prints a remediation hint and exits non-zero.",
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout := cmd.OutOrStdout()
			store, err := openSecrets()
			if err != nil {
				return err
			}
			cfg, err := loadS3Config(store)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "✓ stored credentials for %s\n", cfg.Endpoint)

			client, err := s3client.New(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			buckets, err := client.Ping(cmd.Context())
			if err != nil {
				return fmt.Errorf("ping failed: %w\n  Re-run `mus s3 login` to fix.", err)
			}
			fmt.Fprintf(stdout, "✓ authenticated (%d bucket(s) visible)\n", len(buckets))

			// Per-folder bucket check.
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			env, err := config.Load(dir)
			if err != nil {
				return err
			}
			dataProject := env.String("data_project")
			if dataProject == "" {
				fmt.Fprintln(stdout,
					"  (no data_project in the local cascade — skipping bucket-existence check)")
				return nil
			}
			exists, err := client.BucketExists(cmd.Context(), dataProject)
			if err != nil {
				return fmt.Errorf("HeadBucket %s: %w", dataProject, err)
			}
			if !exists {
				return fmt.Errorf(
					"bucket %q does not exist (or you have no access).\n"+
						"  Create it on your S3 provider, then re-run `mus s3 check`.",
					dataProject)
			}
			fmt.Fprintf(stdout, "✓ bucket %s exists and is reachable\n", dataProject)
			return nil
		},
	}
}
