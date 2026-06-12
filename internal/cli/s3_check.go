package cli

// `mus s3 check` — verify that the stored S3 credentials work AND
// that the configured bucket is reachable. Nice failure modes:
// surfaces credential / bucket-permission problems here rather than
// mid-upload.

import (
	"fmt"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/s3client"
)

func newS3CheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "verify S3 credentials + configured-bucket reachability",
		Long: "Runs three probes:\n" +
			"  1. mus secret store has all five S3 fields\n" +
			"  2. those credentials authenticate against the endpoint\n" +
			"  3. the configured bucket exists and is HEAD-able\n\n" +
			"Failure of any probe prints a remediation hint and exits non-zero.\n\n" +
			"Mus uploads always go to s3://<bucket>/<data_project>/<basename>.\n" +
			"`mus s3 check` doesn't validate per-cwd state (data_project) — that\n" +
			"happens at upload time. It only verifies the connection layer.",
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
			fmt.Fprintf(stdout, "  bucket=%s region=%s\n", cfg.Bucket, cfg.Region)

			client, err := s3client.New(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if _, err := client.Ping(cmd.Context()); err != nil {
				return fmt.Errorf("ping failed: %w\n  Re-run `mus s3 login` to fix.", err)
			}
			fmt.Fprintln(stdout, "✓ authenticated")

			exists, err := client.BucketExists(cmd.Context(), cfg.Bucket)
			if err != nil {
				return fmt.Errorf("HeadBucket %s: %w", cfg.Bucket, err)
			}
			if !exists {
				return fmt.Errorf(
					"bucket %q does not exist (or you have no access).\n"+
						"  Create it on your S3 provider, then re-run `mus s3 check`.",
					cfg.Bucket)
			}
			fmt.Fprintf(stdout, "✓ bucket %s exists and is reachable\n", cfg.Bucket)
			return nil
		},
	}
}
