package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// findSubcommand resolves a space-separated command path (e.g. "irods check")
// against the real root command tree, returning the leaf *cobra.Command so the
// test exercises the actual wiring rather than a hand-built stand-in.
func findSubcommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cmd := NewRootCmd()
	for _, name := range path {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				cmd = sub
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("subcommand %q not found under %q", name, cmd.Name())
		}
	}
	return cmd
}

// Read-only / inspection commands must NOT trigger the .env -> mus.cfg
// auto-migration, because that rewrites (and can delete) files in the user's
// data tree. Regression for the attic bug: `mus irods check` mutated a .env.
func TestShouldAutoMigrate_ReadOnlyCommandsOptOut(t *testing.T) {
	readOnly := [][]string{
		{"check"},
		{"irods", "check"},
		{"irods", "find"},
		{"irods", "get"},
		{"irods", "scan"},
		{"irods", "projects"},
		{"irods", "project"},
		{"config", "show"},
		{"config", "files"},
		{"eln", "whoami"},
		{"version"},
		{"vocab", "show"},
		{"secret", "list"},
	}
	for _, path := range readOnly {
		t.Run(joinPath(path), func(t *testing.T) {
			cmd := findSubcommand(t, path...)
			if shouldAutoMigrate(cmd) {
				t.Errorf("shouldAutoMigrate(%q) = true, want false (read-only must not mutate the data tree)", joinPath(path))
			}
		})
	}
}

// Commands that already write config or sidecars in the data tree SHOULD still
// migrate a legacy .env as part of that write.
func TestShouldAutoMigrate_WritingCommandsOptIn(t *testing.T) {
	writing := [][]string{
		{"tag"},
		{"irods", "upload"},
		{"config", "set"},
		{"config", "unset"},
		{"eln", "link"},
	}
	for _, path := range writing {
		t.Run(joinPath(path), func(t *testing.T) {
			cmd := findSubcommand(t, path...)
			if !shouldAutoMigrate(cmd) {
				t.Errorf("shouldAutoMigrate(%q) = false, want true (writing command should migrate)", joinPath(path))
			}
		})
	}
}

// MUS_NO_MIGRATE always wins, even for a writing command.
func TestShouldAutoMigrate_EnvOptOutWins(t *testing.T) {
	t.Setenv("MUS_NO_MIGRATE", "1")
	cmd := findSubcommand(t, "tag")
	if shouldAutoMigrate(cmd) {
		t.Error("MUS_NO_MIGRATE=1 must disable auto-migration even for writing commands")
	}
}

func joinPath(path []string) string {
	out := "mus"
	for _, p := range path {
		out += " " + p
	}
	return out
}
