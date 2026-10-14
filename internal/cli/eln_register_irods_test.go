package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func regIrodsCmd() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader(""))
	return c, &out
}

// Missing sidecar → clear error, no ELN call.
func TestRegisterIRODS_NoSidecar(t *testing.T) {
	cmd, _ := regIrodsCmd()
	err := runELNRegisterIRODS(cmd, filepath.Join(t.TempDir(), "nope.mus"), false)
	// A missing sidecar surfaces as a read error — the point is it fails
	// clearly and reaches no ELN call.
	if err == nil {
		t.Fatal("want an error for a missing sidecar, got nil")
	}
}

// Sidecar without irods_path → refuse (not an iRODS upload).
func TestRegisterIRODS_NoIrodsPath(t *testing.T) {
	dir := t.TempDir()
	sc := filepath.Join(dir, "x.mus")
	os.WriteFile(sc, []byte("eln_experiment_id=2004560\nversion=1\n"), 0o644)
	cmd, _ := regIrodsCmd()
	err := runELNRegisterIRODS(cmd, sc, false)
	if err == nil || !strings.Contains(err.Error(), "irods_path") {
		t.Fatalf("want no-irods_path error, got %v", err)
	}
}

// Sidecar without eln_experiment_id → refuse before any ELN call.
func TestRegisterIRODS_NoExperiment(t *testing.T) {
	dir := t.TempDir()
	sc := filepath.Join(dir, "x.mus")
	os.WriteFile(sc, []byte("irods_path=/z/x.tar.gz\nkind=folder\nversion=1\n"), 0o644)
	cmd, _ := regIrodsCmd()
	err := runELNRegisterIRODS(cmd, sc, false)
	if err == nil || !strings.Contains(err.Error(), "eln_experiment_id") {
		t.Fatalf("want no-experiment error, got %v", err)
	}
}

// Idempotency guard: already-registered sidecar refuses without --force.
func TestRegisterIRODS_GuardAlreadyRegistered(t *testing.T) {
	dir := t.TempDir()
	sc := filepath.Join(dir, "x.mus")
	os.WriteFile(sc, []byte(
		"irods_path=/z/x.tar.gz\nkind=folder\neln_experiment_id=2004560\neln_sample_id=12345\nversion=1\n"),
		0o644)
	cmd, _ := regIrodsCmd()
	err := runELNRegisterIRODS(cmd, sc, false)
	if err == nil || !strings.Contains(err.Error(), "already records eln_sample_id") {
		t.Fatalf("want already-registered guard, got %v", err)
	}
}
