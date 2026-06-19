// Package de validates differential-expression result files for
// deposit into the anthive catalog.
//
// Scope (deliberate):
//   - format detection from column-name signatures (deseq2, edger,
//     scanpy, seurat, glmmseq, glmmtmb, long — see Format)
//   - row count + required-column validation
//   - distinct {term × contrast} computation for `long` deposits
//     where the depositor set `de_term=auto` / `de_contrast=auto`
//   - parquet rejection (no DE tool writes parquet natively;
//     ant-ingest handles conversion if needed)
//
// Out of scope: statistical sanity (effect sizes, p-value
// distributions). The catalog-side ingest does its own checks.
//
// The package is pure logic — it reads files (.tsv / .csv) but
// does not touch the network, the cascade, or the sidecar layer.
// `mus de check` (in internal/cli) wires it together with cascade
// loading + sidecar writing + (optional) cluster validation +
// (optional) vark cross-check.
package de

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CheckVersion is bumped when the validation logic changes in a
// way that invalidates previously-stamped PASSes. The check passes
// only if the recorded de_check_version matches this constant.
const CheckVersion = 1

// Format identifies the column-signature shape of a DE table.
type Format string

const (
	FormatDESeq2   Format = "deseq2"
	FormatEdgeR    Format = "edger"
	FormatScanpy   Format = "scanpy"
	FormatSeurat   Format = "seurat"
	FormatGLMMseq  Format = "glmmseq"
	FormatGLMMTMB  Format = "glmmtmb"
	FormatLong     Format = "long"
	FormatUnknown  Format = ""
)

// AllFormats lists every supported format, in detection priority
// order. Long is last because its signature (gene + effsize +
// pvalue + padj + term + contrast) is anthive-native and rare in
// the wild — checking it first would shadow more-common tool
// formats with the same `padj` column.
var AllFormats = []Format{
	FormatDESeq2, FormatEdgeR, FormatScanpy, FormatSeurat,
	FormatGLMMseq, FormatGLMMTMB, FormatLong,
}

// signatures lists the required column names per format. A file
// matches when EVERY name (case- and separator-insensitive — see
// normalizeColName) is present in the header row. Multiple
// formats can theoretically match a header; AllFormats's order
// settles ties (first match wins).
var signatures = map[Format][]string{
	FormatDESeq2:  {"baseMean", "log2FoldChange", "lfcSE", "padj"},
	FormatEdgeR:   {"logFC", "logCPM", "FDR"},
	FormatScanpy:  {"names", "logfoldchanges", "pvals_adj"},
	FormatSeurat:  {"avg_log2FC", "pct.1", "pct.2", "p_val_adj"},
	FormatGLMMseq: {"chisq", "df", "P_FixedEffects"},
	FormatGLMMTMB: {"Estimate", "Std. Error", "z value", "Pr(>|z|)"},
	FormatLong:    {"gene_id", "effsize", "pvalue", "padj", "term", "contrast"},
}

// Report is what Detect / Inspect return for one DE file.
type Report struct {
	Path          string
	Format        Format
	MatchedCols   []string // header columns that matched the signature
	HeaderCols    []string // every column in the header, in order
	RowCount      int64    // data rows (header excluded)
	TermSet       []string // distinct terms found (long format only)
	ContrastSet   []string // distinct contrasts found (long format only)
	TermIdx       int      // 0-based index of the `term` column (long only; -1 otherwise)
	ContrastIdx   int      // same for `contrast`
}

// Inspect reads `path` and returns a populated Report. Errors:
//
//   - parquet input → returns a typed ErrParquet with a clear hint
//   - file unreadable / empty header → wrapped error
//   - format unrecognised → Report with Format=="" and a populated
//     HeaderCols (so the caller can print "no match (saw: …)")
//
// Inspect is called by `mus de check` per file. For `long`
// format, Inspect ALSO computes the distinct {term × contrast}
// sets — this is the only path that reads the full body; other
// formats stop after the header.
//
// Compressed inputs (.tsv.gz / .csv.gz) are detected by extension
// and decompressed transparently — same format detection,
// validation, and long-format aggregation as the plain variant.
// The hash recorded in the sidecar is over the COMPRESSED bytes
// on disk (whatever the depositor uploaded).
func Inspect(path string) (*Report, error) {
	if isParquet(path) {
		return nil, &ErrParquet{Path: path}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var src io.Reader = f
	if isGzipped(path) {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("opening gzip stream for %s: %w", path, err)
		}
		defer gz.Close()
		src = gz
	}

	header, reader, delim, err := readHeader(src, path)
	if err != nil {
		return nil, err
	}

	rep := &Report{
		Path:        path,
		HeaderCols:  append([]string(nil), header...),
		TermIdx:     -1,
		ContrastIdx: -1,
	}
	rep.Format, rep.MatchedCols = detectFormat(header)

	// Count rows for every format. For `long`, also collect distinct
	// term + contrast values; the index lookup is set up here so the
	// loop below stays tight.
	if rep.Format == FormatLong {
		rep.TermIdx = indexOfNormalized(header, "term")
		rep.ContrastIdx = indexOfNormalized(header, "contrast")
	}
	rows, terms, contrasts, err := countRowsAndCollect(reader, delim, rep.TermIdx, rep.ContrastIdx)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", path, err)
	}
	rep.RowCount = rows
	if rep.Format == FormatLong {
		rep.TermSet = sortedSet(terms)
		rep.ContrastSet = sortedSet(contrasts)
	}
	return rep, nil
}

// Validate confirms the report describes a usable DE deposit.
// Returns nil on PASS, otherwise the first failure reason. Caller
// (`mus de check`) renders the error verbatim — keep messages
// depositor-friendly.
func Validate(rep *Report) error {
	if rep == nil {
		return fmt.Errorf("no report (internal error)")
	}
	if rep.Format == FormatUnknown {
		return fmt.Errorf(
			"format detection failed (saw columns: %s); "+
				"rename to match one of: %s — "+
				"or pass --format long if the schema is anthive-native",
			strings.Join(rep.HeaderCols, ", "),
			strings.Join(formatNames(), " | "))
	}
	if rep.RowCount == 0 {
		return fmt.Errorf("0 data rows after header — empty DE table is refused")
	}
	if rep.Format == FormatLong {
		if rep.TermIdx < 0 {
			return fmt.Errorf("long format requires a `term` column")
		}
		if rep.ContrastIdx < 0 {
			return fmt.Errorf("long format requires a `contrast` column")
		}
		if len(rep.TermSet) == 0 || len(rep.ContrastSet) == 0 {
			return fmt.Errorf("long format with no distinct term/contrast values found in body")
		}
	}
	return nil
}

// FormatNames returns the canonical names of all supported
// formats, in the order `--format` accepts.
func FormatNames() []string { return formatNames() }

func formatNames() []string {
	out := make([]string, 0, len(AllFormats))
	for _, f := range AllFormats {
		out = append(out, string(f))
	}
	return out
}

// MethodFromFormat returns the canonical anthive method name for a
// given format, used to auto-fill de_method when the depositor
// didn't set it explicitly. Returns "" for unknown formats.
func MethodFromFormat(f Format) string {
	switch f {
	case FormatDESeq2:
		return "DESeq2"
	case FormatEdgeR:
		return "edgeR"
	case FormatScanpy:
		return "scanpy.rank_genes_groups"
	case FormatSeurat:
		return "Seurat.FindMarkers"
	case FormatGLMMseq:
		return "glmmSeq"
	case FormatGLMMTMB:
		return "glmmTMB"
	case FormatLong:
		return "" // long is a transport format, not a method
	}
	return ""
}

// ParseFormat parses a --format flag value into a Format. Accepts
// case-insensitive canonical names plus a few common aliases.
func ParseFormat(raw string) (Format, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return FormatUnknown, nil
	case "deseq2", "deseq", "deseq-2":
		return FormatDESeq2, nil
	case "edger", "edge-r":
		return FormatEdgeR, nil
	case "scanpy", "rank_genes_groups":
		return FormatScanpy, nil
	case "seurat", "findmarkers":
		return FormatSeurat, nil
	case "glmmseq":
		return FormatGLMMseq, nil
	case "glmmtmb":
		return FormatGLMMTMB, nil
	case "long", "anthive":
		return FormatLong, nil
	}
	return FormatUnknown, fmt.Errorf(
		"unknown --format value %q (use one of: %s)",
		raw, strings.Join(formatNames(), ", "))
}

// ErrParquet is returned by Inspect for .parquet input.
type ErrParquet struct{ Path string }

func (e *ErrParquet) Error() string {
	return fmt.Sprintf(
		"%s: parquet not accepted in DE deposits — convert to .tsv first "+
			"(no DE tool writes parquet natively; pandas/pyarrow can convert "+
			"if you have parquet from upstream)", e.Path)
}

// IsParquetError reports whether err is an ErrParquet (for callers
// that want to render the parquet-rejection message specially).
func IsParquetError(err error) bool {
	_, ok := err.(*ErrParquet)
	return ok
}

func isParquet(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".parquet" || ext == ".pq"
}

// readHeader reads the first line from src, splits on the path's
// likely delimiter (TSV → tab; CSV → comma; .gz suffix stripped
// first), and returns the headers + a buffered reader positioned
// at the first data row + the chosen delimiter.
//
// src is an io.Reader rather than *os.File because the caller may
// have wrapped a *os.File in a gzip.Reader for compressed input.
func readHeader(src io.Reader, path string) ([]string, *bufio.Reader, rune, error) {
	delim := delimiterFromPath(path)
	br := bufio.NewReader(src)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, nil, 0, fmt.Errorf("reading header: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, nil, 0, fmt.Errorf("empty header line in %s", path)
	}
	header := splitDelim(line, delim)
	if len(header) < 2 {
		return nil, nil, 0, fmt.Errorf(
			"header looks malformed (only %d field(s) — wrong delimiter?): %q",
			len(header), line)
	}
	return header, br, delim, nil
}

// delimiterFromPath picks tab / comma based on the data-file
// extension. Strips a trailing .gz first so .tsv.gz / .csv.gz
// resolve correctly.
func delimiterFromPath(path string) rune {
	stripped := stripGzExt(path)
	switch strings.ToLower(filepath.Ext(stripped)) {
	case ".tsv", ".txt":
		return '\t'
	case ".csv":
		return ','
	}
	// default to tab — DE tooling skews TSV
	return '\t'
}

// isGzipped reports whether the path ends in `.gz` (compressed
// DE input). We trust the extension; gzip's magic bytes would
// confirm it but the extension is the contract with the catalog
// (anthive reads .tsv.gz vs .tsv as the same logical resource).
func isGzipped(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".gz")
}

// stripGzExt drops a trailing .gz suffix (case-insensitive),
// returning the inner extension carrier ("foo.tsv.gz" → "foo.tsv").
// Used by delimiterFromPath so .tsv.gz routes through the .tsv
// branch.
func stripGzExt(path string) string {
	if isGzipped(path) {
		return path[:len(path)-len(".gz")]
	}
	return path
}

// splitDelim splits a line on `delim`. We use encoding/csv for the
// comma path (handles quoted fields with embedded commas — common
// in long-format `contrast` values like "patient:sexM_vs_patient:sexF").
// Tab-delimited input is split with a fast strings.Split since DE
// tools never quote tab-delimited.
func splitDelim(line string, delim rune) []string {
	if delim == ',' {
		r := csv.NewReader(strings.NewReader(line))
		r.LazyQuotes = true
		r.FieldsPerRecord = -1
		row, err := r.Read()
		if err != nil {
			// Fallback to plain split — better than failing outright.
			return strings.Split(line, ",")
		}
		return row
	}
	return strings.Split(line, string(delim))
}

// detectFormat matches the header against every signature in
// AllFormats's order. Returns the first matching Format + the
// subset of header columns that satisfied the signature (for
// verbose display). Case + separator insensitive (so `log2FC` and
// `LOG_2_FC` both match `logFC` etc).
func detectFormat(header []string) (Format, []string) {
	normHeader := make(map[string]string, len(header))
	for _, h := range header {
		normHeader[normalizeColName(h)] = h
	}
	for _, f := range AllFormats {
		sig := signatures[f]
		matched := make([]string, 0, len(sig))
		ok := true
		for _, want := range sig {
			orig, present := normHeader[normalizeColName(want)]
			if !present {
				ok = false
				break
			}
			matched = append(matched, orig)
		}
		if ok {
			return f, matched
		}
	}
	return FormatUnknown, nil
}

// normalizeColName lowercases the name and strips out
// '.', '_', '-', ' ' so column-naming style differences don't
// trip detection. DESeq2's `log2FoldChange`, scanpy's
// `logfoldchanges`, an analyst's `log_2_fc` all collapse to
// `log2foldchange{s}` after this; the signatures use canonical
// tool names so the post-normalize comparison still distinguishes.
func normalizeColName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '.', '_', '-', ' ':
			// drop
		default:
			b.WriteRune(toLowerRune(r))
		}
	}
	return b.String()
}

// toLowerRune is ASCII-only lowercase. Column names are ASCII in
// every tool we care about; unicode.ToLower would pull in the
// unicode tables for no good reason.
func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// indexOfNormalized returns the 0-based position of `target`
// (post-normalize) in `header`, or -1 if absent.
func indexOfNormalized(header []string, target string) int {
	t := normalizeColName(target)
	for i, h := range header {
		if normalizeColName(h) == t {
			return i
		}
	}
	return -1
}

// countRowsAndCollect walks the body of the file, counting data
// rows and (when termIdx / contrastIdx ≥ 0) collecting the
// distinct values for those columns. Built for the `long` format's
// term/contrast=auto path; other formats pass -1 and we just count.
func countRowsAndCollect(br *bufio.Reader, delim rune,
	termIdx, contrastIdx int) (int64, map[string]struct{}, map[string]struct{}, error) {

	var rows int64
	terms := map[string]struct{}{}
	contrasts := map[string]struct{}{}
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed != "" {
				rows++
				if termIdx >= 0 || contrastIdx >= 0 {
					fields := splitDelim(trimmed, delim)
					if termIdx >= 0 && termIdx < len(fields) {
						terms[strings.TrimSpace(fields[termIdx])] = struct{}{}
					}
					if contrastIdx >= 0 && contrastIdx < len(fields) {
						contrasts[strings.TrimSpace(fields[contrastIdx])] = struct{}{}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return rows, terms, contrasts, fmt.Errorf("read: %w", err)
		}
	}
	return rows, terms, contrasts, nil
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
