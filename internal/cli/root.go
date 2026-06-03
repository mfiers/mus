// Package cli wires up the cobra command tree for `mus`.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/upgradecheck"
)

// Build-time variables, populated via -ldflags. See Makefile.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// NewRootCmd constructs the top-level command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mus",
		Short: "research data management CLI",
		Long: "mus — track local research-data files (.mus TOML sidecars + cascading\n" +
			"folder config), verify integrity, and sync to iRODS (via IRON) and ELN.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().BoolP("verbose", "v", false, "verbose logging")
	root.PersistentFlags().StringP("dir", "C", "", "run as if invoked from this directory (like git -C)")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		if dir != "" {
			if err := os.Chdir(dir); err != nil {
				return fmt.Errorf("-C %s: %w", dir, err)
			}
		}
		// One-line-per-day notice if Codeberg shows a newer release.
		// Skipped for `mus upgrade` itself (it does its own version
		// check) and `mus version` (terse output, no noise). All
		// errors are absorbed silently — see upgradecheck.Notify.
		if shouldRunUpgradeCheck(cmd) {
			upgradecheck.Notify(context.Background(), Version, cmd.ErrOrStderr())
		}
		return nil
	}

	root.AddCommand(
		newVersionCmd(),
		newConfigCmd(),
		newSecretCmd(),
		newTagCmd(),
		newCheckCmd(),
		newELNCmd(),
		newIRODSCmd(),
		newH5adCmd(),
		newS3Cmd(),
		newVocabCmd(),
		newUpgradeCmd(),
		newVerifyCmd(),
		newRmCmd(),
	)
	// cobra creates the `completion` subcommand lazily during Execute();
	// force it to exist now so we can attach `install` under it.
	root.InitDefaultCompletionCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "completion" {
			sub.AddCommand(newCompletionInstallCmd(root))
			break
		}
	}
	return root
}

// Run executes the CLI with os.Args. Returns a process exit code.
func Run() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mus: "+err.Error())
		return 1
	}
	return 0
}

// workingDir returns the current process cwd. After PersistentPreRunE has run,
// this already reflects any `-C` override.
func workingDir(cmd *cobra.Command) (string, error) {
	return os.Getwd()
}

// shouldRunUpgradeCheck returns true unless cmd is one of the
// subcommands where the once-per-day "new release available" notice
// would be redundant or unwanted.
func shouldRunUpgradeCheck(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "upgrade", "version", "verify", "completion":
			return false
		}
	}
	return true
}
