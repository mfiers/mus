package cli

// `mus vocab` command tree — inspect the lab-wide controlled
// vocabulary (organism, celltype, technology, …) that every
// validating subcommand reads from.
//
// Layout:
//   mus vocab               → equivalent to `mus vocab show` (every field)
//   mus vocab show [FIELD]  → show every field, or just one
//   mus vocab refresh       → force a fresh HTTPS pull, bypassing the
//                             24h local cache TTL (same as `--refresh`).

import (
	"fmt"
	"sort"
	"strings"

	"codeberg.org/mfiers/mus/internal/vocab"
	"github.com/spf13/cobra"
)

func newVocabCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "vocab",
		Short: "show the active controlled vocabulary",
		Long: "Inspect the lab-wide controlled vocabulary mus uses to validate\n" +
			"annotations (organism, tissue, cell_type, technology, …).\n\n" +
			"`mus vocab` with no subcommand is equivalent to `mus vocab show` —\n" +
			"it prints every field. Pass a FIELD name to `mus vocab show FIELD`\n" +
			"to drill into one list (this is what `mus config set` suggests when\n" +
			"a value falls outside the vocabulary).\n\n" +
			"Use `mus vocab refresh` (or any subcommand with --refresh) to skip\n" +
			"the 24h local cache TTL and force a fresh fetch from Codeberg.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVocab(cmd, refresh, "")
		},
	}
	cmd.PersistentFlags().BoolVar(&refresh, "refresh", false,
		"skip the 24h local cache TTL and re-fetch from Codeberg")
	cmd.AddCommand(newVocabShowCmd(&refresh), newVocabRefreshCmd(&refresh))
	return cmd
}

func newVocabShowCmd(refresh *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "show [FIELD]",
		Short: "print the controlled vocabulary (all fields, or one)",
		Long: "Without arguments: prints every field's allowed values, plus the\n" +
			"loaded source (codeberg / local cache / embedded fallback) and\n" +
			"schema version.\n\n" +
			"With FIELD: prints only that field's controlled list. Unknown\n" +
			"FIELD names exit non-zero and list the known fields so a typo is\n" +
			"easy to spot.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			field := ""
			if len(args) == 1 {
				field = args[0]
			}
			return runVocab(cmd, *refresh, field)
		},
	}
}

func newVocabRefreshCmd(refresh *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "force a fresh vocabulary fetch from Codeberg (bypass the 24h cache)",
		Long: "Equivalent to `mus vocab show --refresh` — pulls a fresh copy of\n" +
			"the vocabulary from Codeberg, replaces the local cache, and prints\n" +
			"every field. mus also refreshes the cache automatically in the\n" +
			"background once per 24h on any invocation; this command is for when\n" +
			"you want to know NOW that an upstream vocabulary change has\n" +
			"landed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			*refresh = true
			return runVocab(cmd, true, "")
		},
	}
}

// runVocab prints the loaded vocabulary. When `field` is non-empty
// it filters output to that one field; an unknown field exits with
// an error listing the known names.
func runVocab(cmd *cobra.Command, refresh bool, field string) error {
	res, err := vocab.Load(cmd.Context(), vocab.LoadOptions{
		IgnoreCache: refresh,
	})
	if err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()

	// Stable ordering for the controlled values.
	keys := make([]string, 0, len(res.Vocabulary.Vocab))
	for k := range res.Vocabulary.Vocab {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Single-field path: print only the field's values, nothing else.
	if field != "" {
		vals, ok := res.Vocabulary.Vocab[field]
		if !ok {
			return fmt.Errorf(
				"unknown vocabulary field %q.\n  known fields: %s",
				field, strings.Join(keys, ", "))
		}
		for _, v := range vals {
			fmt.Fprintln(stdout, v)
		}
		return nil
	}

	// All-fields path: source banner + every list.
	fmt.Fprintf(stdout, "source: %s\n", res.Source)
	if res.Path != "" {
		fmt.Fprintf(stdout, "path:   %s\n", res.Path)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(stdout, "warn:   %s\n", w)
	}
	fmt.Fprintf(stdout, "schema_version: %d\n\n", res.Vocabulary.SchemaVersion)
	fmt.Fprintf(stdout, "required: %s\n", strings.Join(res.Vocabulary.Required, ", "))
	if len(res.Vocabulary.Soft) > 0 {
		fmt.Fprintf(stdout, "soft:     %s\n", strings.Join(res.Vocabulary.Soft, ", "))
	}
	fmt.Fprintln(stdout)
	for _, k := range keys {
		fmt.Fprintf(stdout, "%-16s %s\n", k+":", joinVocabValues(res.Vocabulary.Vocab[k]))
	}
	return nil
}

// joinVocabValues renders a list of vocabulary values for display.
// Items containing commas are double-quoted so the user can tell
// where one label ends and the next begins.
func joinVocabValues(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		if strings.ContainsRune(v, ',') {
			parts[i] = `"` + v + `"`
		} else {
			parts[i] = v
		}
	}
	return strings.Join(parts, ", ")
}
