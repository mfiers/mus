// Command mus is the entrypoint for the mus CLI.
package main

import (
	"os"

	"github.com/mfiers/mus/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}
