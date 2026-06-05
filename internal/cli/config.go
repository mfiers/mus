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
		Short: "manage .mus TOML config (cascading)",
	}
	cmd.AddCommand(
		newConfigShowCmd(),
		newConfigSetCmd(),
		newConfigUnsetCmd(),
		newConfigFilesCmd(),
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
	return &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "set a key in the local .env",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			if err := validateConfigKey(key, value); err != nil {
				return err
			}
			resolved, err := resolveConfigValue(cmd.Context(), key, value, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			dir, err := workingDir(cmd)
			if err != nil {
				return err
			}
			return config.Save(dir, map[string]string{key: resolved})
		},
	}
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
//     → kept verbatim; prints a stderr warning so the typo is
//     visible. The save still proceeds — the controlled vocabulary
//     is a guidance layer, not a hard schema, and the depositor may
//     have a legitimate reason to use a not-yet-listed term.
//
// List-valued keys (tissue, cell_type, …) are split on commas and
// each item processed individually. The returned string is the
// canonicalised comma-joined form.
//
// Vocab load failures are non-fatal — set proceeds without
// validation. (mus's other vocab consumers do the same.)
func resolveConfigValue(ctx context.Context, key, value string, stderr io.Writer) (string, error) {
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
		return resolveVocabList(res.Vocabulary, key, value, stderr), nil
	}
	return resolveVocabScalar(res.Vocabulary, key, value, stderr), nil
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

func resolveVocabScalar(v *vocab.Vocabulary, key, value string, stderr io.Writer) string {
	canon, ok := v.Canonical(key, value)
	if !ok {
		fmt.Fprintf(stderr,
			"  ⚠ %s=%q is not in the controlled vocabulary — saving anyway.\n"+
				"    See `mus vocab show %s` or %s\n",
			key, value, key,
			"https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml")
		return value
	}
	if canon != value {
		fmt.Fprintf(stderr,
			"  note: normalised %s=%q → %q (canonical form)\n",
			key, value, canon)
	}
	return canon
}

func resolveVocabList(v *vocab.Vocabulary, key, value string, stderr io.Writer) string {
	items := strings.Split(value, ",")
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		out = append(out, resolveVocabScalar(v, key, item, stderr))
	}
	return strings.Join(out, ",")
}

func newConfigUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset KEY",
		Short: "remove a key from the local .env",
		Long: "Removes KEY from the local folder's .env. Preserves any\n" +
			"comments and blank lines in the file. If removing KEY leaves\n" +
			"the file with no keys AND no comments (only whitespace), the\n" +
			"file is deleted; otherwise it's kept with the surviving\n" +
			"content.\n\n" +
			"This is local-only — keys inherited from parent .env files\n" +
			"are not touched. To remove a key from a parent, run\n" +
			"`mus -C PARENT_DIR config unset KEY`.",
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
		Short: "list .mus files in the cascade (root first)",
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
