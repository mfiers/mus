package cli

// `mus completion install` writes a shell-completion script to the
// conventional location for the user's shell. Saves the user from looking
// up the right directory for each shell.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newCompletionInstallCmd attaches an `install` subcommand to the
// auto-generated `mus completion` group. cobra synthesises the
// `completion bash/zsh/fish/powershell` siblings; we add `install` for
// fire-and-forget setup.
func newCompletionInstallCmd(root *cobra.Command) *cobra.Command {
	var shell, dest string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "write a completion script to the conventional path for your shell",
		Long: "Auto-detects your shell from $SHELL (override with --shell), picks the\n" +
			"right XDG / dotfile location, and writes the completion script there.\n" +
			"Equivalent to running `mus completion <shell> > <path>` but without\n" +
			"having to remember the path.\n\n" +
			"Falls back to the parent of $0 if neither $XDG_*_HOME nor $HOME work.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompletionInstall(root, shell, dest, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "",
		"shell to install for (bash | zsh | fish); default: detect from $SHELL")
	cmd.Flags().StringVar(&dest, "dest", "",
		"explicit destination file (overrides the conventional path)")
	return cmd
}

func runCompletionInstall(root *cobra.Command, shellFlag, destFlag string, stdout interface{ Write([]byte) (int, error) }) error {
	sh := shellFlag
	if sh == "" {
		sh = detectShell()
	}
	if sh == "" {
		return fmt.Errorf("could not detect shell from $SHELL; pass --shell bash | zsh | fish")
	}
	sh = strings.ToLower(filepath.Base(sh))

	path := destFlag
	if path == "" {
		var err error
		path, err = completionPath(sh)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	// Generate the completion script.
	var genErr error
	switch sh {
	case "bash":
		genErr = root.GenBashCompletionFile(path)
	case "zsh":
		genErr = root.GenZshCompletionFile(path)
	case "fish":
		genErr = root.GenFishCompletionFile(path, true)
	default:
		return fmt.Errorf("unsupported shell %q (supported: bash, zsh, fish)", sh)
	}
	if genErr != nil {
		return fmt.Errorf("generating %s completion: %w", sh, genErr)
	}

	fmt.Fprintf(stdout, "✓ installed %s completion to %s\n", sh, path)
	switch sh {
	case "bash":
		fmt.Fprintf(stdout, "  Reload your shell, or run:  source %s\n", path)
	case "zsh":
		fmt.Fprintf(stdout,
			"  Reload your shell. If completions don't show up, ensure %s\n"+
				"  is in your $fpath (e.g. add to .zshrc: fpath=(%s $fpath) and run `compinit`).\n",
			filepath.Dir(path), filepath.Dir(path))
	case "fish":
		fmt.Fprintf(stdout, "  Fish reloads completions automatically.\n")
	}
	return nil
}

// detectShell returns the shell base name from $SHELL (bash, zsh, fish, …)
// or "" if it can't be determined.
func detectShell() string {
	s := os.Getenv("SHELL")
	if s == "" {
		return ""
	}
	return filepath.Base(s)
}

// completionPath returns the conventional install location for a shell's
// completion script for the running user. Honours XDG_DATA_HOME and
// XDG_CONFIG_HOME when set; otherwise falls back to ~/.local/share (bash,
// zsh) or ~/.config (fish).
func completionPath(sh string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch sh {
	case "bash":
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "bash-completion", "completions", "mus"), nil
	case "zsh":
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "zsh", "site-functions", "_mus"), nil
	case "fish":
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "fish", "completions", "mus.fish"), nil
	}
	return "", fmt.Errorf("unsupported shell %q", sh)
}
