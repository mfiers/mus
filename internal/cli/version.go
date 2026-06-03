package cli

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/upgradecheck"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version information (and check Codeberg for a newer release)",
		RunE: func(cmd *cobra.Command, args []string) error {
			stdout := cmd.OutOrStdout()
			fmt.Fprintf(stdout, "mus %s (commit %s, built %s, %s/%s with %s)\n",
				Version, Commit, BuildDate,
				runtime.GOOS, runtime.GOARCH, runtime.Version())

			// Probe Codeberg for the current latest release. Bypasses the
			// 24h notifier cache — `mus version` should give the user
			// up-to-the-moment info, not a day-old summary. Tight 5s
			// timeout so a slow network doesn't make `mus version` hang.
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			st := upgradecheck.CheckNow(ctx, Version)
			switch st.Kind {
			case upgradecheck.StatusUpToDate:
				fmt.Fprintf(stdout, "  ✓ up to date (latest: %s)\n", st.LatestTag)
			case upgradecheck.StatusBehind:
				fmt.Fprintf(stdout,
					"  ⚠ update available: %s — run `mus upgrade`\n", st.LatestTag)
			case upgradecheck.StatusAhead:
				fmt.Fprintf(stdout,
					"  ↑ ahead of upstream (latest tagged release: %s)\n", st.LatestTag)
			case upgradecheck.StatusDevBuild:
				fmt.Fprintln(stdout, "  · local dev build — skipping release check")
			case upgradecheck.StatusUnknown:
				fmt.Fprintf(stdout, "  ? could not reach Codeberg (%v)\n", st.Err)
			}
			return nil
		},
	}
}
