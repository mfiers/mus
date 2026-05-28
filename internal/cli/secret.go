package cli

import (
	"fmt"
	"os"
	"sort"

	"codeberg.org/mfiers/mus/internal/secret"
	"github.com/spf13/cobra"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "manage credentials (keyring or age-encrypted file)",
	}
	cmd.AddCommand(
		newSecretSetCmd(),
		newSecretGetCmd(),
		newSecretListCmd(),
		newSecretDeleteCmd(),
		newSecretBackendCmd(),
	)
	return cmd
}

func openSecrets() (secret.Store, error) {
	s, err := secret.Open()
	if err != nil {
		return nil, fmt.Errorf("open secret store: %w", err)
	}
	return s, nil
}

func newSecretSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set NAME [VALUE]",
		Short: "store a secret (reads stdin if VALUE omitted)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSecrets()
			if err != nil {
				return err
			}
			var value string
			if len(args) == 2 {
				value = args[1]
			} else {
				// read full stdin (allow newlines but strip trailing one)
				buf, err := os.ReadFile("/dev/stdin")
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				value = string(buf)
				if n := len(value); n > 0 && value[n-1] == '\n' {
					value = value[:n-1]
				}
			}
			return s.Set(args[0], value)
		},
	}
}

func newSecretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get NAME",
		Short: "print a stored secret to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSecrets()
			if err != nil {
				return err
			}
			v, err := s.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Println(v)
			return nil
		},
	}
}

func newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list secret names",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSecrets()
			if err != nil {
				return err
			}
			names, err := s.List()
			if err != nil {
				return err
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Println(n)
			}
			return nil
		},
	}
}

func newSecretDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete NAME",
		Aliases: []string{"del", "rm"},
		Short:   "remove a stored secret",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSecrets()
			if err != nil {
				return err
			}
			return s.Delete(args[0])
		},
	}
}

func newSecretBackendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backend",
		Short: "print active secret backend (keyring or age)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSecrets()
			if err != nil {
				return err
			}
			fmt.Println(s.Backend())
			return nil
		},
	}
}
