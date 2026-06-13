package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/vocab"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "manage cascading mus.cfg folder config",
	}
	cmd.AddCommand(
		newConfigShowCmd(),
		newConfigSetCmd(),
		newConfigUnsetCmd(),
		newConfigFilesCmd(),
		newConfigMigrateCmd(),
	)
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var jsonOut bool
	var local bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "print effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			var env *config.Env
			if local {
				env, err = config.LoadLocal(dir)
			} else {
				env, err = config.Load(dir)
			}
			if err != nil {
				return err
			}
			all := env.All()
			if jsonOut {
				out, _ := json.MarshalIndent(all, "", "  ")
				fmt.Println(string(out))
				return nil
			}
			keys := make([]string, 0, len(all))
			for k := range all {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				switch v := all[k].(type) {
				case []string:
					fmt.Printf("%s\t%s\n", k, strings.Join(v, ", "))
				default:
					fmt.Printf("%s\t%v\n", k, v)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&jsonOut, "json", "j", false, "JSON output")
	cmd.Flags().BoolVarP(&local, "local", "l", false, "only show local .mus, not cascade")
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "set a key in the local mus.cfg",
		Long: "Writes KEY=VALUE to the local folder's mus.cfg (creating it if needed).\n\n" +
			"For keys with a controlled vocabulary (organism, tissue, technology, " +
			"cell_type, disease, suspension_type, sex, access, source, license):\n" +
			"  - case-only differences are silently canonicalised\n" +
			"    (e.g. `HuMaN` → `human`)\n" +
			"  - values that don't match any vocab entry are REJECTED;\n" +
			"    use --force to save them anyway (extends the vocab on disk,\n" +
			"    not in the source — file an issue to make the term canonical).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			resolvedKey, err := resolveConfigKey(key, force, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := validateConfigKey(resolvedKey, value); err != nil {
				return err
			}
			resolved, err := resolveConfigValue(cmd.Context(), resolvedKey, value, force, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			// Implicit migration: if this dir has a legacy .env but no
			// mus.cfg yet, move the mus-recognised keys over first so
			// the upcoming Save call doesn't write a half-empty mus.cfg
			// next to a still-authoritative .env.
			maybeMigrateLegacyEnv(dir, cmd.ErrOrStderr())
			return config.Save(dir, map[string]string{resolvedKey: resolved})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"save even if the value is not in the controlled vocabulary")
	return cmd
}

// validateConfigKey applies key-specific hard validation when writing
// to .env. Returns an error for malformed values that should NOT be
// written under any circumstance.
func validateConfigKey(key, value string) error {
	switch key {
	case "data_project":
		return dataproject.ValidateName(value)
	}
	return nil
}

// resolveConfigValue runs `value` through the joint vocabulary for
// fields that have a controlled-value list. Behaviour:
//
//   - Field not in the vocabulary at all (e.g. `note`, `irods_home`)
//     → returned verbatim, silent.
//   - Value matches a vocab entry modulo case / `_`-`-`-space
//     → rewritten to the canonical form. Tells the user on stderr
//     if the input differed from canonical.
//   - Value doesn't match any vocab entry
//     → REJECTED with an error pointing at the vocab + the --force
//     escape hatch. If `force` is true, kept verbatim with a stderr
//     warning so the off-vocab term stays visible.
//
// List-valued keys (tissue, cell_type, …) are split on commas and
// each item processed individually. A single off-vocab item rejects
// the whole save (atomic — never persist a partial list).
//
// Vocab load failures are non-fatal — set proceeds without
// validation (with a stderr note). Same posture as mus's other
// vocab consumers.
func resolveConfigValue(ctx context.Context, key, value string, force bool, stderr io.Writer) (string, error) {
	if !isVocabConfigKey(key) {
		return value, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := vocab.Load(ctx, vocab.LoadOptions{})
	if err != nil {
		fmt.Fprintf(stderr,
			"  note: vocabulary unavailable (%v) — saving %s=%q verbatim\n",
			err, key, value)
		return value, nil
	}
	if isVocabListKey(key) {
		return resolveVocabList(res.Vocabulary, key, value, force, stderr)
	}
	return resolveVocabScalar(res.Vocabulary, key, value, force, stderr)
}

// isVocabListKey reports whether a vocab-controlled key is also a
// comma-separated list-valued key. Subset of isVocabConfigKey.
func isVocabListKey(key string) bool {
	switch key {
	case "tissue", "cell_type", "disease":
		return true
	}
	return false
}

// isVocabConfigKey reports whether a .env key has a controlled
// vocabulary. Hard-coded list mirrors the joint-vocab top-level keys
// — kept in sync with vocabulary/main.yaml.
func isVocabConfigKey(key string) bool {
	switch key {
	case "organism", "technology", "suspension_type", "sex",
		"access", "source", "license",
		"tissue", "cell_type", "disease":
		return true
	}
	return false
}

// offVocabError is the user-facing error for a value that doesn't
// match any vocab entry. Names the key + value, suggests the
// closest allowed value (with the exact corrected command), points
// at the live vocab + local `mus vocab show`, and surfaces --force.
func offVocabError(v *vocab.Vocabulary, key, value string) error {
	msg := fmt.Sprintf(
		"%s=%q is not in the controlled vocabulary — refusing to save.\n",
		key, value)
	if suggested := nearestVocabValue(v, key, value); suggested != "" {
		msg += fmt.Sprintf(
			"  Did you mean %q?\n"+
				"  Try: mus config set %s %s\n",
			suggested, key, shellQuote(suggested))
	}
	msg += fmt.Sprintf("  See allowed values: `mus vocab show %s`\n", key)
	msg += "  Or browse: https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml\n"
	msg += "  Pass --force to save anyway (then please file an issue to extend the vocab)."
	return fmt.Errorf("%s", msg)
}

func resolveVocabScalar(v *vocab.Vocabulary, key, value string, force bool, stderr io.Writer) (string, error) {
	canon, ok := v.Canonical(key, value)
	if !ok {
		if !force {
			return "", offVocabError(v, key, value)
		}
		fmt.Fprintf(stderr,
			"  ⚠ %s=%q is not in the controlled vocabulary — saving anyway (--force).\n",
			key, value)
		if suggested := nearestVocabValue(v, key, value); suggested != "" {
			fmt.Fprintf(stderr,
				"    (closest known value: %q — `mus config set %s %s`)\n",
				suggested, key, shellQuote(suggested))
		}
		return value, nil
	}
	if canon != value {
		fmt.Fprintf(stderr,
			"  note: normalised %s=%q → %q (canonical form)\n",
			key, value, canon)
	}
	return canon, nil
}

// nearestVocabValue returns the closest allowed value for `key` to
// `value`. Two passes:
//
//  1. Whole-string Levenshtein distance, threshold-bounded. Catches
//     simple typos: "huamn" → "human", "kortex" → "cortex".
//  2. Token-level fallback: input tokens vs each token of each
//     allowed value, threshold 2. Catches incomplete pastes that
//     match one word of a multi-word vocab entry: "microglias" →
//     "microglial cell" (1 edit on the first token).
//
// Whole-string hits always rank above token hits. Within each pass
// lower edit distance wins; ties broken alphabetically for
// determinism.
func nearestVocabValue(v *vocab.Vocabulary, key, value string) string {
	allowed := v.AllowedValues(key)
	if allowed == nil {
		return ""
	}
	normalizedInput := strings.ToLower(strings.TrimSpace(value))
	if normalizedInput == "" {
		return ""
	}
	// Whole-string threshold: scales with length so single-letter
	// typos in 4-char terms (human) and longer typos in 15-char terms
	// (dorsolateral …) both get caught, without `x` → `a` false hits.
	wholeThreshold := 3
	if alt := len(normalizedInput) * 2 / 5; alt > wholeThreshold {
		wholeThreshold = alt
	}
	// Hard upper bound at half the input length — beyond that it's a
	// different word, not a typo.
	if cap := len(normalizedInput) / 2; cap < wholeThreshold {
		wholeThreshold = cap
	}
	if wholeThreshold < 1 {
		wholeThreshold = 1
	}

	type cand struct {
		val  string
		dist int  // bucket: <100 whole-string, 100+ token-level
	}
	var hits []cand
	inputTokens := strings.Fields(normalizedInput)
	for _, a := range allowed {
		aLower := strings.ToLower(a)

		// Pass 1: whole string.
		if d := levenshtein(normalizedInput, aLower); d <= wholeThreshold {
			hits = append(hits, cand{a, d})
			continue
		}

		// Pass 2: token vs token. Tight threshold (2) per pairing.
		allowedTokens := strings.Fields(aLower)
		bestTok := -1
		for _, it := range inputTokens {
			if len(it) < 4 {
				continue // skip very short tokens — too many false positives
			}
			for _, at := range allowedTokens {
				if len(at) < 4 {
					continue
				}
				d := levenshtein(it, at)
				if d <= 2 && (bestTok == -1 || d < bestTok) {
					bestTok = d
				}
			}
		}
		if bestTok >= 0 {
			// Offset so token hits rank below all whole-string hits.
			hits = append(hits, cand{a, bestTok + 100})
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].dist != hits[j].dist {
			return hits[i].dist < hits[j].dist
		}
		return hits[i].val < hits[j].val
	})
	return hits[0].val
}

// shellQuote wraps a value in single quotes when it contains
// whitespace, commas, or shell metacharacters. So a suggestion like
// `microglial cell` renders as `'microglial cell'` — pasteable as-is
// into a shell.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.ContainsAny(s, " \t,'\"\\$`*?[]{}()<>|&;!#~") {
		// Single-quote it; escape any embedded single quotes the
		// standard POSIX way (close-quote, escaped-quote, re-open).
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

func resolveVocabList(v *vocab.Vocabulary, key, value string, force bool, stderr io.Writer) (string, error) {
	items := strings.Split(value, ",")
	out := make([]string, 0, len(items))
	// For each rejected item we keep the raw value AND a suggestion
	// (possibly empty) so the error message can show per-item hints
	// + the corrected command.
	type rej struct {
		raw       string
		suggested string
	}
	var rejected []rej
	// Track the canonicalised items (suggestions or originals) for the
	// "try this" command line at the bottom of the error.
	corrected := make([]string, 0, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		resolved, err := resolveVocabScalar(v, key, item, force, stderr)
		if err != nil {
			s := nearestVocabValue(v, key, item)
			rejected = append(rejected, rej{raw: item, suggested: s})
			if s != "" {
				corrected = append(corrected, s)
			} else {
				corrected = append(corrected, item)
			}
			continue
		}
		out = append(out, resolved)
		corrected = append(corrected, resolved)
	}
	if len(rejected) > 0 {
		var msg strings.Builder
		fmt.Fprintf(&msg,
			"%d list item(s) not in the controlled vocabulary for %s — refusing to save.\n",
			len(rejected), key)
		for _, r := range rejected {
			if r.suggested != "" {
				fmt.Fprintf(&msg, "  - %q → did you mean %q?\n", r.raw, r.suggested)
			} else {
				fmt.Fprintf(&msg, "  - %q (no near match in the vocabulary)\n", r.raw)
			}
		}
		// Build the corrected one-liner. Only show it if at least one
		// suggestion was meaningful (otherwise we'd just be parroting
		// the user's input).
		anySuggested := false
		for _, r := range rejected {
			if r.suggested != "" {
				anySuggested = true
				break
			}
		}
		if anySuggested {
			fmt.Fprintf(&msg, "  Try: mus config set %s %s\n",
				key, shellQuote(strings.Join(corrected, ",")))
		}
		fmt.Fprintf(&msg, "  See allowed values: `mus vocab show %s`\n", key)
		msg.WriteString("  Or browse: https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml\n")
		msg.WriteString("  Pass --force to save anyway (then please file an issue to extend the vocab).")
		return "", fmt.Errorf("%s", msg.String())
	}
	return strings.Join(out, ","), nil
}

func newConfigMigrateCmd() *cobra.Command {
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "move mus-recognised keys from a legacy .env to mus.cfg",
		Long: "Pre-0.2.12 mus stored folder config in `.env`. That name collides\n" +
			"with the dotenv ecosystem (direnv, docker-compose, python-dotenv,\n" +
			"…) and risks mus's structured metadata being eval'd into a shell\n" +
			"environment. `mus.cfg` is the new canonical name.\n\n" +
			"This command rewrites the local .env: mus-recognised keys\n" +
			"(organism, tissue, data_project, …) move to mus.cfg; anything\n" +
			"that looks like a secret (DATABASE_URL, AWS_*, *_TOKEN, …) or\n" +
			"that mus doesn't recognise stays in .env, with a warning so you\n" +
			"can review it. If .env ends up empty after the move it gets\n" +
			"deleted; otherwise it stays in place for whatever other tool\n" +
			"was using it.\n\n" +
			"Safety gate: by default mus only migrates a .env that LOOKS\n" +
			"confidently mus-shaped (contains data_project= OR ≥2 mus keys).\n" +
			"A single coincidental mus-name key in a docker / direnv .env\n" +
			"does NOT trigger migration. Pass --force to override the gate\n" +
			"when you know the .env is mus's.\n\n" +
			"Idempotent: a folder that already has mus.cfg is left alone.\n" +
			"Cascade-aware mus calls already read both filenames; migration\n" +
			"is recommended but not required.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			report, err := config.MigrateWithOptions(dir, isKnownConfigKey,
				config.MigrateOptions{DryRun: dryRun, Force: force})
			if err != nil {
				return err
			}
			if report == nil {
				fmt.Fprintln(cmd.OutOrStdout(),
					"nothing to migrate (no .env, mus.cfg already exists, "+
						"or .env doesn't look mus-shaped — use --force to override)")
				return nil
			}
			config.PrintMigrationReport(cmd.ErrOrStderr(), report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"classify and report what would be migrated; don't touch any files")
	cmd.Flags().BoolVar(&force, "force", false,
		"skip the safety gate; migrate any .env with at least one mus key")
	return cmd
}

// maybeMigrateLegacyEnv runs the per-directory .env → mus.cfg
// migration silently when both conditions hold:
//   - there's a `.env` in `dir`
//   - there's no `mus.cfg` in `dir`
//
// Errors from the migration are reported but NOT fatal — `mus config set`
// proceeds either way so the user's primary action isn't blocked by
// migration trouble. Idempotent.
func maybeMigrateLegacyEnv(dir string, stderr io.Writer) {
	report, err := config.Migrate(dir, isKnownConfigKey, false)
	if err != nil {
		fmt.Fprintf(stderr, "  note: legacy .env migration failed: %v\n", err)
		return
	}
	if report == nil {
		return
	}
	config.PrintMigrationReport(stderr, report)
}

// isKnownConfigKey is the predicate the config.Migrate classifier
// uses to decide whether a legacy .env key is a mus field worth
// moving to mus.cfg.
func isKnownConfigKey(key string) bool {
	// Exact match against the master list.
	for _, k := range knownConfigKeys {
		if k == key {
			return true
		}
	}
	// Case / underscore / dash / space variants — same fuzz the
	// `mus config set` key resolver uses. This catches `CellType` /
	// `cell-type` / `celltype` flavours of mus keys in legacy .env
	// files written by hand.
	normalized := normalizeConfigKey(key)
	for _, k := range knownConfigKeys {
		if normalizeConfigKey(k) == normalized {
			return true
		}
	}
	return false
}

func newConfigUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset KEY",
		Short: "remove a key from the local mus.cfg",
		Long: "Removes KEY from the local folder's mus.cfg. Preserves any\n" +
			"comments and blank lines in the file. If removing KEY leaves\n" +
			"the file with no keys AND no comments (only whitespace), the\n" +
			"file is deleted; otherwise it's kept with the surviving\n" +
			"content.\n\n" +
			"This is local-only — keys inherited from parent mus.cfg files\n" +
			"(or legacy .env in the cascade) are not touched. To remove a\n" +
			"key from a parent, run `mus -C PARENT_DIR config unset KEY`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			return config.Unset(dir, key)
		},
	}
}

func newConfigFilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "files",
		Short: "list mus.cfg / .env files in the cascade (root first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			env, err := config.Load(dir)
			if err != nil {
				return err
			}
			for _, f := range env.Files() {
				fmt.Println(f)
			}
			return nil
		},
	}
}
