package cli

// `mus irods login` walks a new user through getting `iron` authenticated.
// The actual auth handoff goes through `iron auth` interactively (PAM
// password or KU Leuven SSO); mus just bridges the discovery + verification
// around it.
//
// Sequence:
//
//  1. Locate the `iron` binary on PATH; if missing, print install link.
//  2. Check ~/.irods/irods_environment.json. If missing, print the URL
//     where the user downloads it from the Mango portal.
//  3. Probe current auth state with `iron pwd`. If already good, exit ✓.
//  4. Run `iron auth` interactively (passes stdin/stdout through).
//  5. Re-probe with `iron pwd`. Report success.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"codeberg.org/mfiers/mus/internal/iron"
	"github.com/spf13/cobra"
)

func newIRODSLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "interactive: get the `iron` CLI authenticated against your iRODS zone",
		Long: "Bridges `iron auth` so first-time users don't have to remember the\n" +
			"steps. Confirms the iron binary is installed, points you at the Mango\n" +
			"portal to download your irods_environment.json if missing, runs\n" +
			"`iron auth` for the actual credential exchange, then verifies with\n" +
			"`iron pwd`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSLogin(cmd)
		},
	}
}

func runIRODSLogin(cmd *cobra.Command) error {
	stdout := cmd.OutOrStdout()

	// Step 1: iron on PATH?
	ironPath, err := exec.LookPath("iron")
	if err != nil {
		return fmt.Errorf(
			"the `iron` CLI is not on PATH.\n" +
				"  Install it from: https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html\n" +
				"  (one binary download per platform).")
	}
	fmt.Fprintf(stdout, "✓ iron found at %s\n", ironPath)

	// Step 2: iRODS env file present?
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	envFile := filepath.Join(home, ".irods", "irods_environment.json")
	if _, err := os.Stat(envFile); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"iRODS environment file is missing: %s\n"+
					"  Download yours from https://mango.kuleuven.be/\n"+
					"  (top-right user menu → \"Download iRODS environment\")\n"+
					"  Save it as ~/.irods/irods_environment.json, then re-run `mus irods login`.",
				envFile)
		}
		return err
	}
	fmt.Fprintf(stdout, "✓ iRODS environment file present (%s)\n", envFile)

	client, err := iron.New()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Step 3: already authenticated?
	if pwd, perr := client.Run(ctx, "pwd"); perr == nil {
		fmt.Fprintf(stdout, "✓ already authenticated; iron pwd = %s\n", trimRight(pwd, '\n'))
		return nil
	}

	// Step 4: run `iron auth` interactively. We invoke it through Stream so
	// stdin/stdout/stderr pass through to the user's terminal (so PAM prompts
	// work).
	fmt.Fprintln(stdout, "→ running `iron auth` — follow the prompt to enter your password or SSO credential")
	if err := client.Stream(ctx, "auth"); err != nil {
		return fmt.Errorf("iron auth: %w", err)
	}

	// Step 5: verify
	pwd, err := client.Run(ctx, "pwd")
	if err != nil {
		return fmt.Errorf("iron auth completed but `iron pwd` still fails: %w", err)
	}
	fmt.Fprintf(stdout, "\n✓ authenticated.  iron pwd = %s\n",
		trimRight(pwd, '\n'))
	fmt.Fprintln(stdout, "  You can now run `mus irods upload`, `mus irods get`, etc.")
	return nil
}

func trimRight(s string, b byte) string {
	for len(s) > 0 && s[len(s)-1] == b {
		s = s[:len(s)-1]
	}
	return s
}
