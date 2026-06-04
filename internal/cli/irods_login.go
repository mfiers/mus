package cli

// `mus irods login` is a verify-or-nudge helper for `iron auth`. mus
// does NOT bootstrap an iRODS environment itself — `iron auth` already
// does that in one command. mus just probes the current state and, if
// auth is missing, points the user at the canonical iron command.
//
// Sequence:
//
//  1. Locate the `iron` binary on PATH; if missing, print install link.
//  2. Probe current auth state with `iron pwd`. If it works, ✓ done.
//  3. Otherwise: print the `iron auth <u-or-r-number> gbiomed
//     gbiomed.irods.icts.kuleuven.be` command and exit, asking the
//     user to run it (it prompts for password / SSO and writes the
//     environment file). Re-run `mus irods login` to verify.

import (
	"context"
	"fmt"
	"os/exec"

	"codeberg.org/mfiers/mus/internal/iron"
	"github.com/spf13/cobra"
)

func newIRODSLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "verify iRODS authentication, or nudge you to run `iron auth`",
		Long: "Probes `iron pwd` to confirm you're authenticated against the\n" +
			"iRODS server. If you are: prints ✓ and exits. If not: prints the\n" +
			"canonical `iron auth` command (with the gbiomed zone + host filled\n" +
			"in) so you can run it yourself.\n\n" +
			"mus does NOT bootstrap auth itself — `iron auth` already handles\n" +
			"that in one command, including writing the environment file.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSLogin(cmd)
		},
	}
}

// IronAuthHint is the canonical command users should run to get
// authenticated against the BADS / KU Leuven Mango zone. Surfaced
// here so other commands (e.g. `mus irods upload` on a stale token)
// can show the same string.
const IronAuthHint = "iron auth <u-or-r-number> gbiomed gbiomed.irods.icts.kuleuven.be"

func runIRODSLogin(cmd *cobra.Command) error {
	stdout := cmd.OutOrStdout()

	ironPath, err := exec.LookPath("iron")
	if err != nil {
		return fmt.Errorf(
			"the `iron` CLI is not on PATH.\n" +
				"  Install it from: https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html\n" +
				"  (one binary download per platform).")
	}
	fmt.Fprintf(stdout, "✓ iron found at %s\n", ironPath)

	client, err := iron.New()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Already authenticated? Confirm and exit.
	if pwd, perr := client.Run(ctx, "pwd"); perr == nil {
		fmt.Fprintf(stdout, "✓ authenticated; iron pwd = %s\n",
			trimRight(pwd, '\n'))
		fmt.Fprintln(stdout, "  You can now run `mus irods upload`, `mus irods get`, etc.")
		return nil
	}

	// Not authenticated. Tell the user exactly what to run.
	return fmt.Errorf(
		"not authenticated against iRODS.\n"+
			"  Run:\n"+
			"      %s\n"+
			"  (substitute your KU Leuven account number for <u-or-r-number>).\n"+
			"  It prompts for your password / SSO and writes the environment\n"+
			"  file. Then re-run `mus irods login` to verify.",
		IronAuthHint)
}

func trimRight(s string, b byte) string {
	for len(s) > 0 && s[len(s)-1] == b {
		s = s[:len(s)-1]
	}
	return s
}
