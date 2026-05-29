package cli

// Top-level `mus vocab` command — prints the active controlled
// vocabulary (source + lists). The vocabulary is the lab-wide
// canonical list of allowed values for annotation fields (organism,
// celltype, technology, …). Every mus subcommand that validates
// annotations reads from this single source.

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
		Short: "show the active controlled vocabulary (source + lists)",
		Long: "Prints which vocabulary mus is using (codeberg / local cache /\n" +
			"embedded fallback), where it came from, and the controlled-value\n" +
			"lists for every field under vocabulary control.\n\n" +
			"With --refresh, skips the 24h local cache TTL and forces a fresh\n" +
			"HTTPS fetch from Codeberg.\n\n" +
			"The vocabulary is the single source of truth for every annotation\n" +
			"mus validates — currently `mus h5ad check` consumes it; future\n" +
			"data-format tools will share the same lists.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVocab(cmd, refresh)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false,
		"skip the 24h local cache TTL and re-fetch from Codeberg")
	return cmd
}

func runVocab(cmd *cobra.Command, refresh bool) error {
	res, err := vocab.Load(cmd.Context(), vocab.LoadOptions{
		IgnoreCache: refresh,
	})
	if err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()
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
	// Stable ordering for the controlled values.
	keys := make([]string, 0, len(res.Vocabulary.Vocab))
	for k := range res.Vocabulary.Vocab {
		keys = append(keys, k)
	}
	sort.Strings(keys)
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
