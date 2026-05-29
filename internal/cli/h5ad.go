package cli

// `mus h5ad` subtree.
//
// Currently exposes one user-facing command, `mus h5ad check FILE`,
// which validates a .h5ad against the anthive ingestion spec
// (vocabulary served from this repo's vocabulary/main.yaml). On pass
// the sidecar at <file>.h5ad.mus is stamped with the h5ad_* shape
// block + Bio block, and the file's current sha256 is pinned into
// h5ad_inspected_sha256 so future `mus check` runs (and the planned
// upload gates) can tell "the recorded pass is still valid" without
// re-running anndata.
//
// All shared inspection helpers (runH5adInspection, stampSidecar,
// printReport, …) also live in this file — they're used by
// internal/cli/check.go's h5ad-freshness post-pass.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/h5ad"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"codeberg.org/mfiers/mus/internal/vocab"
	"github.com/spf13/cobra"
)

func newH5adCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "h5ad",
		Short: "h5ad ingestion sanity-checks (one command today: `check`)",
	}
	cmd.AddCommand(newH5adCheckCmd())
	return cmd
}

// --- mus h5ad check --------------------------------------------------------

func newH5adCheckCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "check FILE",
		Short: "validate an .h5ad against the lab ingestion spec",
		Long: "FILE is the path to a .h5ad data file (passing the sidecar path\n" +
			"<file>.h5ad.mus also works — mus resolves to the data file).\n\n" +
			"Reads experiment metadata (organism / celltype / technology / …) from\n" +
			"the cascading .env walking up from the file's directory, plus per-file\n" +
			"overrides from <file>.h5ad.mus if present.\n\n" +
			"If the sidecar already records a passed inspection AND the file's\n" +
			"sha256 hasn't changed since, the previous PASS is reused and anndata is\n" +
			"NOT re-run. Override with --force to re-inspect anyway.\n\n" +
			"Otherwise: streams the embedded Python helper via `uv run --script` to\n" +
			"open the file in backed='r' mode, applies §1.1-§1.4 structural rules,\n" +
			"validates metadata against the controlled vocabulary (see `mus vocab`),\n" +
			"prints a tiered HARD/SOFT/INFO report, and on PASS stamps the sidecar\n" +
			"with the h5ad_* block + the file's sha256 as h5ad_inspected_sha256.\n\n" +
			"Exit code is 0 on PASS (cached or fresh), 1 on any HARD finding.\n\n" +
			"Requires `uv` on PATH for the inspection path; see\n" +
			"https://docs.astral.sh/uv/ for the installer.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runH5adCheck(cmd, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"re-run anndata inspection even when the sidecar records a passed inspection at the current sha256")
	return cmd
}

func runH5adCheck(cmd *cobra.Command, arg string, force bool) error {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return err
	}
	// Sidecar argument → resolve to the data file.
	if sidecar.IsSidecar(abs) {
		abs = sidecar.DataPath(abs)
	}
	if !isH5adData(abs) {
		return fmt.Errorf("%s is not a .h5ad file (extension must be lowercase .h5ad per spec §1.4)", abs)
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}

	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	// Cached-pass shortcut.
	if !force {
		hit, err := cachedH5adPass(cache, abs, stdout)
		if err != nil {
			return err
		}
		if hit {
			return nil
		}
	}

	// Full inspection.
	report, err := runH5adInspection(cmd, cache, abs)
	if err != nil {
		return err
	}
	printReport(stdout, report)

	if report.HasHard() {
		return fmt.Errorf("h5ad check failed: %d HARD finding(s) — fix and re-run",
			len(report.Hard()))
	}

	// PASS → stamp the sidecar.
	sha, err := cache.Sum(abs)
	if err != nil {
		fmt.Fprintf(stderr, "WARN: could not compute sha256 of %s: %v\n", abs, err)
		return nil
	}
	if err := stampSidecar(report, sha); err != nil {
		fmt.Fprintf(stderr, "WARN: could not write sidecar: %v\n", err)
		return nil
	}
	fmt.Fprintf(stderr, "wrote sidecar %s (inspected_sha256=%s)\n",
		sidecar.SidecarPath(abs), shortSha(sha))
	return nil
}

// cachedH5adPass returns true (and prints a one-line cached-pass
// message) when the sidecar's H5ad.InspectedSha256 matches the file's
// current sha256 — meaning the previously-recorded PASS is still
// valid and anndata doesn't need to re-run.
func cachedH5adPass(cache *hashcache.Cache, h5adPath string, stdout io.Writer) (bool, error) {
	scPath := sidecar.SidecarPath(h5adPath)
	doc, err := readMaybe(scPath)
	if err != nil {
		return false, err
	}
	if doc == nil || doc.H5ad == nil || doc.H5ad.InspectedSha256 == "" {
		return false, nil
	}
	sum, err := cache.Sum(h5adPath)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(sum, doc.H5ad.InspectedSha256) {
		return false, nil
	}
	fmt.Fprintf(stdout,
		"✓ %s: cached pass (inspected %s, sha unchanged)\n"+
			"  re-run with --force to re-inspect.\n",
		h5adPath,
		doc.H5ad.InspectedAt.UTC().Format("2006-01-02"))
	return true, nil
}

// --- shared inspection helpers (also used by `mus check`) ------------------

// runH5adInspection is the shared inspection entry point used by both
// `mus h5ad check` and the `mus check` h5ad-freshness post-pass.
// `h5adPath` MUST be the data file (.h5ad), not a sidecar — callers
// resolve before calling.
//
// Streams the Python inspector's progress to cmd's stderr live.
// Caller decides what to do with the returned Report — print it,
// stamp the sidecar, both, or just inspect the HasHard() flag.
func runH5adInspection(cmd *cobra.Command, cache *hashcache.Cache,
	h5adPath string) (*h5ad.Report, error) {

	// 1. Vocabulary (HTTPS → cache → embedded).
	vocabRes, err := vocab.Load(cmd.Context(), vocab.LoadOptions{})
	if err != nil {
		return nil, fmt.Errorf("load vocabulary: %w", err)
	}

	// 2. Metadata from cascade (.env walked up from the h5ad's dir)
	//    + the data file's existing sidecar Bio/H5ad blocks (if any).
	dir := filepath.Dir(h5adPath)
	env, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load cascade for %s: %w", dir, err)
	}
	var bio *sidecar.Bio
	var h5 *sidecar.H5ad
	if doc, err := readMaybe(sidecar.SidecarPath(h5adPath)); err == nil && doc != nil {
		bio = doc.Bio
		h5 = doc.H5ad
	}
	meta := h5ad.MetadataFromCascade(env, bio, h5)

	insp := h5ad.NewPythonInspector()
	insp.ProgressOut = cmd.ErrOrStderr()
	return h5ad.RunChecks(cmd.Context(), h5adPath, h5ad.CheckOptions{
		Inspector:  insp,
		Vocabulary: vocabRes.Vocabulary,
		Metadata:   meta,
		ComputeSha256: func(p string) (string, error) {
			return cache.Sum(p)
		},
	})
}

// stampSidecar writes the h5ad_* shape block + the Bio block (if any
// metadata is set) into <h5ad>.mus, preserving other sidecar
// sections. `sha` is the sha256 of the .h5ad file at inspection
// time — recorded so a later `mus check` can decide whether the
// recorded PASS is still valid without re-running anndata.
func stampSidecar(r *h5ad.Report, sha string) error {
	scPath := sidecar.SidecarPath(r.H5adPath)
	doc, err := readMaybe(scPath)
	if err != nil {
		return err
	}
	if doc == nil {
		doc = &sidecar.Doc{}
	}
	// Stamp the file's sha256 if not already there (typical for users
	// who go straight to `mus h5ad check` without first running
	// `mus tag`). This is what `mus check` later compares against.
	if doc.File.Sha256 == "" && sha != "" {
		doc.File.Sha256 = sha
	}
	// File-shape block (h5ad-specific).
	doc.H5ad = &sidecar.H5ad{
		NObs:            r.Inspection.NObs,
		NVars:           r.Inspection.NVars,
		Layers:          append([]string(nil), r.Inspection.Layers...),
		Embedding:       embeddingLabel(r.Inspection),
		AnnDataVersion:  r.Inspection.AnnDataVersion,
		InspectedAt:     time.Now().UTC().Truncate(time.Second),
		InspectedSha256: sha,
	}
	if r.Metadata != nil {
		doc.H5ad.GeneIDType = r.Metadata.GeneIDType
	}
	// Bio block (broadly applicable) — only stamp if the metadata
	// carries anything; avoids polluting otherwise-empty sidecars.
	if m := r.Metadata; m != nil && hasAnyBio(m) {
		doc.Bio = &sidecar.Bio{
			Organism:    m.Organism,
			Celltype:    append([]string(nil), m.Celltype...),
			Technology:  m.Technology,
			Preparation: m.Preparation,
			Tissue:      append([]string(nil), m.Tissue...),
			Region:      append([]string(nil), m.Region...),
			Disease:     append([]string(nil), m.Disease...),
			Sex:         m.Sex,
			Title:       m.Title,
			Contributor: m.Contributor,
			Year:        m.Year,
			DOI:         m.DOI,
			Source:      m.Source,
			License:     m.License,
			Internal:    m.Internal,
			Accession:   m.Accession,
		}
	}
	return sidecar.Write(scPath, doc)
}

// hasAnyBio reports whether Metadata carries at least one
// broadly-applicable field — used to decide whether to emit a Bio
// block on the sidecar.
func hasAnyBio(m *h5ad.Metadata) bool {
	return m.Organism != "" || len(m.Celltype) > 0 || m.Technology != "" ||
		m.Preparation != "" || len(m.Tissue) > 0 || len(m.Region) > 0 ||
		len(m.Disease) > 0 || m.Sex != "" || m.Title != "" ||
		m.Contributor != "" || m.Year != 0 || m.DOI != "" ||
		m.Source != "" || m.License != "" || m.Internal != "" ||
		m.Accession != ""
}

// printReport renders a Report as human-readable output to w.
func printReport(w io.Writer, r *h5ad.Report) {
	if r.Inspection != nil {
		fmt.Fprintf(w, "\n%s\n", r.H5adPath)
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

// shortSha returns the first 12 chars of s for log lines.
func shortSha(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
