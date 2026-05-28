package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"codeberg.org/atrxia/mus/internal/config"
	"codeberg.org/atrxia/mus/internal/dataproject"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "manage .mus TOML config (cascading)",
	}
	cmd.AddCommand(newConfigShowCmd(), newConfigSetCmd(), newConfigFilesCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var jsonOut bool
	var local bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "print effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			var env *config.Env
			if local {
				env, err = config.LoadLocal(dir)
			} else {
				env, err = config.Load(dir)
			}
			if err != nil {
				return err
			}
			all := env.All()
			if jsonOut {
				out, _ := json.MarshalIndent(all, "", "  ")
				fmt.Println(string(out))
				return nil
			}
			keys := make([]string, 0, len(all))
			for k := range all {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				switch v := all[k].(type) {
				case []string:
					fmt.Printf("%s\t%s\n", k, strings.Join(v, ", "))
				default:
					fmt.Printf("%s\t%v\n", k, v)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&jsonOut, "json", "j", false, "JSON output")
	cmd.Flags().BoolVarP(&local, "local", "l", false, "only show local .mus, not cascade")
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "set a key in the local .env",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			if err := validateConfigValue(key, value); err != nil {
				return err
			}
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			return config.Save(dir, map[string]string{key: value})
		},
	}
}

// validateConfigValue applies key-specific validation when writing to .env.
// Keys without a registered validator are accepted as-is.
func validateConfigValue(key, value string) error {
	switch key {
	case "data_project":
		return dataproject.ValidateName(value)
	}
	return nil
}

func newConfigFilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "files",
		Short: "list .mus files in the cascade (root first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			env, err := config.Load(dir)
			if err != nil {
				return err
			}
			for _, f := range env.Files() {
				fmt.Println(f)
			}
			return nil
		},
	}
}
