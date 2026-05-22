// Command mus is the entrypoint for the mus CLI.
package main

import (
	"os"

	"codeberg.org/atrxia/mus/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}
