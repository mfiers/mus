package cli

// `mus eln disable` / `mus eln enable` + the requireELNDecision gate.
//
// Every folder mus operates on must be in one of three ELN states:
//
//   linked    — eln_experiment_id is present in the cascade
//   disabled  — eln_disabled is truthy in the cascade
//   undecided — neither
//
// `mus irods upload` (and the future `mus s3 upload`) refuse to run while
// undecided so the user has to make an explicit choice. `mus eln disable`
// records the "I don't use ELN here" answer; `mus eln link <id>` records
// "this folder is linked to experiment <id>".

import (
	"fmt"
	"strings"

	"codeberg.org/mfiers/mus/internal/config"
	"github.com/spf13/cobra"
)

// elnDisabled reports whether eln_disabled is set to a truthy value in the
// effective .env cascade. Truthy = anything matching the common yes/true/1
// set; everything else (including empty) is treated as false.
func elnDisabled(env *config.Env) bool {
	if env == nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(env.String("eln_disabled")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// requireELNDecision returns nil when the current folder has either a
// linked experiment (eln_experiment_id) or an explicit eln_disabled marker.
// Otherwise it returns an error listing the three remedies.
//
// Called at the top of every command that should not "silently work without
// ELN" — currently `mus irods upload`.
func requireELNDecision(env *config.Env) error {
	if env != nil && env.String("eln_experiment_id") != "" {
		return nil
	}
	if elnDisabled(env) {
		return nil
	}
	return fmt.Errorf("this folder has no ELN status declared. Choose one:\n" +
		"  1. `mus eln link <EXPERIMENT_ID>` — link this folder to an ELN experiment\n" +
		"  2. `mus eln disable`             — mark THIS folder as non-ELN (per-folder)\n" +
		"  3. `mus eln disable --global`    — disable ELN system-wide for your user\n" +
		"\n" +
		"Find an experiment ID in the eLab web UI: open the experiment, the digits\n" +
		"at the end of the URL are the ID (e.g. .../experiment/12345).")
}

// --- mus eln disable --------------------------------------------------------

func newELNDisableCmd() *cobra.Command {
	var global bool
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "mark this folder (or your user) as non-ELN",
		Long: "Writes eln_disabled=true to the local .env so `mus irods upload` and\n" +
			"`mus s3 upload` stop demanding an ELN linkage in this folder.\n\n" +
			"With --global, writes to ~/.config/mus/.env (XDG-aware) so the choice\n" +
			"applies anywhere on this user account — useful if you simply don't\n" +
			"have ELN access at all.\n\n" +
			"Either form is reversible via `mus eln enable [--global]`. Running\n" +
			"`mus eln link <ID>` in this folder also clears the local marker.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNDisable(cmd, global)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false,
		"write to ~/.config/mus/.env instead of the current folder")
	return cmd
}

func runELNDisable(cmd *cobra.Command, global bool) error {
	if global {
		path := config.GlobalPath()
		if path == "" {
			return fmt.Errorf("could not determine global config path (no $HOME / $XDG_CONFIG_HOME)")
		}
		if err := config.SaveToFile(path, map[string]string{"eln_disabled": "true"}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"eln_disabled=true written to %s\n"+
				"  mus will no longer demand ELN setup for any folder under this user.\n",
			path)
		return nil
	}
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	if err := config.Save(dir, map[string]string{"eln_disabled": "true"}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"eln_disabled=true written to %s/.env\n"+
			"  this folder will no longer demand ELN setup.\n"+
			"  run `mus eln enable` to undo, or `mus eln link <ID>` to link an experiment.\n",
		dir)
	return nil
}

// --- mus eln enable ---------------------------------------------------------

func newELNEnableCmd() *cobra.Command {
	var global bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "undo a previous `mus eln disable`",
		Long: "Removes the eln_disabled key from the local .env (or, with --global,\n" +
			"from ~/.config/mus/.env). After this, `mus irods upload` will again\n" +
			"demand a linkage (`mus eln link <ID>`) for this folder.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNEnable(cmd, global)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false,
		"clear ~/.config/mus/.env instead of the current folder's .env")
	return cmd
}

func runELNEnable(cmd *cobra.Command, global bool) error {
	if global {
		path := config.GlobalPath()
		if path == "" {
			return fmt.Errorf("could not determine global config path (no $HOME / $XDG_CONFIG_HOME)")
		}
		if err := config.UnsetFromFile(path, "eln_disabled"); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"removed eln_disabled from %s\n", path)
		return nil
	}
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	if err := config.Unset(dir, "eln_disabled"); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"removed eln_disabled from %s/.env (if it was set)\n", dir)
	return nil
}
