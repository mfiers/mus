package pikahook

// Tests for pikahook.Emit — verify silent no-op on every disabled /
// failed path, and verify the JSON payload shape when emission DOES
// fire. The stubbed `pika` binary writes its stdin to a known file
// so the test can re-parse it and assert on structure.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// stubPika writes a fake `pika` shell script to a temp dir and
// returns its directory (which the caller prepends to PATH). The
// stub copies its stdin to <dir>/captured.json so the test can
// re-parse it.
func stubPika(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "pika")
	script := fmt.Sprintf(`#!/usr/bin/env bash
cat > %q
`, filepath.Join(dir, "captured.json"))
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// readCaptured reads the JSON the stub captured to disk, or returns
// (nil, false) when no call was made.
func readCaptured(t *testing.T, stubDir string) (map[string]any, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stubDir, "captured.json"))
	if err != nil {
		return nil, false
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("captured.json is not valid JSON: %v\n%s", err, raw)
	}
	return got, true
}

// withConfig writes a .env to a fresh working dir and returns its
// path. dir = workingDir of the mus invocation.
func withConfig(t *testing.T, kv map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if len(kv) > 0 {
		var body string
		for k, v := range kv {
			body += k + "=" + v + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestEmit_SilentWhenNotEnabled(t *testing.T) {
	// .env has no pika_log_enabled → must NOT call pika even though
	// the stub is on PATH. Stub records its invocation; absence of
	// captured.json proves no call was made.
	stubDir := stubPika(t)
	t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))

	dir := withConfig(t, map[string]string{"data_project": "Test2026"})
	if err := Emit(context.Background(), dir, "tag", map[string]string{"path": "x"}); err != nil {
		t.Fatalf("Emit returned err: %v", err)
	}
	if _, ok := readCaptured(t, stubDir); ok {
		t.Error("pika was called despite pika_log_enabled not being set")
	}
}

func TestEmit_SilentWhenBinaryMissing(t *testing.T) {
	// pika_log_enabled=true but `pika` not on PATH. Must still be a
	// silent no-op (the hard constraint).
	t.Setenv("PATH", t.TempDir()) // an empty dir
	dir := withConfig(t, map[string]string{"pika_log_enabled": "true"})
	if err := Emit(context.Background(), dir, "tag", nil); err != nil {
		t.Errorf("Emit returned err when binary missing: %v", err)
	}
}

func TestEmit_SilentWhenBinaryExitsNonZero(t *testing.T) {
	// A pika binary that always fails. Emit must still return nil.
	dir := t.TempDir()
	failingPika := filepath.Join(dir, "pika")
	if err := os.WriteFile(failingPika, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	cfgDir := withConfig(t, map[string]string{"pika_log_enabled": "true"})
	if err := Emit(context.Background(), cfgDir, "tag", nil); err != nil {
		t.Errorf("Emit must swallow failing-binary errors, got: %v", err)
	}
}

func TestEmit_CallsBinaryWhenEnabledAndAvailable(t *testing.T) {
	stubDir := stubPika(t)
	t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))
	cfgDir := withConfig(t, map[string]string{"pika_log_enabled": "true"})

	err := Emit(context.Background(), cfgDir, "irods-upload",
		map[string]string{
			"path":   "data/cells.h5ad",
			"remote": "/zone/proj/cells.h5ad",
			"sha256": "abc123",
		})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := readCaptured(t, stubDir)
	if !ok {
		t.Fatal("pika was not called — expected an emission")
	}
	if got["type"] != "mus-irods-upload" {
		t.Errorf("type = %v, want mus-irods-upload", got["type"])
	}
	meta, _ := got["meta"].(map[string]any)
	if meta["path"] != "data/cells.h5ad" {
		t.Errorf("meta.path = %v", meta["path"])
	}
	if meta["remote"] != "/zone/proj/cells.h5ad" {
		t.Errorf("meta.remote = %v", meta["remote"])
	}
	if meta["sha256"] != "abc123" {
		t.Errorf("meta.sha256 = %v", meta["sha256"])
	}
}

func TestEmit_AcceptedEnabledValues(t *testing.T) {
	// `true`, `1`, `yes`, `on` (case-insensitive) all turn it on.
	// Anything else (including bogus values) is treated as off.
	for _, v := range []string{"true", "TRUE", "1", "yes", "on", "True"} {
		stubDir := stubPika(t)
		t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))
		cfgDir := withConfig(t, map[string]string{"pika_log_enabled": v})
		_ = Emit(context.Background(), cfgDir, "tag", nil)
		if _, ok := readCaptured(t, stubDir); !ok {
			t.Errorf("value %q should enable emission", v)
		}
	}
	for _, v := range []string{"false", "0", "", "maybe"} {
		stubDir := stubPika(t)
		t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))
		cfgDir := withConfig(t, map[string]string{"pika_log_enabled": v})
		_ = Emit(context.Background(), cfgDir, "tag", nil)
		if _, ok := readCaptured(t, stubDir); ok {
			t.Errorf("value %q should NOT enable emission", v)
		}
	}
}

func TestBuildPayload_FallbackMessage(t *testing.T) {
	// Caller didn't pass a "message" field — buildPayload composes a
	// human-readable one-liner from path / remote / error.
	p := buildPayload("irods-upload", map[string]string{
		"path":   "data.h5ad",
		"remote": "/zone/p/data.h5ad",
	})
	if msg := p["message"]; msg != "irods-upload data.h5ad → /zone/p/data.h5ad" {
		t.Errorf("fallback message = %q", msg)
	}
}

func TestBuildPayload_ExplicitMessageWins(t *testing.T) {
	p := buildPayload("tag", map[string]string{
		"message": "user-supplied note",
		"path":    "foo.csv",
	})
	if msg := p["message"]; msg != "user-supplied note" {
		t.Errorf("explicit message lost: %q", msg)
	}
}
