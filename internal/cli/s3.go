package cli

// `mus s3` subtree. Phase 1: vocabulary inspection + h5ad sanity
// checks. Phase 2 (login / check / upload) is not yet wired.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/h5ad"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

func newS3Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "s3",
		Short: "S3 deposit pipeline for .h5ad files (sanity checks + upload)",
		Long: "First-class commands today:\n" +
			"  mus s3 inspect FILE  — run the anthive ingestion-spec sanity checks\n" +
			"  mus s3 vocab         — inspect the active controlled vocabulary\n\n" +
			"`mus s3 login` / `check` / `upload` are not wired yet — Phase 2.",
	}
	cmd.AddCommand(newS3InspectCmd(), newS3VocabCmd(), newS3UploadStubCmd())
	return cmd
}

// --- mus s3 inspect FILE ---------------------------------------------------

func newS3InspectCmd() *cobra.Command {
	var writeSidecar bool
	cmd := &cobra.Command{
		Use:   "inspect FILE",
		Short: "validate an .h5ad + its sibling .metadata.yaml against the ingestion spec",
		Long: "FILE can be either the .h5ad or its .metadata.yaml — mus finds the pair.\n\n" +
			"Returns exit code 0 if no HARD findings, 1 otherwise. SOFT findings are\n" +
			"printed but never fail the command.\n\n" +
			"Requires `uv` on PATH (resolves anndata + numpy + scipy lazily).\n" +
			"See https://docs.astral.sh/uv/ for the installer.\n\n" +
			"The first invocation of inspect on a fresh machine downloads ~150 MB of\n" +
			"Python packages into uv's cache (~/.cache/uv/). Subsequent runs are ~1s.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runS3Inspect(cmd, args[0], writeSidecar)
		},
	}
	cmd.Flags().BoolVar(&writeSidecar, "write-sidecar", true,
		"on success, stamp the h5ad_* block into <file>.h5ad.mus")
	return cmd
}

func runS3Inspect(cmd *cobra.Command, pathHint string, writeSidecar bool) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}

	// 1. Resolve the active vocabulary (iRODS → cache → embedded).
	vocabRes, err := h5ad.Load(cmd.Context(), h5ad.LoadOptions{
		IRODSHome: env.String("irods_home"),
	})
	if err != nil {
		return fmt.Errorf("load vocabulary: %w", err)
	}
	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "vocabulary source: %s", vocabRes.Source)
	if vocabRes.Path != "" {
		fmt.Fprintf(stderr, " (%s)", vocabRes.Path)
	}
	fmt.Fprintln(stderr)
	for _, w := range vocabRes.Warnings {
		fmt.Fprintf(stderr, "  ! %s\n", w)
	}

	// 2. Run the checks.
	report, err := h5ad.RunChecks(cmd.Context(), pathHint, h5ad.CheckOptions{
		Inspector:  h5ad.NewPythonInspector(),
		Vocabulary: vocabRes.Vocabulary,
		// ComputeSha256 is left nil for now — phase 2 will wire the
		// hashcache so metadata.sha256 cross-check kicks in for the
		// upload path. The inspect command stays cheap.
	})
	if err != nil {
		return err
	}

	// 3. Print the tiered report.
	stdout := cmd.OutOrStdout()
	printReport(stdout, report)

	// 4. Sidecar stamp on PASS (no HARD findings).
	if writeSidecar && !report.HasHard() && report.Inspection != nil {
		if err := stampSidecar(report); err != nil {
			fmt.Fprintf(stderr, "  ! could not write sidecar: %v\n", err)
		} else {
			fmt.Fprintf(stderr, "wrote sidecar %s\n", sidecar.SidecarPath(report.H5adPath))
		}
	}

	if report.HasHard() {
		return fmt.Errorf("h5ad inspection failed: %d HARD finding(s) — deposit refused",
			len(report.Hard()))
	}
	return nil
}

// printReport renders a Report as human-readable output.
func printReport(w io.Writer, r *h5ad.Report) {
	if r.Inspection != nil {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSuffix(r.H5adPath, ""))
		fmt.Fprintf(w, "  n_obs=%d n_vars=%d layers=%s embedding=%s anndata=%s\n",
			r.Inspection.NObs, r.Inspection.NVars,
			joinOrDash(r.Inspection.Layers),
			embeddingLabel(r.Inspection),
			or(r.Inspection.AnnDataVersion, "?"))
	}
	printIssueGroup(w, "HARD", r.Hard(), "✘")
	printIssueGroup(w, "SOFT", r.Soft(), "⚠")
	printIssueGroup(w, "INFO", r.Info(), "·")
	if !r.HasHard() {
		fmt.Fprintln(w, "\n✓ no HARD findings — file passes the ingestion spec")
	}
	fmt.Fprintln(w)
}

func printIssueGroup(w io.Writer, label string, issues []h5ad.Issue, marker string) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s (%d):\n", label, len(issues))
	for _, i := range issues {
		field := i.Field
		if field != "" {
			field = " " + field
		}
		fmt.Fprintf(w, "  %s%s  %s\n", marker, field, i.Message)
	}
}

func embeddingLabel(rep *h5ad.InspectionReport) string {
	switch {
	case rep.Obsm.HasUMAP:
		return "X_umap"
	case rep.Obsm.HasTSNE:
		return "X_tsne"
	default:
		return "(none)"
	}
}

func joinOrDash(v []string) string {
	if len(v) == 0 {
		return "—"
	}
	return strings.Join(v, ",")
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// stampSidecar writes the h5ad_* block into <h5ad>.mus, preserving
// other sidecar sections if they exist.
func stampSidecar(r *h5ad.Report) error {
	scPath := sidecar.SidecarPath(r.H5adPath)
	doc, err := readMaybe(scPath)
	if err != nil {
		return err
	}
	if doc == nil {
		doc = &sidecar.Doc{}
	}
	h := &sidecar.H5ad{
		NObs:           r.Inspection.NObs,
		NVars:          r.Inspection.NVars,
		Layers:         append([]string(nil), r.Inspection.Layers...),
		Embedding:      embeddingLabel(r.Inspection),
		AnnDataVersion: r.Inspection.AnnDataVersion,
		InspectedAt:    time.Now().UTC().Truncate(time.Second),
	}
	if r.Metadata != nil {
		m := r.Metadata
		h.Organism = m.Organism
		h.Celltype = append([]string(nil), m.Celltype...)
		h.Technology = m.Technology
		h.Preparation = m.Preparation
		h.Tissue = append([]string(nil), m.Tissue...)
		h.Region = append([]string(nil), m.Region...)
		h.Disease = append([]string(nil), m.Disease...)
		h.Sex = m.Sex
		h.Internal = m.Internal
		h.Accession = m.Accession
		h.DOI = m.DOI
		h.Year = m.Year
		h.Contributor = m.Contributor
		h.GeneIDType = m.GeneIDType
	}
	doc.H5ad = h
	return sidecar.Write(scPath, doc)
}

// --- mus s3 vocab ----------------------------------------------------------

func newS3VocabCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "vocab",
		Short: "show the active controlled vocabulary (source + lists)",
		Long: "Prints which vocabulary mus is using (iRODS / local cache / embedded\n" +
			"fallback) and the controlled-value lists from §3 of the ingestion spec.\n\n" +
			"With --refresh, ignores the local cache and forces an iRODS re-pull.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runS3Vocab(cmd, refresh)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false,
		"force re-pull from iRODS (ignore the 24h local cache TTL)")
	return cmd
}

func runS3Vocab(cmd *cobra.Command, refresh bool) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	res, err := h5ad.Load(cmd.Context(), h5ad.LoadOptions{
		IRODSHome:   env.String("irods_home"),
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
	sortStrings(keys)
	for _, k := range keys {
		fmt.Fprintf(stdout, "%-12s %s\n", k+":", strings.Join(res.Vocabulary.Vocab[k], ", "))
	}
	return nil
}

func sortStrings(xs []string) {
	for i := range xs {
		for j := i + 1; j < len(xs); j++ {
			if xs[j] < xs[i] {
				xs[i], xs[j] = xs[j], xs[i]
			}
		}
	}
}

// --- mus s3 upload (stub, Phase 2) ----------------------------------------

func newS3UploadStubCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upload FILE [FILE...]",
		Short: "(planned) upload h5ad files to S3 — Phase 2",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"mus s3 upload is not implemented yet (Phase 2).")
			fmt.Fprintln(cmd.ErrOrStderr(),
				"For now run `mus s3 inspect FILE` to validate; uploads stay manual.")
			return fmt.Errorf("not implemented")
		},
	}
}

// readMaybe is shared with the iRODS upload path; defined in
// irods.go and used by stampSidecar above.
