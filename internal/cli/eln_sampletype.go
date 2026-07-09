package cli

// `mus eln sample-type [NAME]` — diagnostic command that inspects a
// sample type on the tenant and prints its meta-field definitions.
//
// Primary use case: verifying that the tenant-declared meta field
// names match mus's expected auto-fill keys (path, sha256sum,
// mango_purl / purl, upload_from_path, ...). Case-insensitive
// exact-character match — a typo or spelling mismatch here silently
// drops the field on POST /samples.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newELNSampleTypeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sample-type [NAME]",
		Short: "inspect a sample type's meta-field definitions on the ELN tenant",
		Long: "Prints the meta-field definitions on a sample type. Use to verify\n" +
			"that the tenant-declared field names match the auto-fill keys mus\n" +
			"knows about (path, sha256sum, size_bytes, mtime, purl / mango_purl,\n" +
			"upload_data, upload_user, upload_from_server, upload_from_path,\n" +
			"kind, mus_version).\n\n" +
			"Matching is case-insensitive on character content; underscores /\n" +
			"hyphens / spaces DO NOT normalise. `mango_purl` matches `MANGO_PURL`\n" +
			"but NOT `mangopurl` or `mango-purl`.\n\n" +
			"With no NAME argument, defaults to \"Data\".",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "Data"
			if len(args) == 1 {
				name = args[0]
			}
			return runELNSampleTypeInspect(cmd, name)
		},
	}
	return cmd
}

func runELNSampleTypeInspect(cmd *cobra.Command, name string) error {
	stdout := cmd.OutOrStdout()

	client, err := openELN()
	if err != nil {
		return err
	}
	st, err := client.FindSampleTypeByName(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sample type: %s (id=%d)\n\n", st.Name, st.ID)

	metas, err := client.ListSampleTypeMetas(st.ID)
	if err != nil {
		return fmt.Errorf("list meta fields: %w", err)
	}
	if len(metas) == 0 {
		fmt.Fprintln(stdout, "(no meta fields declared on this sample type — auto-fill is a no-op)")
		return nil
	}

	// mus-known auto-fill keys — anything the tenant declared under a
	// name that lowercases to one of these gets filled automatically
	// on POST /samples. Kept in lowercase for O(1) match.
	musKnown := map[string]bool{
		"path": true, "upload_from_path": true,
		"sha256sum": true, "sha256": true,
		"size_bytes": true, "size": true,
		"mtime": true,
		"upload_data": true, "upload_date": true,
		"upload_user":        true,
		"upload_from_server": true,
		"purl":               true,
		"mango_purl":         true,
		"kind":               true,
		"mus_version":        true,
	}

	fmt.Fprintf(stdout, "%-24s  %-10s  %-8s  %s\n",
		"KEY (tenant)", "DATATYPE", "TYPE_ID", "AUTOFILL")
	fmt.Fprintln(stdout, strings.Repeat("-", 60))
	for _, m := range metas {
		mark := "-"
		if musKnown[strings.ToLower(strings.TrimSpace(m.Key))] {
			mark = "✓ mus fills this"
		}
		fmt.Fprintf(stdout, "%-24s  %-10s  %-8d  %s\n",
			m.Key, m.SampleDataType, m.SampleTypeMetaID, mark)
	}
	return nil
}
