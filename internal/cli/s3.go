package cli

// `mus s3` subtree — deposit h5ad files into a project-named S3
// bucket for downstream catalog (anthive) ingest.
//
// Three subcommands:
//
//   mus s3 login      — interactive wizard, stores endpoint + region
//                       + access_key + secret_key in mus's secret
//                       backend (keyring on a desktop, age-encrypted
//                       file on headless hosts).
//
//   mus s3 check      — verify the stored creds work + the
//                       per-project bucket exists / is reachable.
//
//   mus s3 upload     — gated upload. Refuses unless:
//                         - file is *.h5ad
//                         - sidecar has a fresh `mus h5ad check` PASS
//                         - data_project is set in the cascade
//                         - bucket named after data_project exists
//                         - -m / --message is provided
//                       Mirrors `mus irods upload`'s pattern.

import (
	"github.com/spf13/cobra"
)

func newS3Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "s3",
		Short: "deposit h5ad files to an S3 bucket (one bucket per data_project)",
		Long: "S3 deposit pipeline for h5ad files destined for the lab catalog\n" +
			"(anthive). Each data_project gets its own bucket (e.g. Thrup2020,\n" +
			"Fiers2025); files land at <bucket>/<basename>. Hard-gated on a\n" +
			"fresh `mus h5ad check` PASS — non-validating files are refused.\n\n" +
			"Configure once with `mus s3 login`. Endpoint, region, access key,\n" +
			"and secret key are stored in mus's secret backend.",
	}
	cmd.AddCommand(
		newS3LoginCmd(),
		newS3CheckCmd(),
		newS3UploadCmd(),
	)
	return cmd
}
