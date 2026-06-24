// Package cli wires up the cobra command tree for `mus`.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/upgradecheck"
	"codeberg.org/mfiers/mus/internal/vocab"
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
		// Walk the cascade and auto-migrate any legacy `.env` that
		// contains mus-recognised keys → `mus.cfg`. Silent for dirs
		// where there's nothing to migrate; loud on stderr for each
		// dir that did get migrated (with secret/unknown warnings).
		if shouldAutoMigrate(cmd) {
			if cwd, err := os.Getwd(); err == nil {
				autoMigrateCascade(cwd, cmd.ErrOrStderr())
			}
		}
		// Best-effort background refresh of the local vocabulary
		// cache when it's stale (>24h) or missing. Fire-and-forget;
		// failures are silent. MUS_SKIP_VOCAB_REFRESH=1 opts out.
		vocab.BackgroundRefresh()
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
		newDECmd(),
		newS3Cmd(),
		newVocabCmd(),
		newUpgradeCmd(),
		newVerifyCmd(),
		newRmCmd(),
		newInstallSkillsCmd(),
		newServeCmd(),
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
	// Windows-only: clean up the .old backup left by a previous
	// `mus upgrade` (no-op everywhere else).
	sweepStaleUpgradeBackup()
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

// shouldAutoMigrate returns true unless cmd is one of the read-only
// / housekeeping subcommands where we don't want a side effect of
// rewriting files on the filesystem. Also opts out when
// MUS_NO_MIGRATE is set in the environment.
func shouldAutoMigrate(cmd *cobra.Command) bool {
	if os.Getenv("MUS_NO_MIGRATE") != "" {
		return false
	}
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "version", "verify", "completion", "upgrade", "migrate":
			return false
		}
	}
	return true
}

// autoMigrateCascade walks from startDir up to /, calling
// config.Migrate on every directory that has a legacy .env with
// mus-recognised keys (and no mus.cfg yet). Also covers the global
// ~/.config/mus/.env if present. All file IO is best-effort: errors
// on individual directories don't block the user's primary command.
func autoMigrateCascade(startDir string, stderr io.Writer) {
	cur := startDir
	for {
		runMigrateIfNeeded(cur, stderr)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// Global config dir.
	if gp := config.GlobalPathLegacy(); gp != "" {
		runMigrateIfNeeded(filepath.Dir(gp), stderr)
	}
}

// runMigrateIfNeeded is one cycle of the auto-migrate scan: stat
// for both files (cheap), then call Migrate only when there's a
// legacy file to consider AND no mus.cfg yet. Prints a report iff
// keys were actually moved.
func runMigrateIfNeeded(dir string, stderr io.Writer) {
	legacy := filepath.Join(dir, config.LegacyFileName)
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	target := filepath.Join(dir, config.FileName)
	if _, err := os.Stat(target); err == nil {
		return // mus.cfg already exists
	}
	report, err := config.Migrate(dir, isKnownConfigKey, false)
	if err != nil {
		fmt.Fprintf(stderr, "  note: legacy .env auto-migrate failed for %s: %v\n", dir, err)
		return
	}
	if report == nil || len(report.Migrated) == 0 {
		// Nothing mus-recognisable in this .env — leave it alone
		// silently. It's someone else's file.
		return
	}
	config.PrintMigrationReport(stderr, report)
}
