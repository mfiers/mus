package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("mus %s (commit %s, built %s, %s/%s with %s)\n",
				Version, Commit, BuildDate,
				runtime.GOOS, runtime.GOARCH, runtime.Version())
			return nil
		},
	}
}
