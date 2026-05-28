package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
)

func TestRequireELNDecision(t *testing.T) {
	tmp := t.TempDir()
	// Empty cascade (no .env anywhere reachable from tmp) under a fake HOME
	// → undecided.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	env, err := config.Load(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireELNDecision(env); err == nil {
		t.Fatal("expected error for undecided folder, got nil")
	} else if !strings.Contains(err.Error(), "no ELN status declared") {
		t.Fatalf("expected helpful error, got: %v", err)
	}
}

func TestRequireELNDecisionPassesWhenLinked(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	if err := config.Save(tmp, map[string]string{"eln_experiment_id": "12345"}); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireELNDecision(env); err != nil {
		t.Errorf("linked folder should pass, got: %v", err)
	}
}

func TestRequireELNDecisionPassesWhenDisabled(t *testing.T) {
	for _, val := range []string{"true", "True", "1", "yes", "on"} {
		t.Run(val, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
			if err := config.Save(tmp, map[string]string{"eln_disabled": val}); err != nil {
				t.Fatal(err)
			}
			env, err := config.Load(tmp)
			if err != nil {
				t.Fatal(err)
			}
			if err := requireELNDecision(env); err != nil {
				t.Errorf("disabled (%s) should pass, got: %v", val, err)
			}
		})
	}
}

func TestRequireELNDecisionFalsyDisabled(t *testing.T) {
	for _, val := range []string{"false", "0", "no", "off", ""} {
		t.Run("falsy="+val, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
			if val != "" {
				if err := config.Save(tmp, map[string]string{"eln_disabled": val}); err != nil {
					t.Fatal(err)
				}
			}
			env, err := config.Load(tmp)
			if err != nil {
				t.Fatal(err)
			}
			if err := requireELNDecision(env); err == nil {
				t.Errorf("falsy eln_disabled=%q should still trigger undecided error", val)
			}
		})
	}
}

func TestRequireELNDecisionGlobalDisablePropagates(t *testing.T) {
	// A global eln_disabled=true should satisfy the gate from any working dir.
	tmp := t.TempDir()
	xdg := filepath.Join(tmp, "xdg")
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	globalPath := config.GlobalPath()
	if !strings.HasPrefix(globalPath, xdg) {
		t.Fatalf("GlobalPath %q does not honour XDG_CONFIG_HOME %q", globalPath, xdg)
	}
	if err := config.SaveToFile(globalPath, map[string]string{"eln_disabled": "true"}); err != nil {
		t.Fatal(err)
	}

	// Make a totally unrelated working dir (outside $HOME).
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireELNDecision(env); err != nil {
		t.Errorf("global eln_disabled should satisfy gate from anywhere, got: %v", err)
	}
}
