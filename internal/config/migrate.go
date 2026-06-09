// Migration of legacy `.env` cascade files to `mus.cfg`.
//
// Before mus 0.2.12, the per-folder cascade file was `.env`. That
// name collided with the dotenv ecosystem (direnv, docker-compose,
// python-dotenv, …) and risked mus's structured metadata being
// loaded into a shell environment as variables — or, in the other
// direction, secrets in a user's existing `.env` being silently
// exposed to mus / pika.
//
// `mus.cfg` is the new canonical name. This file implements the
// per-directory migration: read the legacy `.env`, classify each
// key into one of three buckets, and emit a `mus.cfg` containing
// only the mus-recognised keys. Secrets and unknown keys STAY in
// `.env` — they were never mus's to touch.
//
// Classification:
//
//   - mus-key  → KEY is in knownConfigKeys (see internal/cli/config_keys.go)
//                OR matches one modulo case / _-space normalisation.
//   - secret   → KEY matches one of the secretPatterns below
//                (case-insensitive). Loud warning emitted, not migrated.
//   - unknown  → KEY is none of the above. Soft warning emitted,
//                not migrated.

package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codeberg.org/mfiers/mus/internal/atomicfile"
)

// MigrationReport summarises what happened during a single
// directory's migration. The CLI uses it to print a human-readable
// breakdown; callers can also act on it programmatically.
type MigrationReport struct {
	Dir          string   // absolute directory
	LegacyPath   string   // path to the old .env (always set when this struct is non-nil)
	NewCfgPath   string   // path to the new mus.cfg (always set, even on dry-run)
	Migrated     []string // mus-recognised keys moved to mus.cfg
	Secrets      []string // keys matching secret patterns — left in .env
	Unknown      []string // keys neither mus-recognised nor secret — left in .env
	LegacyKept   bool     // true when .env still has content after migration
	DryRun       bool     // true when no files were written
}

// Migrate moves mus-recognised keys from `<dir>/.env` to a new
// `<dir>/mus.cfg`. Secrets and unknown keys are LEFT IN PLACE in
// `.env` because mus doesn't know enough about them to safely move
// them. If `.env` would be left effectively empty (no keys, no
// comments) it is deleted.
//
// Idempotent: if there is no `.env` or there is already a `mus.cfg`,
// the call is a silent no-op and returns nil (no report).
//
// dryRun=true: classify and report what WOULD happen, but write
// nothing. Used by `mus config migrate --dry-run`.
//
// knownMusKey is the predicate used to classify a key as a mus
// field. We take it as a parameter rather than baking it in because
// the canonical list lives in internal/cli/config_keys.go (which
// imports this package, not the other way around).
//
// Wraps MigrateWithOptions with the SAFE-default policy: only act
// when the .env looks confidently mus-shaped. Callers that need the
// "act regardless" behaviour (e.g. `mus config migrate --force`)
// should call MigrateWithOptions directly.
func Migrate(dir string, knownMusKey func(string) bool, dryRun bool) (*MigrationReport, error) {
	return MigrateWithOptions(dir, knownMusKey, MigrateOptions{DryRun: dryRun})
}

// MigrateOptions configures Migrate's safety policy.
type MigrateOptions struct {
	// DryRun: classify + report but never write.
	DryRun bool
	// Force: skip the "looks confidently mus-shaped" guard and
	// migrate any .env that has at least one mus-recognised key.
	// Use only for the explicit `mus config migrate --force` path.
	Force bool
}

// MigrateWithOptions is the configurable form of Migrate. See
// MigrateOptions for the available knobs.
//
// Safety policy (when Force is false): migrate only when the file
// looks like a mus config, defined as either
//
//   - the file contains `data_project=<value>` (a key name no other
//     tool uses), OR
//   - the file contains TWO OR MORE keys that mus recognises
//
// A single coincidental mus-name key (e.g. a docker `.env` that
// happens to use `source=` for its own purposes) does NOT trip the
// migration. The user can always force it via the explicit
// command if they want to.
//
// When the guard refuses, the call is a silent no-op: returns nil
// rather than a report. That's the right contract for auto-migrate
// in PersistentPreRunE — we never want to print a "would have
// migrated, didn't" notice for someone else's file.
func MigrateWithOptions(dir string, knownMusKey func(string) bool, opts MigrateOptions) (*MigrationReport, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	legacy := filepath.Join(abs, LegacyFileName)
	target := filepath.Join(abs, FileName)

	// Idempotency guards.
	if _, err := os.Stat(legacy); err != nil {
		return nil, nil // no .env → nothing to migrate
	}
	if _, err := os.Stat(target); err == nil {
		// mus.cfg already exists. Cowardly refuse — we'd risk
		// overwriting user-managed mus.cfg content.
		return nil, nil
	}

	raw, err := os.ReadFile(legacy)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", legacy, err)
	}

	report := &MigrationReport{
		Dir:        abs,
		LegacyPath: legacy,
		NewCfgPath: target,
		DryRun:     opts.DryRun,
	}

	// Walk the file line-by-line so we can preserve comments + blank
	// lines on either side. Two output buffers: one for the new
	// mus.cfg, one for the trimmed-down .env left behind.
	var newCfg, leftover strings.Builder
	// Preserve trailing-newline style: if the original ended with
	// "\n", the rewritten one does too.
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		// Blank or comment → write to leftover (preserves the .env
		// shape for whatever tool was using it). NOT copied to
		// mus.cfg, which is a clean mus-only file.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			leftover.WriteString(line)
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx < 0 {
			// Malformed line — keep in .env, don't try to be smart.
			leftover.WriteString(line)
			continue
		}
		key := strings.TrimSpace(trimmed[:idx])
		switch classifyMigrationKey(key, knownMusKey) {
		case keyMusField:
			report.Migrated = append(report.Migrated, key)
			newCfg.WriteString(line)
		case keySecret:
			report.Secrets = append(report.Secrets, key)
			leftover.WriteString(line)
		case keyUnknown:
			report.Unknown = append(report.Unknown, key)
			leftover.WriteString(line)
		}
	}

	sort.Strings(report.Migrated)
	sort.Strings(report.Secrets)
	sort.Strings(report.Unknown)

	if len(report.Migrated) == 0 {
		// Nothing mus-recognisable to migrate. Don't create an empty
		// mus.cfg; return the report so the CLI can still surface
		// the secret / unknown findings.
		report.LegacyKept = true
		return report, nil
	}

	// Safety gate: refuse to touch the .env unless it looks
	// confidently mus-shaped. See MigrateWithOptions docstring. The
	// explicit `mus config migrate --force` path passes opts.Force
	// and bypasses this.
	if !opts.Force && !looksLikeMusEnv(report.Migrated) {
		// Treat as "not actually a mus file" — silent no-op.
		// Caller gets nil so they don't print a confusing report.
		return nil, nil
	}

	report.LegacyKept = !isEffectivelyEmpty(leftover.String())

	if opts.DryRun {
		return report, nil
	}

	// Write the new mus.cfg with a one-line header so a future
	// reader knows where it came from.
	header := "# mus configuration — migrated from .env on " +
		nowUTC() + "\n"
	if err := atomicfile.Replace(target,
		[]byte(header+newCfg.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", target, err)
	}

	// Rewrite or remove .env.
	if report.LegacyKept {
		if err := atomicfile.Replace(legacy,
			[]byte(leftover.String()), 0o644); err != nil {
			return nil, fmt.Errorf("rewrite %s: %w", legacy, err)
		}
	} else {
		if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove %s: %w", legacy, err)
		}
	}
	return report, nil
}

// looksLikeMusEnv returns true when the set of mus-recognised keys
// found in the .env meets the "confidently mus-shaped" bar:
//
//   - data_project is in the set, OR
//   - 2 or more keys are in the set
//
// A single coincidental key — e.g. a docker .env that happens to
// use `source=` for its own thing — does NOT trip the bar.
func looksLikeMusEnv(musKeysPresent []string) bool {
	if len(musKeysPresent) >= 2 {
		return true
	}
	for _, k := range musKeysPresent {
		if k == "data_project" {
			return true
		}
	}
	return false
}

// PrintMigrationReport writes a human-readable summary of `r` to
// `w`. Used by the CLI; library callers can format their own.
func PrintMigrationReport(w io.Writer, r *MigrationReport) {
	if r == nil {
		return
	}
	prefix := "  "
	if r.DryRun {
		fmt.Fprintf(w, "%s[dry-run] would migrate %s → %s:\n",
			prefix, r.LegacyPath, r.NewCfgPath)
	} else {
		fmt.Fprintf(w, "%smigrated %s → %s\n",
			prefix, r.LegacyPath, r.NewCfgPath)
	}
	if len(r.Migrated) > 0 {
		fmt.Fprintf(w, "%s  ✓ %d key(s) moved to mus.cfg: %s\n",
			prefix, len(r.Migrated), strings.Join(r.Migrated, ", "))
	}
	if len(r.Secrets) > 0 {
		fmt.Fprintf(w, "%s  ⚠ %d key(s) LOOK LIKE SECRETS — left in .env:\n",
			prefix, len(r.Secrets))
		for _, k := range r.Secrets {
			fmt.Fprintf(w, "%s      %s\n", prefix, k)
		}
		fmt.Fprintf(w, "%s    Review them. They were never mus's to read.\n", prefix)
	}
	if len(r.Unknown) > 0 {
		fmt.Fprintf(w, "%s  ⚠ %d key(s) not recognised as mus fields — left in .env:\n",
			prefix, len(r.Unknown))
		for _, k := range r.Unknown {
			fmt.Fprintf(w, "%s      %s\n", prefix, k)
		}
		fmt.Fprintf(w, "%s    `mus config set %s VALUE` if you meant them for mus.\n",
			prefix, "<KEY>")
	}
	if r.LegacyKept && !r.DryRun {
		fmt.Fprintf(w, "%s  .env kept (still has secrets / unknowns / comments)\n", prefix)
	} else if !r.LegacyKept && !r.DryRun {
		fmt.Fprintf(w, "%s  .env removed (nothing left to keep)\n", prefix)
	}
}

// ---- classifier ----------------------------------------------------------

type migrationKeyKind int

const (
	keyUnknown migrationKeyKind = iota
	keyMusField
	keySecret
)

func classifyMigrationKey(key string, knownMusKey func(string) bool) migrationKeyKind {
	if knownMusKey != nil && knownMusKey(key) {
		return keyMusField
	}
	if looksLikeSecret(key) {
		return keySecret
	}
	return keyUnknown
}

// looksLikeSecret reports whether `key` matches one of the well-
// known secret naming patterns. Case-insensitive; the matcher is
// deliberately conservative — false positives are fine (the key
// just doesn't get auto-migrated), false negatives mean a secret
// got copied into mus.cfg, which is the worse failure mode.
func looksLikeSecret(key string) bool {
	upper := strings.ToUpper(key)
	for _, suf := range secretSuffixes {
		if strings.HasSuffix(upper, suf) {
			return true
		}
	}
	for _, pre := range secretPrefixes {
		if strings.HasPrefix(upper, pre) {
			return true
		}
	}
	for _, name := range secretWholeNames {
		if upper == name {
			return true
		}
	}
	return false
}

// secretSuffixes / secretPrefixes / secretWholeNames are tuned to
// catch the common dotenv conventions for sensitive config without
// triggering on legitimate mus fields.
//
// All matched case-insensitively against an upper-cased key.
var (
	secretSuffixes = []string{
		"_TOKEN", "_KEY", "_SECRET", "_PASSWORD", "_PASS",
		"_APIKEY", "_API_KEY", "_PRIVKEY", "_PRIVATE_KEY",
		"_DSN",
	}
	secretPrefixes = []string{
		"AWS_", "AZURE_", "GCP_", "GOOGLE_",
		"GITHUB_", "GH_", "GITLAB_", "BITBUCKET_",
		"OPENAI_", "ANTHROPIC_", "HF_", "HUGGINGFACE_",
		"NPM_", "DOCKER_", "STRIPE_", "TWILIO_",
		"SENTRY_", "DATADOG_", "NEWRELIC_",
		"VAULT_", "CONSUL_",
	}
	secretWholeNames = []string{
		"DATABASE_URL", "DB_URL", "DATABASE_URI", "DB_URI",
		"REDIS_URL", "MONGO_URL", "MONGODB_URI",
		"JWT_SECRET", "SECRET_KEY", "PRIVATE_KEY",
		"CODEBERG_TOKEN", "PIKA_TOKEN",
	}
)

// nowUTC formats the current UTC time for the mus.cfg header. Kept
// as a local helper rather than calling time.Now directly inline so
// tests can stub it if we ever want deterministic snapshots.
var nowUTC = func() string {
	return timeNowUTC()
}
