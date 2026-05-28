// Command mus is the entrypoint for the mus CLI.
package main

import (
	"os"

	"codeberg.org/mfiers/mus/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}
