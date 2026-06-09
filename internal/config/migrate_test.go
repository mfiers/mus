package config

// Tests for the legacy .env → mus.cfg migration.

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// musKeyAllowlist is the test-only stand-in for the
// internal/cli/config_keys.go knownConfigKeys list. We can't import
// from cli (circular dep), so list-by-hand the keys the tests touch.
func musKeyAllowlist(k string) bool {
	switch k {
	case "data_project", "organism", "tissue", "cell_type",
		"eln_experiment_id", "irods_home",
		"source", "access":
		return true
	}
	return false
}

func writeEnv(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMigrate_NothingToDoWhenNoLegacy(t *testing.T) {
	dir := t.TempDir()
	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep != nil {
		t.Errorf("expected nil report when no .env, got %+v", rep)
	}
}

func TestMigrate_RefusesWhenMusCfgAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "organism=mouse\n")
	if err := os.WriteFile(filepath.Join(dir, "mus.cfg"),
		[]byte("organism=human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep != nil {
		t.Errorf("Migrate must not run when mus.cfg already exists")
	}
}

func TestMigrate_MovesMusKeysLeavesSecretsAndUnknowns(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"# Project metadata + a couple of shell secrets.",
		"data_project=Fiers2025",
		"organism=human",
		"tissue=hippocampus,prefrontal cortex",
		"DATABASE_URL=postgres://user:pw@host/db",
		"AWS_ACCESS_KEY_ID=AKIAEXAMPLE",
		"MY_CUSTOM_VAR=42",
		"",
	}, "\n")
	writeEnv(t, dir, body)

	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil {
		t.Fatal("expected a report")
	}

	wantMigrated := []string{"data_project", "organism", "tissue"}
	sort.Strings(rep.Migrated)
	if !reflect.DeepEqual(rep.Migrated, wantMigrated) {
		t.Errorf("Migrated = %v, want %v", rep.Migrated, wantMigrated)
	}
	wantSecrets := []string{"AWS_ACCESS_KEY_ID", "DATABASE_URL"}
	sort.Strings(rep.Secrets)
	if !reflect.DeepEqual(rep.Secrets, wantSecrets) {
		t.Errorf("Secrets = %v, want %v", rep.Secrets, wantSecrets)
	}
	wantUnknown := []string{"MY_CUSTOM_VAR"}
	if !reflect.DeepEqual(rep.Unknown, wantUnknown) {
		t.Errorf("Unknown = %v, want %v", rep.Unknown, wantUnknown)
	}

	// mus.cfg has only mus keys.
	cfgRaw, err := os.ReadFile(filepath.Join(dir, "mus.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(cfgRaw)
	for _, k := range []string{"data_project", "organism", "tissue"} {
		if !strings.Contains(cfg, k+"=") {
			t.Errorf("mus.cfg missing %s:\n%s", k, cfg)
		}
	}
	for _, k := range []string{"DATABASE_URL", "AWS_ACCESS_KEY_ID", "MY_CUSTOM_VAR"} {
		if strings.Contains(cfg, k) {
			t.Errorf("mus.cfg leaked %s — should have stayed in .env:\n%s", k, cfg)
		}
	}

	// .env retains secrets + unknowns + the comment.
	envRaw, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf(".env should still exist (had secrets / unknowns): %v", err)
	}
	envStr := string(envRaw)
	for _, k := range []string{"DATABASE_URL", "AWS_ACCESS_KEY_ID", "MY_CUSTOM_VAR"} {
		if !strings.Contains(envStr, k) {
			t.Errorf(".env should retain %s:\n%s", k, envStr)
		}
	}
	if !strings.Contains(envStr, "Project metadata") {
		t.Errorf(".env should retain comments:\n%s", envStr)
	}
	for _, k := range []string{"data_project", "organism", "tissue"} {
		if strings.Contains(envStr, k+"=") {
			t.Errorf(".env still has migrated key %s:\n%s", k, envStr)
		}
	}
}

func TestMigrate_DeletesEnvWhenEverythingMoved(t *testing.T) {
	dir := t.TempDir()
	// .env has ONLY mus keys, no comments, no secrets.
	writeEnv(t, dir, "organism=mouse\ndata_project=Test2026\n")
	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.LegacyKept {
		t.Errorf("LegacyKept should be false when .env is fully drained")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env should be deleted; stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mus.cfg")); err != nil {
		t.Errorf("mus.cfg should exist; stat=%v", err)
	}
}

func TestMigrate_DryRunTouchesNoFiles(t *testing.T) {
	dir := t.TempDir()
	// data_project + organism = 2 mus keys → passes safety gate.
	writeEnv(t, dir, "data_project=Fiers2025\norganism=human\nDATABASE_URL=x\n")
	rep, err := Migrate(dir, musKeyAllowlist, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || len(rep.Migrated) != 2 || len(rep.Secrets) != 1 {
		t.Errorf("classification wrong: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, "mus.cfg")); !os.IsNotExist(err) {
		t.Errorf("dry-run created mus.cfg; should not have")
	}
	// .env untouched.
	envRaw, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(envRaw), "organism=human") {
		t.Errorf("dry-run modified .env: %q", envRaw)
	}
}

func TestMigrate_SafetyGate_SingleMusNameKey(t *testing.T) {
	// A docker-style .env where ONE key name happens to collide with
	// a mus field name. The gate must refuse to touch it — silent
	// no-op, no mus.cfg created.
	dir := t.TempDir()
	writeEnv(t, dir, strings.Join([]string{
		"DATABASE_URL=postgres://x",
		"PORT=3000",
		"source=git@github:org/repo", // collides with mus's `source`
		"NODE_ENV=production",
	}, "\n")+"\n")
	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep != nil {
		t.Errorf("safety gate failed — should be silent no-op, got: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, "mus.cfg")); !os.IsNotExist(err) {
		t.Errorf("mus.cfg created from a non-mus .env — DANGEROUS")
	}
}

func TestMigrate_SafetyGate_TwoMusKeysPasses(t *testing.T) {
	// Two mus-recognised keys is enough to trip the gate.
	dir := t.TempDir()
	writeEnv(t, dir, "organism=human\ntissue=hippocampus\n")
	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || len(rep.Migrated) != 2 {
		t.Errorf("2 mus keys should pass safety gate; got %+v", rep)
	}
}

func TestMigrate_SafetyGate_DataProjectPassesEvenAlone(t *testing.T) {
	// data_project is a mus-specific key name (no other tool uses
	// it). Its presence alone is enough to act on.
	dir := t.TempDir()
	writeEnv(t, dir, "data_project=Fiers2025\nDATABASE_URL=x\n")
	rep, err := Migrate(dir, musKeyAllowlist, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || len(rep.Migrated) != 1 {
		t.Errorf("data_project alone should pass safety gate; got %+v", rep)
	}
}

func TestMigrate_ForceBypassesSafetyGate(t *testing.T) {
	// --force migrates a sparse .env that wouldn't otherwise pass.
	dir := t.TempDir()
	writeEnv(t, dir, "organism=human\nDATABASE_URL=x\n")
	rep, err := MigrateWithOptions(dir, musKeyAllowlist,
		MigrateOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || len(rep.Migrated) != 1 {
		t.Errorf("--force should migrate single-key .env; got %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, "mus.cfg")); err != nil {
		t.Errorf("mus.cfg should exist after --force: %v", err)
	}
}

func TestLooksLikeMusEnv(t *testing.T) {
	if !looksLikeMusEnv([]string{"data_project"}) {
		t.Error("data_project alone should pass")
	}
	if !looksLikeMusEnv([]string{"organism", "tissue"}) {
		t.Error("2 mus keys should pass")
	}
	if looksLikeMusEnv([]string{"source"}) {
		t.Error("single non-data_project key should NOT pass")
	}
	if looksLikeMusEnv([]string{"organism"}) {
		t.Error("single mus key (not data_project) should NOT pass")
	}
	if looksLikeMusEnv(nil) {
		t.Error("empty should not pass")
	}
}

func TestLooksLikeSecret(t *testing.T) {
	yes := []string{
		"DATABASE_URL", "AWS_ACCESS_KEY_ID", "GITHUB_TOKEN",
		"my_api_key", "STRIPE_SECRET_KEY", "JWT_SECRET",
		"PRIVATE_KEY", "openai_api_key", "Anthropic_API_KEY",
		"db_url", "MONGO_URL", "PIKA_TOKEN",
	}
	no := []string{
		"organism", "tissue", "data_project", "eln_experiment_id",
		"cell_type", "tag", "irods_home",
	}
	for _, k := range yes {
		if !looksLikeSecret(k) {
			t.Errorf("expected %q to look like a secret", k)
		}
	}
	for _, k := range no {
		if looksLikeSecret(k) {
			t.Errorf("%q should NOT look like a secret", k)
		}
	}
}

func TestLoadLocal_PrefersMusCfgOverEnv(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "organism=mouse\n")
	if err := os.WriteFile(filepath.Join(dir, "mus.cfg"),
		[]byte("organism=human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.String("organism"); got != "human" {
		t.Errorf("organism = %q (mus.cfg should win), .env said mouse, mus.cfg said human", got)
	}
}

func TestLoadLocal_FallsBackToLegacyEnv(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "organism=mouse\n")
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.String("organism"); got != "mouse" {
		t.Errorf("organism = %q, want mouse (read from legacy .env)", got)
	}
}
