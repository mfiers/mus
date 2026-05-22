package cli

// `mus _verify BINARY SIG` is a hidden helper used by install.sh and by
// anyone scripting integrity checks. It reads BINARY's bytes, reads SIG, and
// verifies via the embedded ed25519 pubkey. Exit 0 = signature valid; non-zero
// otherwise (and a one-line diagnostic on stderr).
//
// Hidden (underscore prefix) because it is plumbing, not part of the user
// surface. Pairs with pika's `_sign` on the producer side.

import (
	"fmt"
	"os"

	"codeberg.org/atrxia/mus/internal/signing"
	"github.com/spf13/cobra"
)

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_verify BINARY SIG",
		Short:  "verify a file against an ed25519 signature using the embedded pubkey",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading %s: %w", args[0], err)
			}
			sig, err := os.ReadFile(args[1])
			if err != nil {
				return fmt.Errorf("reading %s: %w", args[1], err)
			}
			if err := signing.Verify(payload, sig); err != nil {
				return err
			}
			fmt.Println("signature OK")
			return nil
		},
	}
}
