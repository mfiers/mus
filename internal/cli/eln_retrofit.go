package cli

// `mus eln retrofit` — one-experiment-at-a-time retrofit of legacy
// "mus: uploaded ... to iRODS" COMMENT sections into proper SAMPLESOUT
// sections with structured sample records.
//
// Design (matches doc/... and the plan discussed with the maintainer):
//
//   1. Fetch the target experiment's sections; select the COMMENT ones
//      whose header matches `mus: uploaded ...`.
//   2. Parse each COMMENT's HTML content into per-file rows:
//        (iRODS path, PURL, basename, sha256, kind, size, upload_data,
//         upload_user, upload_from_server, upload_from_path, message,
//         data_project). Older HTML shapes may omit fields — parser
//         is defensive; missing values are left blank.
//   3. Live-verify each object via `iron stat -j` — pulls the current
//      path, size, checksum, and mus_* AVU triplets (authoritative for
//      upload_message / upload_data / data_project / etc.). AVU wins
//      over HTML on conflict.
//   4. Compute the canonical mus path per current heuristics:
//        <irods_home>/projects/<YEAR>/<data_project>/<safe_experiment_name>/<basename>
//      YEAR comes from data_project's suffix (e.g. Calafate2019 → 2019).
//      If current path != canonical, a mv is planned.
//   5. Print the plan (dry-run — default).
//   6. On --apply: `iron mv` any out-of-place objects, create a fresh
//      SAMPLESOUT section, POST /samples per row, batch-link, then
//      annotate + archive the old COMMENTs.
//
// Idempotent-ish: on re-run, samples that ALREADY exist as linked to
// the target experiment (matched by iRODS path in the `path` meta) are
// reported as `↷ already retrofitted` and skipped. The one-shot COMMENT
// archive step is safe to repeat — DELETEing an already-archived
// section is a no-op on eLabNext.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/defaults"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/elnmap"
	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/secret"
)

// --- command wiring ---------------------------------------------------------

func newELNRetrofitCmd() *cobra.Command {
	var apply bool
	var archiveComments bool
	var moveDanglers bool
	var sweepLegacy bool
	var legacySource string
	var treePath string
	var dataProject string
	var forceDataProject bool
	cmd := &cobra.Command{
		Use:   "retrofit [EXPERIMENT_ID]",
		Short: "convert legacy mus 'uploaded ...' COMMENT sections into structured samples",
		Long: "Reads legacy `mus: uploaded ...` HTML COMMENT sections on a target\n" +
			"experiment, cross-checks the referenced iRODS objects against live\n" +
			"iron stat, moves any pre-0.2.33-layout objects to the current canonical\n" +
			"location, creates a fresh SAMPLESOUT section with one Sample record per\n" +
			"file (auto-fill from AVU metadata), and archives the old COMMENTs with\n" +
			"a note pointing at the new section.\n\n" +
			"Dry-run by default — prints every operation that would fire. Pass\n" +
			"--apply to actually execute.\n\n" +
			"Uses eln_experiment_id from the mus.cfg cascade if no ID is given.\n" +
			"eln_sample_location / eln_irods_sample_location keys select where the\n" +
			"created samples land (cascade > secret). Same auto-fill machinery as\n" +
			"`mus eln sample` and the auto-register on `mus irods upload`.\n\n" +
			"Non-destructive dry-run is safe to run on any experiment; --apply\n" +
			"invokes `iron mv` (fast namespace rename, no bytes move) and creates\n" +
			"new ELN samples. Old comments are archived (soft-delete, restorable\n" +
			"via POST /experiments/sections/{id}/restore) unless --no-archive-comments.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate the flag value up-front so we fail fast
			// with a clear message rather than in the middle of
			// the retrofit.
			if strings.TrimSpace(dataProject) != "" {
				if err := dataproject.ValidateName(dataProject); err != nil {
					return fmt.Errorf("--data-project %q: %w", dataProject, err)
				}
			}
			return runELNRetrofit(cmd, args, apply, archiveComments, moveDanglers,
				sweepLegacy, legacySource, treePath, dataProject, forceDataProject)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false,
		"actually execute the plan (default: dry-run only)")
	cmd.Flags().StringVar(&dataProject, "data-project", "",
		"data_project for the retrofit. When set AND mus has NO value from any "+
			"other source (HTML / AVU / elnmap), applies this value to every row "+
			"and skips the interactive prompt. When mus DOES have a value from "+
			"another source AND it disagrees, retrofit halts loudly — pass "+
			"--force-data-project to override. Format: NameYear (e.g. Fiers2025). "+
			"Persisted to elnmap so subsequent runs pick it up automatically.")
	cmd.Flags().BoolVar(&forceDataProject, "force-data-project", false,
		"override the safety halt when --data-project disagrees with an "+
			"existing HTML/AVU/elnmap value. Use only when you're sure the "+
			"existing value is wrong.")
	cmd.Flags().BoolVar(&sweepLegacy, "sweep-legacy", false,
		"after the main retrofit, sweep the predicted LEGACY source collection "+
			"(<irods_home>/lab_data_archive/<sanitize(study_name)>/<sanitize(experiment_name)>/) "+
			"for files + sub-collections mus didn't already handle. These are "+
			"almost certainly this experiment's data (the path proves it) so "+
			"mus asks for confirmation, then moves them to canonical + creates "+
			"sample records. Sub-collections (folders) are supported — moved "+
			"whole; folder-level sha256 is left blank (run `mus check` later "+
			"to compute).")
	cmd.Flags().StringVar(&legacySource, "legacy-source", "",
		"override the predicted legacy source collection with an explicit "+
			"iRODS path (e.g. /gbiomed/home/BADS/lab_data_archive/foo/bar). "+
			"Useful when the sanitisation of study/experiment names doesn't "+
			"match what actually exists on iRODS.")
	cmd.Flags().BoolVar(&archiveComments, "archive-comments", true,
		"annotate + archive the old COMMENT sections after successful retrofit")
	cmd.Flags().BoolVar(&moveDanglers, "move-danglers", false,
		"after the main retrofit, sweep each source collection for leftover files. "+
			"Any leftover data-object whose AVU (mus_eln_experiment_id OR "+
			"mgs.project.experiment_id) matches THIS experiment is moved to the "+
			"canonical location and gets its own sample record in a fresh "+
			"'reclaimed danglers' SAMPLESOUT section. Files with no AVU or a "+
			"different experiment ID are NOT moved — they're reported for manual "+
			"review. Sub-collections are never auto-moved (retrofit doesn't "+
			"recurse — investigate them by hand).")
	cmd.Flags().StringVar(&treePath, "tree", "",
		"optional accelerator: path to an iron_tree NDJSON dump (name/checksum). "+
			"Without --tree, mus resolves relocated files via live `iron query` on "+
			"the basename (DATA_NAME is indexed, so it's fast). Pass --tree only "+
			"when you need sha256-based lookups (DATA_CHECKSUM is unindexed and "+
			"scans the whole catalog — slow live but O(1) from the tree).")
	return cmd
}

// --- iron_tree lookup ------------------------------------------------------

// treeIndex holds sha256 → current iRODS path lookups from an iron_tree
// NDJSON dump. Loaded once per retrofit invocation (~200 MB RAM for a
// 1.5M-row dump).
type treeIndex struct {
	// bySha keyed by sha256 hex (data-object checksums only —
	// collections don't have one). One sha may appear multiple times
	// in iRODS (replicas / dedup), but for retrofit "any current
	// path" is enough — first hit wins.
	bySha map[string]string
	// byBasename maps trailing-path-segment → paths, for name-based
	// fallback when a folder has no checksum (collections).
	byBasename map[string][]string
}

// loadTreeIndex streams an iron_tree NDJSON dump and builds sha256 +
// basename indexes. Uses streaming JSON decode so the whole file
// never lives in memory at once — the built maps are what stay.
func loadTreeIndex(path string) (*treeIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open tree dump %s: %w", path, err)
	}
	defer f.Close()
	idx := &treeIndex{
		bySha:      map[string]string{},
		byBasename: map[string][]string{},
	}
	// One JSON object per line — bufio.Scanner + json.Unmarshal is
	// enough (each row is small). Bump the buffer to handle any
	// unusually-long path row.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	type row struct {
		Name     string `json:"name"`
		Checksum string `json:"checksum"`
	}
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.Checksum != "" {
			// First hit wins — replicas end up as duplicates,
			// any path is functional for our lookup.
			if _, ok := idx.bySha[r.Checksum]; !ok {
				idx.bySha[r.Checksum] = r.Name
			}
		}
		if base := lastPathSegment(r.Name); base != "" {
			idx.byBasename[base] = append(idx.byBasename[base], r.Name)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan tree dump: %w", err)
	}
	return idx, nil
}

// lookupBySha returns the current iRODS path of the object with the
// given checksum, or empty if no match.
func (t *treeIndex) lookupBySha(sha string) string {
	if t == nil || sha == "" {
		return ""
	}
	return t.bySha[sha]
}

// lookupByBasename returns paths whose last segment matches basename.
// Empty result when the tree is nil, no match found, or basename is empty.
func (t *treeIndex) lookupByBasename(basename string) []string {
	if t == nil || basename == "" {
		return nil
	}
	return t.byBasename[basename]
}

// dropMusPrefix returns the input path with a `/mus/` segment
// stripped just after the zone home. Handles the pre-Go Python
// tool's habit of writing every upload under
// `<home>/mus/lab_data_archive/...` while the modern tree drops
// the `/mus/` segment (`<home>/lab_data_archive/...`). Empty
// return means the input didn't match the specific shape.
func dropMusPrefix(p string) string {
	// Only strip when the /mus/ appears as a discrete segment
	// immediately after a 3-segment zone-home prefix like
	// /gbiomed/home/BADS/mus/... . Broader stripping would
	// corrupt paths that happen to contain /mus/ elsewhere.
	i := strings.Index(p, "/mus/")
	if i <= 0 {
		return ""
	}
	// Verify the segment before /mus/ looks like a zone-home
	// (three-segment prefix, e.g. /gbiomed/home/BADS).
	before := p[:i]
	if strings.Count(before, "/") != 3 {
		return ""
	}
	return before + p[i+len("/mus"):]
}

// longestSuffixMatch picks the candidate whose path shares the
// longest trailing segment sequence with target. Ties are broken by
// picking the shortest total path (least specific match wins on
// suffix equality — that's usually the right answer for retrofit).
// Returns "" when there are no candidates with any suffix match.
func longestSuffixMatch(candidates []string, target string) string {
	targetSegs := strings.Split(target, "/")
	bestScore := 0
	bestPath := ""
	for _, c := range candidates {
		segs := strings.Split(c, "/")
		score := 0
		for i := 1; i <= len(segs) && i <= len(targetSegs); i++ {
			if segs[len(segs)-i] == targetSegs[len(targetSegs)-i] {
				score++
			} else {
				break
			}
		}
		if score > bestScore || (score == bestScore && bestPath != "" && len(c) < len(bestPath)) {
			bestScore = score
			bestPath = c
		}
	}
	if bestScore < 2 {
		// A single-segment suffix match (just the basename) is
		// too weak — could easily be a coincidence. Require ≥2
		// matching trailing segments.
		return ""
	}
	return bestPath
}

func lastPathSegment(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return p
	}
	return p[i+1:]
}

// --- plan data types --------------------------------------------------------

// retrofitAction represents ONE iRODS object → ONE new ELN sample.
// A retrofit plan is a []retrofitAction plus a list of source COMMENT
// section IDs to archive on completion.
type retrofitAction struct {
	// From HTML parse (may be partial on older comments).
	SourceCommentJID int64
	Message          string // -m message from the original upload
	UploadUser       string // e.g. u0089478
	UploadHost       string // e.g. gbw-d-l0067
	UploadTime       time.Time
	UploadFromPath   string // local abs at upload time (from Source: line)
	DataProject      string
	Kind             string // file / folder / archive

	// From HTML parse of the row anchor (initial guess for iRODS path).
	CommentPath   string
	CommentPURL   string
	CommentSha256 string
	Basename      string
	SizeHuman     string // e.g. "25.5 GiB" — parsed HTML

	// Populated by liveVerify.
	IRODSPath     string // current path from iron stat (may differ from CommentPath if the file was moved)
	CatalogID     int64
	SizeBytes     int64
	Sha256        string
	AVU           map[string][]string

	// Computed.
	CanonicalPath string
	NeedsMove     bool

	// Status.
	Skipped      bool   // already retrofitted (sample exists on the experiment already)
	SkipReason   string
	IronStatErr  error  // set if iron stat failed — action is skipped on --apply
}

// --- HTML parser ------------------------------------------------------------

// parseUploadComment detects the format of an ELN COMMENT's HTML and
// dispatches to the matching parser. Two known formats:
//
//   1. Modern (`mus: uploaded ... to iRODS` header) — a full HTML
//      table with per-file rows, blockquote message, uploader
//      signature, and explicit data_project. Produced by
//      postUploadCommentToELN before it was retired.
//   2. Legacy (arbitrary descriptive header) — a text preamble
//      ("Upload by USER from HOST:PATH at TIMESTAMP.") followed by
//      an `<ol><li>` list, each row carrying `<b>name</b>` +
//      `<a href="mango.kuleuven.be/data-object/view/PATH">[IRODS]</a>`
//      + `(shasum: HEX)`. Produced by the pre-Go Python `mus`.
//
// Both dispatchers return one retrofitAction per uploaded file/folder
// referenced in the section. Sections in neither format return nil
// so the retrofit skips them cleanly.
func parseUploadComment(commentJID int64, htmlBody string) []retrofitAction {
	switch {
	case reModernSignature.MatchString(htmlBody):
		return parseModernUploadComment(commentJID, htmlBody)
	case reLegacySignature.MatchString(htmlBody):
		return parseLegacyUploadComment(commentJID, htmlBody)
	}
	return nil
}

// Signatures for cheap format detection (checked against the raw HTML
// once per candidate COMMENT).
var (
	reModernSignature = regexp.MustCompile(`<strong>Upload message:</strong>`)
	reLegacySignature = regexp.MustCompile(`^Upload by \S+`)
)

// parseModernUploadComment handles the modern HTML-table format.
// (Was previously the whole of parseUploadComment; renamed on the
// legacy-format add.)
func parseModernUploadComment(commentJID int64, htmlBody string) []retrofitAction {
	// Header lines (once per comment; shared across all rows).
	msg := regexMatch(reMessage, htmlBody)
	user := regexMatch(reUploaderUser, htmlBody)
	host := regexMatch(reUploaderHost, htmlBody)
	tsRaw := regexMatch(reUploaderTs, htmlBody)
	dp := regexMatch(reDataProject, htmlBody)
	srcHost, srcPath := regexMatchPair(reSource, htmlBody)
	_ = srcHost // recorded in host if uploader didn't include one

	var ts time.Time
	if tsRaw != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", tsRaw); err == nil {
			ts = t.UTC()
		}
	}

	// Row anchors — each carries iRODS path (title=), PURL (href=), basename.
	var rows []retrofitAction
	for _, m := range reRowAnchor.FindAllStringSubmatch(htmlBody, -1) {
		purl := html.UnescapeString(m[1])
		irodsPath := html.UnescapeString(m[2])
		basename := html.UnescapeString(m[3])
		// The kind cell is the FIRST <td> in the same <tr>. We locate
		// the anchor's <tr> span and pull the leading <td>.
		kind := extractKindNear(htmlBody, m[0])
		// The sha256 sits in the LAST <code title="..."> AFTER the
		// anchor and BEFORE the next </tr>.
		sha := extractShaNear(htmlBody, m[0])
		// Size cell — best-effort; not load-bearing, iron stat gives the real bytes.
		size := extractSizeNear(htmlBody, m[0])

		rows = append(rows, retrofitAction{
			SourceCommentJID: commentJID,
			Message:          html.UnescapeString(msg),
			UploadUser:       user,
			UploadHost:       host,
			UploadTime:       ts,
			UploadFromPath:   srcPath, // partial — this is the cwd at upload, not the full source path
			DataProject:      dp,
			Kind:             kind,
			CommentPath:      irodsPath,
			CommentPURL:      purl,
			CommentSha256:    sha,
			Basename:         basename,
			SizeHuman:        size,
		})
	}
	return rows
}

// parseLegacyUploadComment handles the pre-Go Python mus format:
//
//     Upload by USER
//     from HOST:CWD
//     at YYYY-MM-DD HH:MM:SS.
//     <ol>
//       <li><b>NAME</b> <a href="MANGO_URL">[IRODS]</a> <span ...>(shasum: HEX)</span></li>
//       ...
//     </ol>
//
// URLs are path-based (not PURLs). The URL prefix distinguishes kind:
//   `/data-object/view/PATH` → file
//   `/collection/browse/PATH` → folder
// mango.kuleuven.be is stripped to recover the iRODS path.
//
// Legacy comments carry no `data_project` explicitly and no
// PURL — both are filled from AVU / elnmap / iron stat downstream.
func parseLegacyUploadComment(commentJID int64, htmlBody string) []retrofitAction {
	user := regexMatch(reLegacyUser, htmlBody)
	host, srcPath := regexMatchPair(reLegacyFrom, htmlBody)
	tsRaw := regexMatch(reLegacyTs, htmlBody)
	var ts time.Time
	if tsRaw != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", tsRaw); err == nil {
			ts = t.UTC()
		}
	}

	var rows []retrofitAction
	for _, m := range reLegacyRow.FindAllStringSubmatch(htmlBody, -1) {
		basename := html.UnescapeString(m[1])
		url := html.UnescapeString(m[2])
		shaRaw := strings.TrimSpace(m[3])
		irodsPath, kind := legacyPathFromURL(url)
		if irodsPath == "" {
			continue // unrecognised URL shape — skip
		}
		sha := ""
		if shaRaw != "" && !strings.EqualFold(shaRaw, "none") {
			sha = strings.ToLower(shaRaw)
		}
		rows = append(rows, retrofitAction{
			SourceCommentJID: commentJID,
			// Legacy comments have no `-m` message; the section
			// header (descriptive text like "pooya scripts") is
			// the closest equivalent. The retrofit runner will
			// treat SectionHeader as the description if this
			// field is empty (see applyRetrofit).
			Message:        "",
			UploadUser:     user,
			UploadHost:     host,
			UploadTime:     ts,
			UploadFromPath: srcPath,
			DataProject:    "", // filled downstream via elnmap fallback
			Kind:           kind,
			CommentPath:    irodsPath,
			CommentPURL:    "", // legacy comments carry no PURL
			CommentSha256:  sha,
			Basename:       basename,
			SizeHuman:      "", // legacy comments carry no size
		})
	}
	return rows
}

// legacyPathFromURL extracts the iRODS object path from a
// mango.kuleuven.be browse-URL as embedded in a legacy comment. The
// URL prefix disambiguates the kind:
//
//   .../data-object/view/PATH → file
//   .../collection/browse/PATH → folder
//
// Returns ("", "") when the URL doesn't match either shape.
func legacyPathFromURL(url string) (path, kind string) {
	if m := reLegacyURLFile.FindStringSubmatch(url); len(m) == 2 {
		return "/" + m[1], "file"
	}
	if m := reLegacyURLColl.FindStringSubmatch(url); len(m) == 2 {
		return "/" + m[1], "folder"
	}
	return "", ""
}

var (
	reLegacyUser    = regexp.MustCompile(`^Upload by (\S+)`)
	reLegacyFrom    = regexp.MustCompile(`from ([^:\s]+):(\S+)`)
	reLegacyTs      = regexp.MustCompile(`at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\.`)
	reLegacyRow     = regexp.MustCompile(
		`<li>\s*<b>([^<]+)</b>\s*<a href="([^"]+)">\[IRODS\]</a>\s*<span[^>]*>\s*\(shasum:\s*([^)]*)\)\s*</span>\s*</li>`)
	reLegacyURLFile = regexp.MustCompile(`^https?://[^/]+/data-object/view/(.+?)/?$`)
	reLegacyURLColl = regexp.MustCompile(`^https?://[^/]+/collection/browse/(.+?)/?$`)
)

// Regex catalogue — matches the HTML mus's postUploadCommentToELN
// emitted before it was retired. Kept as package-level vars so
// compilation happens once.
var (
	reMessage = regexp.MustCompile(
		`<blockquote>\s*<p>\s*<strong>Upload message:</strong>\s*(.*?)\s*</p>\s*</blockquote>`)
	reUploaderUser = regexp.MustCompile(
		`Uploaded via <code>mus</code> by <code>([^<]+)</code>@`)
	reUploaderHost = regexp.MustCompile(
		`Uploaded via <code>mus</code> by <code>[^<]+</code>@<code>([^<]+)</code>`)
	reUploaderTs = regexp.MustCompile(
		`Uploaded via <code>mus</code>[^—]+ at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC`)
	reDataProject = regexp.MustCompile(
		`<p>\s*data_project\s*=\s*<code>([^<]+)</code>\s*</p>`)
	reSource = regexp.MustCompile(
		`<p>\s*Source:\s*<code>([^:]+):([^<]+)</code>\s*</p>`)
	reRowAnchor = regexp.MustCompile(
		`<a href="(https://[^"]+/PID/[^"]+)"\s+title="([^"]+)">\s*<code>([^<]+)</code>\s*</a>`)
	reRowKind = regexp.MustCompile(
		`<tr>\s*<td>(file|folder|archive)</td>`)
	reRowSha = regexp.MustCompile(
		`<code title="([0-9a-f]{64})">`)
	reRowSize = regexp.MustCompile(
		`<td>(\d[\d\.]*\s*[KMGT]?i?B)</td>\s*<td>\s*<code title="[0-9a-f]{64}">`)
)

func regexMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func regexMatchPair(re *regexp.Regexp, s string) (string, string) {
	m := re.FindStringSubmatch(s)
	if len(m) < 3 {
		return "", ""
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
}

// extractKindNear looks backwards from the anchor's start-position to
// find the nearest preceding "<tr><td>KIND</td>" — that's the row's
// kind cell. Returns "" on any parse failure.
func extractKindNear(htmlBody, anchorMatch string) string {
	idx := strings.Index(htmlBody, anchorMatch)
	if idx < 0 {
		return ""
	}
	// Look at the ~200 bytes BEFORE the anchor.
	start := idx - 200
	if start < 0 {
		start = 0
	}
	pre := htmlBody[start:idx]
	m := reRowKind.FindStringSubmatch(pre)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// extractShaNear looks forward from the anchor's end-position for the
// FIRST <code title="{64 hex}"> — that's the sha256 for this row.
// Bounded to 800 bytes to stay within one <tr>.
func extractShaNear(htmlBody, anchorMatch string) string {
	idx := strings.Index(htmlBody, anchorMatch)
	if idx < 0 {
		return ""
	}
	from := idx + len(anchorMatch)
	end := from + 800
	if end > len(htmlBody) {
		end = len(htmlBody)
	}
	m := reRowSha.FindStringSubmatch(htmlBody[from:end])
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// extractSizeNear pulls the human-formatted size cell that sits
// immediately before the sha256 cell. Not load-bearing — iron stat
// gives the exact byte count on live-verify.
func extractSizeNear(htmlBody, anchorMatch string) string {
	idx := strings.Index(htmlBody, anchorMatch)
	if idx < 0 {
		return ""
	}
	from := idx + len(anchorMatch)
	end := from + 800
	if end > len(htmlBody) {
		end = len(htmlBody)
	}
	m := reRowSize.FindStringSubmatch(htmlBody[from:end])
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// --- live verify ------------------------------------------------------------

// liveVerify calls `iron stat -j` on the parsed CommentPath, copies
// the current path / size / catalog ID / AVU into the action. On
// stat failure at the original path (legacy comments often point at
// paths that no longer exist), tree lookup by sha256 or basename is
// attempted before giving up. On final failure, IronStatErr is set —
// the caller reports and skips the action on --apply.
func liveVerify(ctx context.Context, client *iron.Client, tree *treeIndex, act *retrofitAction) {
	info, err := client.Stat(ctx, act.CommentPath)
	if err != nil {
		// Try relocation strategies before flagging as gone.
		// Order (cheapest / most specific first):
		//   1. `/mus/`-dropped path — the pre-Go Python tool
		//      prefixed everything under `/mus/`; the modern
		//      tree drops it. Deterministic guess, no lookup.
		//   2. Tree lookup by sha256 (only if --tree provided).
		//      DATA_CHECKSUM is NOT indexed in iCat — a live
		//      query is a full scan that times out on the BADS
		//      catalog. Tree gives O(1) sha256 → path.
		//   3. Live iron query by basename (DATA_NAME IS
		//      indexed — instant). Filtered by longest-suffix
		//      match against the source path so we don't
		//      grab an unrelated file with the same basename.
		//   4. Tree lookup by basename (accelerator when
		//      --tree is provided — avoids the network call).
		candidates := []string{}
		if guess := dropMusPrefix(act.CommentPath); guess != "" && guess != act.CommentPath {
			candidates = append(candidates, guess)
		}
		if tree != nil && act.CommentSha256 != "" {
			if p := tree.lookupBySha(act.CommentSha256); p != "" {
				candidates = append(candidates, p)
			}
		}
		if tree != nil {
			if hits := tree.lookupByBasename(act.Basename); len(hits) > 0 {
				if best := longestSuffixMatch(hits, act.CommentPath); best != "" {
					candidates = append(candidates, best)
				}
			}
		} else if act.Basename != "" {
			// No tree — hit iron live. Fast because DATA_NAME is
			// indexed. Ignored on error so a transient network
			// hiccup doesn't kill the retrofit.
			if hits, qerr := client.FindByName(ctx, act.Basename); qerr == nil && len(hits) > 0 {
				if best := longestSuffixMatch(hits, act.CommentPath); best != "" {
					candidates = append(candidates, best)
				}
			}
		}
		for _, resolvedPath := range candidates {
			if resolvedPath == act.CommentPath {
				continue
			}
			info2, err2 := client.Stat(ctx, resolvedPath)
			if err2 == nil {
				info = info2
				err = nil
				act.CommentPath = resolvedPath
				break
			}
		}
	}
	if err != nil {
		act.IronStatErr = err
		return
	}
	act.IRODSPath = info.Name
	act.SizeBytes = info.Size
	act.CatalogID = info.ID
	act.AVU = info.MetadataMap()

	// AVU wins over HTML on shared fields (message, timestamps).
	if v := firstAVU(act.AVU, "mus_upload_message"); v != "" {
		act.Message = v
	}
	if v := firstAVU(act.AVU, "mus_data_project"); v != "" {
		act.DataProject = v
	}
	if v := firstAVU(act.AVU, "mus_uploaded_at"); v != "" {
		if t, terr := time.Parse(time.RFC3339, v); terr == nil {
			act.UploadTime = t.UTC()
		}
	}
	// Prefer AVU sha256 over the HTML short form (which we already
	// have in full via CommentSha256 for the title=attr rows, but
	// double-check).
	for _, key := range []string{"mus_sha256", "mus_archive_sha256", "mus_recursive_sha256"} {
		if v := firstAVU(act.AVU, key); v != "" {
			act.Sha256 = v
			break
		}
	}
	if act.Sha256 == "" {
		act.Sha256 = act.CommentSha256
	}
	// Creator on iRODS is the upload user of record; falls back to
	// what the HTML said.
	if info.Creator != "" {
		act.UploadUser = info.Creator
	}
}

func firstAVU(m map[string][]string, key string) string {
	if vs, ok := m[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// --- canonical path --------------------------------------------------------

// canonicalPathFor computes where the current mus version would place
// this basename given the AVU's data_project + eln_experiment_name.
// Mirrors resolveIRODSCollection but without the interactive env
// lookup — we already have the raw values from AVU / HTML.
func canonicalPathFor(env *config.Env, dataProjectAVU, expNameAVU, basename string) (string, error) {
	if dataProjectAVU == "" {
		return "", fmt.Errorf("cannot compute canonical path: data_project unknown")
	}
	if expNameAVU == "" {
		return "", fmt.Errorf("cannot compute canonical path: eln_experiment_name unknown")
	}
	if err := dataproject.ValidateName(dataProjectAVU); err != nil {
		return "", fmt.Errorf("data_project %q is invalid: %w", dataProjectAVU, err)
	}
	year := dataproject.Year(dataProjectAVU)
	if year == "" {
		return "", fmt.Errorf("no year suffix on data_project %q", dataProjectAVU)
	}
	home := env.String("irods_home")
	if home == "" {
		home = defaults.IRODSHome
	}
	if home == "" {
		return "", fmt.Errorf("irods_home not set (no default)")
	}
	safe := dataproject.SanitizeForPath(expNameAVU)
	if safe == "" {
		return "", fmt.Errorf("safe experiment name empty after sanitising %q", expNameAVU)
	}
	return strings.TrimRight(home, "/") + "/projects/" + year + "/" +
		dataProjectAVU + "/" + safe + "/" + basename, nil
}

// --- runner ---------------------------------------------------------------

func runELNRetrofit(cmd *cobra.Command, args []string, apply, archiveComments, moveDanglers, sweepLegacy bool,
	legacySource, treePath, dataProjectOverride string, forceDataProject bool) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}

	// Resolve experiment ID.
	var expID int64
	if len(args) == 1 {
		expID, err = parseELNExperimentID(args[0])
		if err != nil {
			return fmt.Errorf("invalid experiment ID %q: %w", args[0], err)
		}
	} else {
		raw := env.String("eln_experiment_id")
		if raw == "" {
			return fmt.Errorf("no experiment ID given and no eln_experiment_id in cascade")
		}
		expID, err = parseELNExperimentID(raw)
		if err != nil {
			return err
		}
	}

	elnClient, err := openELN()
	if err != nil {
		return err
	}
	ironClient, err := iron.New()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Optional: iron_tree index for sha256-based file relocation.
	// Used only when iron stat on the parsed original path fails —
	// legacy uploads may reference a path where the object no
	// longer lives (moved after upload).
	var tree *treeIndex
	if treePath != "" {
		fmt.Fprintf(stdout, "loading tree index from %s…\n", treePath)
		tree, err = loadTreeIndex(treePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "  indexed %d checksummed object(s), %d basename(s)\n",
			len(tree.bySha), len(tree.byBasename))
	}

	// elnmap: mus's local experiment → (data_project, experiment_name)
	// cache. Filled by `mus eln link` / `mus eln tag`. Used as the
	// authoritative fallback when the COMMENT HTML omits these
	// (legacy format) and the iRODS AVU doesn't carry them either.
	mapStore, err := elnmap.Open("")
	if err != nil {
		fmt.Fprintf(stderr, "  ⚠ elnmap open failed: %v — fallback disabled\n", err)
	}

	// 1. Fetch sections + pick out mus: uploaded COMMENTs.
	sections, err := elnClient.ListExperimentSections(expID, false)
	if err != nil {
		return err
	}
	// Every COMMENT section is a candidate. The parser
	// (format-detecting: modern OR legacy) decides whether it's
	// actually a retrofit target. Section headers are unreliable —
	// legacy uploads use arbitrary human labels like "pooya
	// scripts", not the mus: uploaded prefix.
	var targets []eln.ExperimentSection
	for _, s := range sections {
		if s.SectionType == "COMMENT" {
			targets = append(targets, s)
		}
	}
	fmt.Fprintf(stdout, "experiment: %d — scanning %d section(s), %d candidate COMMENT(s)\n",
		expID, len(sections), len(targets))
	if len(targets) == 0 {
		fmt.Fprintln(stdout, "nothing to retrofit — no COMMENT sections on this experiment.")
		return nil
	}

	// 2. Parse each COMMENT into rows. Preserve the source
	// section's header so the retrofit-created SAMPLESOUT can
	// carry the SAME name — the depositor recognises "mus:
	// uploaded 1 file to iRODS" from their own history; "mus
	// retrofit: 1 object" wouldn't mean anything to them.
	var plan []retrofitAction
	var commentsToArchive []eln.ExperimentSection
	for _, s := range targets {
		body, err := elnClient.GetSectionContent(s.ExpJournalID)
		if err != nil {
			fmt.Fprintf(stderr, "  ⚠ could not fetch content for section %d: %v — skipping\n",
				s.ExpJournalID, err)
			continue
		}
		rows := parseUploadComment(s.ExpJournalID, body)
		if len(rows) == 0 {
			fmt.Fprintf(stderr, "  ⚠ section %d %q parsed to zero rows — skipping (unknown format?)\n",
				s.ExpJournalID, s.SectionHeader)
			continue
		}
		plan = append(plan, rows...)
		commentsToArchive = append(commentsToArchive, s)
	}
	if len(plan) == 0 {
		fmt.Fprintln(stdout, "nothing to retrofit — every candidate COMMENT was unparseable.")
		return nil
	}

	// 3. Live-verify each row against iron stat (with tree fallback
	// for relocated legacy files).
	fmt.Fprintf(stdout, "verifying %d iRODS object(s) via iron stat…\n", len(plan))
	for i := range plan {
		liveVerify(ctx, ironClient, tree, &plan[i])
	}

	// 4. Fill missing data_project / experiment_name from fallback
	// sources before computing canonical paths. Order per row:
	//   1. HTML value (set by parseXxxUploadComment)
	//   2. iRODS AVU (mus_data_project OR mgs.project.experiment_name etc.)
	//   3. elnmap.Lookup on the target experiment ID
	// Legacy comments carry neither in HTML — elnmap is the primary
	// source there.
	elnFallback := resolveELNFallback(mapStore, expID)
	for i := range plan {
		a := &plan[i]
		if a.IronStatErr != nil {
			continue
		}
		if a.DataProject == "" {
			a.DataProject = firstAVU(a.AVU, "mus_data_project")
		}
		if a.DataProject == "" {
			a.DataProject = elnFallback.dataProject
		}
	}

	// 5a. Apply the --data-project override, if any. Safety guard:
	// if mus already resolved a non-empty data_project from any
	// source (HTML / AVU / elnmap) AND it disagrees with the CLI
	// override, retrofit halts loudly. --force-data-project
	// bypasses. This exists so a fat-fingered `--data-project`
	// argument doesn't accidentally rewrite a well-established
	// experiment's canonical layout.
	if strings.TrimSpace(dataProjectOverride) != "" {
		// Collect every distinct existing value across the plan
		// + elnFallback. Empty-string entries are ignored.
		existing := map[string]bool{}
		for i := range plan {
			a := &plan[i]
			if a.IronStatErr != nil {
				continue
			}
			if v := strings.TrimSpace(a.DataProject); v != "" && v != dataProjectOverride {
				existing[v] = true
			}
		}
		if v := strings.TrimSpace(elnFallback.dataProject); v != "" && v != dataProjectOverride {
			existing[v] = true
		}
		if len(existing) > 0 && !forceDataProject {
			// List conflicting values so the depositor can
			// investigate before retrying.
			ks := make([]string, 0, len(existing))
			for k := range existing {
				ks = append(ks, k)
			}
			sortStrings(ks)
			return fmt.Errorf(
				"data_project safety halt: --data-project=%q disagrees with existing value(s) mus resolved from HTML / iRODS AVU / elnmap: %s\n"+
					"  If the existing value is right, drop --data-project (or set it to the same value).\n"+
					"  If your value is right and the existing one is wrong, re-run with --force-data-project.",
				dataProjectOverride, strings.Join(ks, ", "))
		}
		// Safe to apply: no conflict, or --force in play.
		for i := range plan {
			plan[i].DataProject = dataProjectOverride
		}
		elnFallback.dataProject = dataProjectOverride
		// Persist so subsequent runs pick it up (mirrors the
		// prompt path below). Non-fatal on failure.
		if mapStore != nil {
			longID := fmt.Sprintf("1%015d", expID)
			if err := mapStore.Remember(longID, dataProjectOverride, elnFallback.experimentName); err != nil {
				fmt.Fprintf(stderr, "  ⚠ save data_project mapping: %v (retrofit continues)\n", err)
			}
		}
		msg := fmt.Sprintf("using --data-project %q (from command line)", dataProjectOverride)
		if len(existing) > 0 {
			msg += " — --force-data-project override active; existing value(s) were: " +
				strings.Join(sortedKeys(existing), ", ")
		}
		fmt.Fprintln(stdout, msg)
	}

	// 5b. If any active row is still missing data_project after the
	// HTML+AVU+elnmap fallbacks, halt and prompt the depositor once.
	// data_project is the most common gap on legacy uploads (older
	// tools didn't stamp it) — asking is cheaper than skipping every
	// row that hits this.
	needDataProject := false
	for i := range plan {
		a := &plan[i]
		if a.IronStatErr != nil {
			continue
		}
		if a.DataProject == "" {
			needDataProject = true
			break
		}
	}
	if needDataProject {
		expInfoName := elnFallback.experimentName
		if expInfoName == "" {
			// Best-effort: fetch experiment metadata for a
			// friendlier prompt. Failure is non-fatal — we
			// just get a less-contextual prompt.
			if info, ierr := elnClient.ExpInfo(expID); ierr == nil {
				expInfoName = info.ExperimentName
			}
		}
		val, perr := promptRetrofitDataProject(cmd.InOrStdin(), stdout, expID, expInfoName)
		if perr != nil {
			return perr
		}
		// Fill every empty row and persist for next time.
		for i := range plan {
			if plan[i].DataProject == "" {
				plan[i].DataProject = val
			}
		}
		elnFallback.dataProject = val
		if mapStore != nil {
			// Store under the long-form key (`1` + zero-padded
			// short ID) to match what `mus eln link` writes.
			longID := fmt.Sprintf("1%015d", expID)
			if err := mapStore.Remember(longID, val, elnFallback.experimentName); err != nil {
				fmt.Fprintf(stderr, "  ⚠ save data_project mapping: %v (retrofit continues)\n", err)
			} else {
				fmt.Fprintf(stdout, "  ✓ saved eln→data_project mapping for next time\n")
			}
		}
	}

	// 5. Compute canonical paths + needs_move flags.
	for i := range plan {
		a := &plan[i]
		if a.IronStatErr != nil {
			continue
		}
		expName := firstAVU(a.AVU, "mus_eln_experiment_name")
		if expName == "" {
			expName = firstAVU(a.AVU, "mgs.project.experiment_name")
		}
		if expName == "" {
			expName = elnFallback.experimentName
		}
		canon, cerr := canonicalPathFor(env, a.DataProject, expName, a.Basename)
		if cerr != nil {
			a.SkipReason = "canonical-path: " + cerr.Error()
			a.Skipped = true
			continue
		}
		a.CanonicalPath = canon
		a.NeedsMove = a.IRODSPath != canon
	}

	// 5. Legacy-sweep context. Resolved once; passed to both the
	// plan printer (dry-run preview) and applyRetrofit.
	sweep := legacySweepContext{
		enabled:      sweepLegacy,
		sourcePath:   strings.TrimRight(legacySource, "/"),
		expID:        expID,
	}
	if sweepLegacy && sweep.sourcePath == "" {
		// Predict from study_name + experiment_name.
		var studyName string
		if info, ierr := elnClient.ExpInfo(expID); ierr == nil {
			studyName = info.StudyName
			if sweep.expName == "" {
				sweep.expName = info.ExperimentName
			}
		}
		if sweep.expName == "" {
			sweep.expName = elnFallback.experimentName
		}
		sweep.sourcePath = predictLegacySourcePath(env, studyName, sweep.expName)
		sweep.studyName = studyName
		if sweep.sourcePath == "" {
			return fmt.Errorf(
				"--sweep-legacy needs study_name + experiment_name to predict the source collection " +
					"(both were empty); pass --legacy-source PATH to specify the iRODS collection directly")
		}
	}
	// Build the "already handled" basename set once so both the
	// dry-run preview and the apply-time sweep subtract from it
	// consistently.
	sweep.handled = handledBasenames(plan, sweep.sourcePath)

	// 6. Print the plan.
	printRetrofitPlan(ctx, stdout, ironClient, expID, elnFallback,
		plan, commentsToArchive, apply, archiveComments, moveDanglers, sweep)

	if !apply {
		return nil
	}

	// 7. Execute.
	return applyRetrofit(cmd.Context(), elnClient, ironClient, env, expID, plan, commentsToArchive,
		archiveComments, moveDanglers, elnFallback, sweep, stdout, stderr)
}

// --- plan printer -----------------------------------------------------------

// printRetrofitPlan renders the dry-run / apply-preview report.
// Structured as three sections + a summary verdict:
//
//   ▶ retrofit plan      per-file, grouped by source COMMENT (mirrors the
//                        SAMPLESOUT sections mus will create)
//   ▶ side effects       one-liner list of what changes on ELN
//   ▶ danglers           live-checked when possible — actual leftover files,
//                        classified as (would reclaim) vs (would leave behind)
//   summary              single ✓ line if everything's tidy, ⚠ line otherwise
func printRetrofitPlan(ctx context.Context, w io.Writer, ironClient *iron.Client,
	expID int64, elnFB elnFallback,
	plan []retrofitAction, comments []eln.ExperimentSection,
	apply, archiveComments, moveDanglers bool, sweep legacySweepContext) {

	// Tally basic counters.
	nStatFail, nSkipped, nMove := 0, 0, 0
	for _, a := range plan {
		switch {
		case a.IronStatErr != nil:
			nStatFail++
		case a.Skipped:
			nSkipped++
		default:
			if a.NeedsMove {
				nMove++
			}
		}
	}

	// Header — experiment + resolved data_project.
	dp := elnFB.dataProject
	expName := elnFB.experimentName
	fmt.Fprintln(w)
	if dp != "" && expName != "" {
		fmt.Fprintf(w, "experiment %d · %s · %q\n", expID, dp, expName)
	} else if dp != "" {
		fmt.Fprintf(w, "experiment %d · %s\n", expID, dp)
	} else {
		fmt.Fprintf(w, "experiment %d\n", expID)
	}
	mode := "dry-run — no writes"
	if apply {
		mode = "--apply — will execute"
	}
	fmt.Fprintf(w, "mode: %s\n", mode)

	// --- ▶ retrofit plan --------------------------------------------
	fmt.Fprintln(w)
	fmt.Fprintf(w, "▶ retrofit plan  (%d file(s) across %d source comment(s))\n",
		len(plan), len(comments))
	// Index plan rows by SourceCommentJID for grouped rendering in
	// the same order comments were fetched.
	rowsByJID := map[int64][]*retrofitAction{}
	for i := range plan {
		a := &plan[i]
		rowsByJID[a.SourceCommentJID] = append(rowsByJID[a.SourceCommentJID], a)
	}
	for _, c := range comments {
		rows := rowsByJID[c.ExpJournalID]
		fmt.Fprintf(w, "\n  [%s]\n", c.SectionHeader)
		if len(rows) == 0 {
			fmt.Fprintln(w, "     (no rows parsed from this comment)")
			continue
		}
		for _, a := range rows {
			printPlanRow(w, a)
		}
	}

	// --- ▶ side effects ---------------------------------------------
	fmt.Fprintln(w)
	fmt.Fprintln(w, "▶ side effects")
	nSample := len(plan) - nStatFail - nSkipped
	fmt.Fprintf(w, "  · %d new SAMPLESOUT section(s) — one per source comment, same header\n", len(comments))
	fmt.Fprintf(w, "  · %d new Sample record(s) — meta autofill (path / sha256 / upload_* / mango_purl / …)\n", nSample)
	if nMove > 0 {
		fmt.Fprintf(w, "  · %d iron mv(s) — namespace rename, no bytes move, PURLs preserved\n", nMove)
	} else {
		fmt.Fprintln(w, "  · 0 iron mv(s) — every file is already at the canonical location")
	}
	if archiveComments {
		fmt.Fprintf(w, "  · %d source COMMENT(s) archived + annotated (soft-delete, restorable)\n", len(comments))
	} else {
		fmt.Fprintln(w, "  · source COMMENT(s) LEFT IN PLACE (--archive-comments=false)")
	}

	// --- ▶ danglers -------------------------------------------------
	// Live iron ls per source-parent — cheap (basename-indexed).
	// Only meaningful when at least one move is planned (a canonical
	// location = a source parent).
	sources := map[string]bool{}
	for _, a := range plan {
		if a.IronStatErr != nil || a.Skipped || !a.NeedsMove {
			continue
		}
		if sp := parentCollection(a.IRODSPath); sp != "" {
			sources[sp] = true
		}
	}
	movedNames := map[string]map[string]bool{} // parent → basename set
	for _, a := range plan {
		if a.IronStatErr != nil || a.Skipped || !a.NeedsMove {
			continue
		}
		p := parentCollection(a.IRODSPath)
		if p == "" {
			continue
		}
		if movedNames[p] == nil {
			movedNames[p] = map[string]bool{}
		}
		movedNames[p][a.Basename] = true
	}

	danglerAnyLeftBehind := false
	danglerAnyReclaimed := false
	if len(sources) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "▶ danglers  (files in source collection(s) not referenced by any comment)")
		keys := make([]string, 0, len(sources))
		for k := range sources {
			keys = append(keys, k)
		}
		sortStrings(keys)
		expIDStr := strconv.FormatInt(expID, 10)
		longIDStr := fmt.Sprintf("1%015d", expID)
		for _, parent := range keys {
			fmt.Fprintf(w, "\n  %s\n", parent)
			files, subcolls, err := ironClient.CollectionContents(ctx, parent)
			if err != nil {
				fmt.Fprintf(w, "    ⚠ could not list: %v\n", err)
				danglerAnyLeftBehind = true
				continue
			}
			// Filter out the basenames we're MOVING out of this
			// parent — those aren't leftovers.
			var leftFiles []string
			for _, f := range files {
				if movedNames[parent][f] {
					continue
				}
				leftFiles = append(leftFiles, f)
			}
			if len(leftFiles) == 0 && len(subcolls) == 0 {
				fmt.Fprintln(w, "    ✓ no danglers — collection will be empty after retrofit")
				continue
			}
			// For each leftover file: iron stat + AVU check.
			// Cost = one stat per file — fine for small numbers.
			var reclaim []string
			var leave []danglerNote
			for _, f := range leftFiles {
				full := parent + "/" + f
				info, sErr := ironClient.Stat(ctx, full)
				if sErr != nil {
					leave = append(leave, danglerNote{
						basename: f, reason: fmt.Sprintf("stat failed: %v", sErr),
					})
					continue
				}
				m := info.MetadataMap()
				owned := false
				for _, key := range []string{"mus_eln_experiment_id", "mgs.project.experiment_id"} {
					for _, v := range m[key] {
						if v == expIDStr || v == longIDStr {
							owned = true
							break
						}
					}
					if owned {
						break
					}
				}
				if owned {
					reclaim = append(reclaim, f)
					continue
				}
				// Explain why NOT owned.
				reason := "no matching AVU"
				for _, key := range []string{"mus_eln_experiment_id", "mgs.project.experiment_id"} {
					if v := firstAVU(m, key); v != "" {
						reason = fmt.Sprintf("%s=%s (different experiment)", key, v)
						break
					}
				}
				leave = append(leave, danglerNote{basename: f, reason: reason})
			}
			// Render this parent's danglers.
			if len(reclaim) > 0 {
				if moveDanglers {
					fmt.Fprintf(w, "    ✓ %d file(s) WILL BE RECLAIMED (--move-danglers on, AVU matches):\n",
						len(reclaim))
					for _, name := range reclaim {
						fmt.Fprintf(w, "        %s\n", name)
					}
					danglerAnyReclaimed = true
				} else {
					fmt.Fprintf(w, "    ⚠ %d file(s) COULD BE RECLAIMED but --move-danglers is OFF:\n",
						len(reclaim))
					for _, name := range reclaim {
						fmt.Fprintf(w, "        %s\n", name)
					}
					fmt.Fprintln(w, "      → pass --move-danglers to auto-move them to canonical + create sample records")
					danglerAnyLeftBehind = true
				}
			}
			if len(leave) > 0 {
				fmt.Fprintf(w, "    ⚠ %d file(s) WILL BE LEFT BEHIND (no matching AVU — investigate):\n",
					len(leave))
				for _, d := range leave {
					fmt.Fprintf(w, "        %s  (%s)\n", d.basename, d.reason)
				}
				danglerAnyLeftBehind = true
			}
			if len(subcolls) > 0 {
				fmt.Fprintf(w, "    ⚠ %d sub-collection(s) LEFT BEHIND (retrofit never recurses):\n",
					len(subcolls))
				for _, c := range subcolls {
					fmt.Fprintf(w, "        %s\n", c)
				}
				danglerAnyLeftBehind = true
			}
		}
	}

	// --- ▶ legacy sweep --------------------------------------------
	sweepAnyStragglers := false
	if sweep.enabled {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "▶ legacy sweep")
		fmt.Fprintf(w, "  predicted source: %s\n", sweep.sourcePath)
		if sweep.studyName != "" {
			fmt.Fprintf(w, "     (derived from study=%q + experiment=%q — override with --legacy-source PATH)\n",
				sweep.studyName, sweep.expName)
		}
		hits, err := findStragglers(ctx, ironClient, sweep.sourcePath, sweep.handled)
		if err != nil {
			fmt.Fprintf(w, "  ⚠ could not enumerate: %v\n", err)
			fmt.Fprintln(w, "     → double-check the path exists (or pass --legacy-source PATH)")
		} else if len(hits) == 0 {
			fmt.Fprintln(w, "  ✓ no stragglers — every child of this collection is already handled by the main retrofit")
		} else {
			sweepAnyStragglers = true
			nFiles, nFolders := 0, 0
			for _, h := range hits {
				if h.IsFolder {
					nFolders++
				} else {
					nFiles++
				}
			}
			fmt.Fprintf(w, "  found %d straggler(s): %d file(s), %d sub-collection(s)\n",
				len(hits), nFiles, nFolders)
			for _, h := range hits {
				tag := "file"
				if h.IsFolder {
					tag = "folder"
				}
				szStr := ""
				if !h.IsFolder && h.SizeBytes > 0 {
					szStr = "  " + retrofitHumanBytes(h.SizeBytes)
				}
				fmt.Fprintf(w, "    · %s  (%s)%s\n", h.Basename, tag, szStr)
			}
			// Where they'd go — pull the canonical parent from
			// any row in the plan; if the plan has none (i.e.
			// zero moves), derive from elnFB.
			target := ""
			for _, a := range plan {
				if a.CanonicalPath != "" {
					target = parentCollection(a.CanonicalPath)
					break
				}
			}
			if target == "" {
				if canon, cerr := canonicalPathFor(nil, elnFB.dataProject, elnFB.experimentName, "x"); cerr == nil {
					target = parentCollection(canon)
				}
			}
			if apply {
				fmt.Fprintf(w, "  → will move to %s/ and create sample records (interactive confirmation)\n",
					target)
			} else {
				fmt.Fprintf(w, "  → would move to %s/ and create sample records — retrofit will prompt for confirmation on --apply\n",
					target)
			}
		}
	}

	// --- summary verdict --------------------------------------------
	fmt.Fprintln(w)
	if nStatFail > 0 || nSkipped > 0 {
		fmt.Fprintf(w, "⚠ %d stat-fail + %d skip — see above; those rows will NOT be retrofitted\n",
			nStatFail, nSkipped)
	}
	if danglerAnyLeftBehind {
		fmt.Fprintln(w, "⚠ some files will be left in the source collection(s) — see \"danglers\" above")
	}
	if sweepAnyStragglers {
		fmt.Fprintln(w, "⚠ legacy sweep found stragglers — see \"legacy sweep\" above")
	}
	allClean := nStatFail == 0 && nSkipped == 0 && !danglerAnyLeftBehind && !sweepAnyStragglers
	if allClean {
		if danglerAnyReclaimed {
			fmt.Fprintln(w, "✓ looks good — every file accounted for, danglers will be reclaimed")
		} else {
			fmt.Fprintln(w, "✓ looks good — every file accounted for, source collections will be empty")
		}
	}
	if !apply {
		fmt.Fprintln(w, "\nrun with --apply to execute.")
	}
}

// printPlanRow renders one file's plan entry inside the grouped
// per-comment block. Kept compact: two lines per row max (three if
// there's a move to display).
func printPlanRow(w io.Writer, a *retrofitAction) {
	// Header line: `     ADneurons_patchSeq.tar.gz  25.5 GiB  (file)`
	size := a.SizeHuman
	if size == "" && a.SizeBytes > 0 {
		size = "≈" + strconv.FormatInt(a.SizeBytes>>20, 10) + " MiB"
	}
	head := fmt.Sprintf("     %s", nz(a.Basename))
	if size != "" {
		head += "  " + size
	}
	if a.Kind != "" {
		head += "  (" + a.Kind + ")"
	}
	fmt.Fprintln(w, head)

	if a.IronStatErr != nil {
		fmt.Fprintf(w, "       ✘ iron stat failed — %v\n", a.IronStatErr)
		return
	}
	if a.Skipped {
		fmt.Fprintf(w, "       ↷ skipped: %s\n", a.SkipReason)
		return
	}
	if a.NeedsMove {
		fmt.Fprintf(w, "       move  %s\n", a.IRODSPath)
		fmt.Fprintf(w, "         →   %s\n", a.CanonicalPath)
	} else {
		fmt.Fprintln(w, "       already at canonical location")
	}
}

// danglerNote is one leftover file with why-not-reclaimed context.
type danglerNote struct {
	basename string
	reason   string
}

// --- executor ---------------------------------------------------------------

func applyRetrofit(ctx context.Context, elnClient *eln.Client, ironClient *iron.Client, env *config.Env,
	expID int64, plan []retrofitAction, commentsToArchive []eln.ExperimentSection,
	archiveComments, moveDanglers bool, elnFB elnFallback,
	sweep legacySweepContext, stdout, stderr io.Writer) error {

	// Locate sample type + meta defs + storage layer once for the batch.
	// Sample type name: fall back to "Data" (matches the auto-register flow).
	sampleTypeName := env.String("eln_sample_type")
	if strings.TrimSpace(sampleTypeName) == "" {
		sampleTypeName = "Data"
	}
	st, err := elnClient.FindSampleTypeByName(sampleTypeName)
	if err != nil {
		return err
	}
	typeMetas, err := elnClient.ListSampleTypeMetas(st.ID)
	if err != nil {
		fmt.Fprintf(stderr, "  ⚠ list sample-type metas failed: %v — auto-fill disabled\n", err)
		typeMetas = nil
	}
	store, _ := secret.Open()
	storageLayerID := resolveSampleStorageLayerID(elnClient, env, store, ELNIRODSSampleLocationKey, stderr)

	// Step 1: moves. Non-fatal per row — a move failure aborts THAT
	// row but the rest proceed (sample creation for a failed-move
	// row is not attempted; the file is still where it was).
	// mkdir the destination's parent collection BEFORE mv so we don't
	// hit CAT_UNKNOWN_COLLECTION on first-ever writes to a fresh
	// /projects/<year>/<dp>/<exp>/ subtree.
	//
	// sourceParents tracks the iRODS collection each moved-away file
	// came from — checked at the end of the run for orphan
	// leftovers. That folder was semantically the "experiment
	// bucket" for the depositor; if there are files STILL sitting in
	// it after we retrofit everything mus knew about, they might be
	// unaccounted-for data.
	mkdirDone := map[string]bool{}
	failedMoves := map[string]bool{}
	sourceParents := map[string]bool{}
	for i := range plan {
		a := &plan[i]
		if a.IronStatErr != nil || a.Skipped {
			continue
		}
		if !a.NeedsMove {
			continue
		}
		if sp := parentCollection(a.IRODSPath); sp != "" {
			sourceParents[sp] = true
		}
		parent := parentCollection(a.CanonicalPath)
		if parent != "" && !mkdirDone[parent] {
			if err := ironClient.Mkdir(ctx, parent); err != nil {
				fmt.Fprintf(stderr,
					"  ✘ mkdir %s failed: %v — skipping this row\n", parent, err)
				failedMoves[a.IRODSPath] = true
				continue
			}
			mkdirDone[parent] = true
		}
		fmt.Fprintf(stdout, "→ iron mv %s %s\n", a.IRODSPath, a.CanonicalPath)
		if err := ironClient.Mv(ctx, a.IRODSPath, a.CanonicalPath, true); err != nil {
			fmt.Fprintf(stderr, "  ✘ mv failed: %v — skipping sample creation for this row\n", err)
			failedMoves[a.IRODSPath] = true
			continue
		}
		// Update the row's path so downstream steps use the new location.
		a.IRODSPath = a.CanonicalPath
	}

	// Step 2: bail if nothing to do (all failed / skipped).
	activeRows := countActive(plan, failedMoves)
	if activeRows == 0 {
		return fmt.Errorf(
			"no rows to link (every row hit a stat / mkdir / mv failure) — no section created")
	}

	// Group active rows by their source COMMENT jid so each new
	// SAMPLESOUT section carries the SAME header as the source
	// COMMENT it retrofits. Preserves the semantic grouping the
	// depositor already knew.
	rowsByComment := map[int64][]*retrofitAction{}
	for i := range plan {
		a := &plan[i]
		if a.IronStatErr != nil || a.Skipped || failedMoves[a.IRODSPath] {
			continue
		}
		rowsByComment[a.SourceCommentJID] = append(rowsByComment[a.SourceCommentJID], a)
	}

	// commentToNewSection maps a source comment's jid to the new
	// SAMPLESOUT section jid mus creates for it. Used when
	// annotating the archived COMMENT with a pointer to the fresh
	// section.
	commentToNewSection := map[int64]int64{}

	// Iterate in a stable order (the order the source COMMENTs
	// were discovered) so both the network calls and the printed
	// summary are deterministic.
	for _, srcComment := range commentsToArchive {
		rows, ok := rowsByComment[srcComment.ExpJournalID]
		if !ok || len(rows) == 0 {
			continue
		}
		newJID, err := elnClient.CreateExperimentSection(expID, "SAMPLESOUT", srcComment.SectionHeader)
		if err != nil {
			fmt.Fprintf(stderr,
				"  ✘ create SAMPLESOUT for source %d (%q): %v — %d row(s) will not be linked\n",
				srcComment.ExpJournalID, srcComment.SectionHeader, err, len(rows))
			continue
		}
		fmt.Fprintf(stdout, "✓ created SAMPLESOUT (expJournalID=%d, header=%q) on experiment %d\n",
			newJID, srcComment.SectionHeader, expID)
		commentToNewSection[srcComment.ExpJournalID] = newJID

		// Create samples for this group, then batch-link.
		// Legacy comments have no `-m` message; the source
		// section's header is the depositor's own description,
		// so we use it as the sample description in that case.
		descFallback := srcComment.SectionHeader
		var sampleIDs []int64
		for _, a := range rows {
			desc := a.Message
			if strings.TrimSpace(desc) == "" {
				desc = descFallback
			}
			provDict := buildProvenanceDict(sampleProvenance{
				path:           a.IRODSPath,
				uploadFromPath: a.UploadFromPath,
				sha256:         a.Sha256,
				sizeBytes:      a.SizeBytes,
				uploadTime:     nzTime(a.UploadTime, time.Now().UTC()),
				purl:           a.CommentPURL,
				kind:           a.Kind,
			})
			if a.UploadUser != "" {
				provDict["upload_user"] = a.UploadUser
			}
			if a.UploadHost != "" {
				provDict["upload_from_server"] = a.UploadHost
			}
			metas := metasForType(provDict, typeMetas)

			s, err := elnClient.CreateSample(eln.CreateSampleReq{
				SampleTypeID:   st.ID,
				Name:           a.Basename,
				Description:    desc,
				SampleMetas:    metas,
				StorageLayerID: storageLayerID,
			})
			if err != nil {
				fmt.Fprintf(stderr, "  ✘ create sample for %s: %v — skipping\n", a.Basename, err)
				continue
			}
			fmt.Fprintf(stdout, "  ✓ %s → sample %d\n", a.Basename, s.ID)
			sampleIDs = append(sampleIDs, s.ID)
		}
		if len(sampleIDs) == 0 {
			fmt.Fprintf(stderr,
				"  ⚠ no samples created for source %d — new SAMPLESOUT %d is empty\n",
				srcComment.ExpJournalID, newJID)
			continue
		}
		if err := elnClient.LinkSamplesToSection(newJID, sampleIDs); err != nil {
			fmt.Fprintf(stderr,
				"  ✘ link %d sample(s) to section %d: %v\n",
				len(sampleIDs), newJID, err)
			continue
		}
		fmt.Fprintf(stdout, "  ✓ linked %d sample(s) to section %d\n", len(sampleIDs), newJID)
	}

	// Step 5: annotate + archive old COMMENTs. Each note carries
	// the ID of the specific new SAMPLESOUT that replaced its
	// source. A COMMENT is only archived if EVERY row it referenced
	// was successfully retrofitted — otherwise we'd lose the
	// pointer to unresolved rows (stat-fail, mkdir-fail, mv-fail,
	// create-sample-fail).
	incompleteComments := map[int64]bool{}
	for i := range plan {
		a := &plan[i]
		if a.IronStatErr != nil || a.Skipped || failedMoves[a.IRODSPath] {
			incompleteComments[a.SourceCommentJID] = true
		}
	}
	if archiveComments {
		for _, srcComment := range commentsToArchive {
			jid := srcComment.ExpJournalID
			newJID, ok := commentToNewSection[jid]
			if !ok {
				fmt.Fprintf(stderr,
					"  ⚠ source COMMENT %d has no matching retrofit section (all rows failed?) — not archiving\n",
					jid)
				continue
			}
			if incompleteComments[jid] {
				fmt.Fprintf(stderr,
					"  ⚠ source COMMENT %d has unresolved rows — leaving unarchived so the reference isn't lost\n",
					jid)
				continue
			}
			body, gerr := elnClient.GetSectionContent(jid)
			if gerr != nil {
				fmt.Fprintf(stderr, "  ⚠ get section %d for annotation: %v — skipping annotation, still trying archive\n", jid, gerr)
			} else {
				annotated := retrofitNoteHTML(newJID) + body
				if uerr := elnClient.UpdateSectionContent(jid, annotated); uerr != nil {
					fmt.Fprintf(stderr, "  ⚠ annotate section %d: %v — still trying archive\n", jid, uerr)
				}
			}
			if aerr := elnClient.ArchiveSection(jid); aerr != nil {
				fmt.Fprintf(stderr, "  ✘ archive section %d: %v\n", jid, aerr)
				continue
			}
			fmt.Fprintf(stdout, "  ✓ archived old COMMENT %d\n", jid)
		}
	}

	fmt.Fprintf(stdout, "\ndone. %d SAMPLESOUT section(s) created, %d source COMMENT(s) processed\n",
		len(commentToNewSection), len(commentsToArchive))

	// Post-move leftovers check. Each source collection we moved
	// files OUT OF was the experiment's semantic bucket in the old
	// layout — anything still sitting there is either (a) an ELN
	// row we couldn't handle, (b) truly-orphan data mus doesn't know
	// about, or (c) a subcollection we don't want to lose track of.
	// Warn loudly, list first few names for triage.
	if moveDanglers {
		// Reclaim before the empty-collection message fires: any
		// mv'd danglers will empty the source collection further.
		if err := reclaimDanglers(ctx, elnClient, ironClient, env, expID, elnFB,
			sourceParents, st, typeMetas, storageLayerID, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "  ⚠ reclaim danglers: %v — continuing to leftovers report\n", err)
		}
	}
	if sweep.enabled {
		if err := sweepLegacyStragglers(ctx, elnClient, ironClient, env, expID, elnFB, sweep,
			st, typeMetas, storageLayerID, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "  ⚠ legacy sweep: %v — continuing\n", err)
		}
	}
	warnLeftovers(ctx, ironClient, sourceParents, stdout, stderr)
	return nil
}

// sweepLegacyStragglers implements --sweep-legacy: enumerates children
// of the predicted legacy source collection, prompts for
// confirmation, then iron-mv's each unhandled child to the canonical
// location and creates a Sample record. Sub-collections (folders)
// are moved whole; their sample records carry kind=folder and no
// sha256 (would require an expensive recursive walk).
func sweepLegacyStragglers(ctx context.Context, elnClient *eln.Client, ironClient *iron.Client,
	env *config.Env, expID int64, elnFB elnFallback, sweep legacySweepContext,
	st *eln.SampleType, typeMetas []eln.SampleTypeMeta, storageLayerID int64,
	stdout, stderr io.Writer) error {

	hits, err := findStragglers(ctx, ironClient, sweep.sourcePath, sweep.handled)
	if err != nil {
		return fmt.Errorf("list %s: %w", sweep.sourcePath, err)
	}
	if len(hits) == 0 {
		fmt.Fprintln(stdout, "legacy sweep: no stragglers under", sweep.sourcePath)
		return nil
	}
	// Canonical parent — where stragglers will land.
	canon, cerr := canonicalPathFor(env, elnFB.dataProject, elnFB.experimentName, "x")
	if cerr != nil {
		return fmt.Errorf("compute canonical parent: %w", cerr)
	}
	canonParent := parentCollection(canon)
	if canonParent == "" {
		return fmt.Errorf("canonical parent resolved to empty string")
	}

	// Show + prompt.
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "legacy sweep: %d straggler(s) under %s\n", len(hits), sweep.sourcePath)
	for _, h := range hits {
		tag := "file"
		sz := ""
		if h.IsFolder {
			tag = "folder"
		} else if h.SizeBytes > 0 {
			sz = "  " + retrofitHumanBytes(h.SizeBytes)
		}
		fmt.Fprintf(stdout, "  · %s  (%s)%s\n", h.Basename, tag, sz)
	}
	fmt.Fprintf(stdout, "these will be moved to %s/ and each will get its own Sample record.\n", canonParent)
	if !confirmYesNo(stdout,
		fmt.Sprintf("Proceed with %d straggler mv+sample? [y/N]: ", len(hits))) {
		fmt.Fprintln(stdout, "  legacy sweep: aborted by depositor")
		return nil
	}

	// Ensure the canonical parent exists.
	if err := ironClient.Mkdir(ctx, canonParent); err != nil {
		return fmt.Errorf("mkdir %s: %w", canonParent, err)
	}

	sectionHeader := "mus retrofit: legacy stragglers reclaimed from " + sweep.sourcePath
	newJID, err := elnClient.CreateExperimentSection(expID, "SAMPLESOUT", sectionHeader)
	if err != nil {
		return fmt.Errorf("create straggler section: %w", err)
	}
	fmt.Fprintf(stdout, "✓ created SAMPLESOUT (expJournalID=%d) for stragglers\n", newJID)

	var sampleIDs []int64
	for _, h := range hits {
		dst := canonParent + "/" + h.Basename
		fmt.Fprintf(stdout, "→ [straggler] iron mv %s %s\n", h.FullPath, dst)
		if err := ironClient.Mv(ctx, h.FullPath, dst, true); err != nil {
			fmt.Fprintf(stderr, "  ✘ mv %s: %v — skipping sample creation\n", h.FullPath, err)
			continue
		}
		kind := "file"
		if h.IsFolder {
			kind = "folder"
		}
		// Grab AVU + fresh size / checksum from the new location.
		var sizeBytes int64
		var sha string
		var uploadUser string
		var uploadedAt string
		if info, sErr := ironClient.Stat(ctx, dst); sErr == nil {
			sizeBytes = info.Size
			sha = info.Checksum
			uploadUser = info.Creator
			// AVU-derived preferences.
			m := info.MetadataMap()
			if sha == "" {
				for _, key := range []string{"mus_sha256", "mus_archive_sha256", "mus_recursive_sha256", "mgs.project.sha256"} {
					if v := firstAVU(m, key); v != "" {
						sha = v
						break
					}
				}
			}
			for _, key := range []string{"mus_uploaded_at", "mgs.project.upload_date"} {
				if v := firstAVU(m, key); v != "" {
					uploadedAt = v
					break
				}
			}
		}
		provDict := buildProvenanceDict(sampleProvenance{
			path:      dst,
			sha256:    sha,
			sizeBytes: sizeBytes,
			kind:      kind,
		})
		if uploadUser != "" {
			provDict["upload_user"] = uploadUser
		}
		if uploadedAt != "" {
			provDict["upload_data"] = uploadedAt
			provDict["upload_date"] = uploadedAt
		}
		metas := metasForType(provDict, typeMetas)

		s, err := elnClient.CreateSample(eln.CreateSampleReq{
			SampleTypeID:   st.ID,
			Name:           h.Basename,
			Description:    "reclaimed straggler from " + sweep.sourcePath,
			SampleMetas:    metas,
			StorageLayerID: storageLayerID,
		})
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ create sample for %s: %v\n", h.Basename, err)
			continue
		}
		fmt.Fprintf(stdout, "  ✓ [straggler] %s → sample %d\n", h.Basename, s.ID)
		sampleIDs = append(sampleIDs, s.ID)
	}
	if len(sampleIDs) == 0 {
		fmt.Fprintln(stderr, "  ⚠ no straggler samples created (all mvs / creates failed)")
		return nil
	}
	if err := elnClient.LinkSamplesToSection(newJID, sampleIDs); err != nil {
		return fmt.Errorf("link stragglers to section %d: %w", newJID, err)
	}
	fmt.Fprintf(stdout, "  ✓ linked %d straggler sample(s) to section %d\n",
		len(sampleIDs), newJID)
	return nil
}

// confirmYesNo prints prompt to stdout, reads one line from stdin,
// returns true only on 'y' / 'yes' (case-insensitive). Empty line
// defaults to false — deliberate: destructive operation, don't
// silently OK on a stray Enter.
func confirmYesNo(stdout io.Writer, prompt string) bool {
	fmt.Fprint(stdout, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// reclaimDanglers scans each source-parent collection for leftover
// data-objects, checks their AVU to confirm they belong to the
// target experiment, and iron-mv's the ones that do into the
// canonical layout. Each moved dangler gets a Sample record; all
// samples land in ONE new SAMPLESOUT section per invocation.
//
// Files without a matching AVU are left in place and reported by
// the subsequent warnLeftovers call. Sub-collections are never
// auto-moved — retrofit doesn't recurse into unknown territory.
func reclaimDanglers(ctx context.Context, elnClient *eln.Client, ironClient *iron.Client,
	env *config.Env, expID int64, elnFB elnFallback,
	sourceParents map[string]bool, st *eln.SampleType, typeMetas []eln.SampleTypeMeta,
	storageLayerID int64, stdout, stderr io.Writer) error {

	if len(sourceParents) == 0 {
		return nil
	}

	// Deterministic order.
	parents := make([]string, 0, len(sourceParents))
	for p := range sourceParents {
		parents = append(parents, p)
	}
	sortStrings(parents)

	expIDStr := strconv.FormatInt(expID, 10)
	longIDStr := fmt.Sprintf("1%015d", expID)

	type dangler struct {
		parent   string   // source collection this came from
		path     string   // full iRODS path of the leftover file
		basename string
		avu      map[string][]string
		msg      string // best-effort description
	}
	var toMove []dangler
	var skipped []dangler // non-owned or no-AVU

	for _, parent := range parents {
		files, _, err := ironClient.CollectionContents(ctx, parent)
		if err != nil {
			fmt.Fprintf(stderr, "  ⚠ list %s: %v — skipping this collection for dangler-reclaim\n", parent, err)
			continue
		}
		for _, name := range files {
			full := parent + "/" + name
			info, err := ironClient.Stat(ctx, full)
			if err != nil {
				fmt.Fprintf(stderr, "  ⚠ stat %s: %v — cannot check AVU, skipping\n", full, err)
				continue
			}
			m := info.MetadataMap()
			// Match either the modern mus_eln_experiment_id AVU
			// OR the legacy mgs.project.experiment_id AVU. Both
			// short-form (1955928) and long-form (10000000...)
			// values are accepted — mus's own upload stamps
			// short-form; older tools sometimes wrote long.
			ownedByThisExp := false
			for _, key := range []string{"mus_eln_experiment_id", "mgs.project.experiment_id"} {
				for _, v := range m[key] {
					if v == expIDStr || v == longIDStr {
						ownedByThisExp = true
						break
					}
				}
				if ownedByThisExp {
					break
				}
			}
			d := dangler{parent: parent, path: full, basename: name, avu: m}
			if !ownedByThisExp {
				skipped = append(skipped, d)
				continue
			}
			// Best-effort description: mus_upload_message first,
			// then mgs.project.description.
			if v := firstAVU(m, "mus_upload_message"); v != "" {
				d.msg = v
			} else if v := firstAVU(m, "mgs.project.description"); v != "" {
				d.msg = v
			}
			toMove = append(toMove, d)
		}
	}

	// Report skipped-with-different-AVU danglers so the depositor
	// sees them clearly (they'll also show up in warnLeftovers).
	for _, d := range skipped {
		reason := "no mus_eln_experiment_id / mgs.project.experiment_id in AVU"
		for _, key := range []string{"mus_eln_experiment_id", "mgs.project.experiment_id"} {
			if v := firstAVU(d.avu, key); v != "" {
				reason = fmt.Sprintf("AVU %s=%s (belongs to a different experiment)", key, v)
				break
			}
		}
		fmt.Fprintf(stderr, "  · leaving in place: %s — %s\n", d.path, reason)
	}

	if len(toMove) == 0 {
		if len(skipped) > 0 {
			fmt.Fprintln(stdout, "no danglers reclaimed (none belong to this experiment by AVU).")
		}
		return nil
	}

	// Compute canonical path + create the reclamation SAMPLESOUT.
	// Section header names the source collection for provenance; if
	// danglers came from several parents, the header lists the first
	// one + "and N more" — good enough context for the depositor.
	dataProj := elnFB.dataProject
	expName := elnFB.experimentName
	if dataProj == "" || expName == "" {
		// Try AVU on the first dangler as a further fallback.
		if len(toMove) > 0 {
			if dataProj == "" {
				dataProj = firstAVU(toMove[0].avu, "mus_data_project")
			}
			if expName == "" {
				expName = firstAVU(toMove[0].avu, "mus_eln_experiment_name")
				if expName == "" {
					expName = firstAVU(toMove[0].avu, "mgs.project.experiment_name")
				}
			}
		}
	}
	if dataProj == "" || expName == "" {
		return fmt.Errorf("cannot compute canonical path for reclaim (data_project=%q, exp_name=%q)", dataProj, expName)
	}

	// Iron mv each dangler + create sample. Mkdir the canonical
	// parent once (same as the main flow).
	canonicalParent := ""
	if canon, err := canonicalPathFor(env, dataProj, expName, "x"); err == nil {
		canonicalParent = parentCollection(canon)
	}
	if canonicalParent == "" {
		return fmt.Errorf("could not compute canonical parent")
	}
	if err := ironClient.Mkdir(ctx, canonicalParent); err != nil {
		return fmt.Errorf("mkdir %s: %w", canonicalParent, err)
	}

	// Section header — mention the primary source collection.
	headerParent := parents[0]
	sectionHeader := "mus retrofit: reclaimed danglers from " + headerParent
	if len(parents) > 1 {
		sectionHeader = fmt.Sprintf("mus retrofit: reclaimed danglers from %s (+%d other source(s))",
			headerParent, len(parents)-1)
	}
	newJID, err := elnClient.CreateExperimentSection(expID, "SAMPLESOUT", sectionHeader)
	if err != nil {
		return fmt.Errorf("create reclaim section: %w", err)
	}
	fmt.Fprintf(stdout, "✓ created reclamation SAMPLESOUT (expJournalID=%d, header=%q)\n",
		newJID, sectionHeader)

	var reclaimedIDs []int64
	for _, d := range toMove {
		canon := canonicalParent + "/" + d.basename
		fmt.Fprintf(stdout, "→ [dangler] iron mv %s %s\n", d.path, canon)
		if err := ironClient.Mv(ctx, d.path, canon, true); err != nil {
			fmt.Fprintf(stderr, "  ✘ mv %s: %v — skipping sample creation\n", d.path, err)
			continue
		}
		// Grab authoritative metadata post-move (checksum, size,
		// mtime) — same as main flow: iron stat on the new
		// location.
		info, sterr := ironClient.Stat(ctx, canon)
		var sizeBytes int64
		var sha string
		if sterr == nil {
			sizeBytes = info.Size
			sha = info.Checksum
		}
		// Prefer AVU sha256 when present.
		if sha == "" {
			for _, key := range []string{"mus_sha256", "mus_archive_sha256", "mus_recursive_sha256", "mgs.project.sha256"} {
				if v := firstAVU(d.avu, key); v != "" {
					sha = v
					break
				}
			}
		}
		provDict := buildProvenanceDict(sampleProvenance{
			path:      canon,
			sha256:    sha,
			sizeBytes: sizeBytes,
			kind:      "file",
		})
		if info != nil && info.Creator != "" {
			provDict["upload_user"] = info.Creator
		}
		if v := firstAVU(d.avu, "mus_uploaded_at"); v != "" {
			provDict["upload_data"] = v
			provDict["upload_date"] = v
		} else if v := firstAVU(d.avu, "mgs.project.upload_date"); v != "" {
			provDict["upload_data"] = v
			provDict["upload_date"] = v
		}
		metas := metasForType(provDict, typeMetas)

		desc := d.msg
		if strings.TrimSpace(desc) == "" {
			desc = "reclaimed dangling file from " + d.parent
		}

		s, err := elnClient.CreateSample(eln.CreateSampleReq{
			SampleTypeID:   st.ID,
			Name:           d.basename,
			Description:    desc,
			SampleMetas:    metas,
			StorageLayerID: storageLayerID,
		})
		if err != nil {
			fmt.Fprintf(stderr, "  ✘ create sample for reclaimed %s: %v\n", d.basename, err)
			continue
		}
		fmt.Fprintf(stdout, "  ✓ [dangler] %s → sample %d\n", d.basename, s.ID)
		reclaimedIDs = append(reclaimedIDs, s.ID)
	}
	if len(reclaimedIDs) == 0 {
		fmt.Fprintf(stderr, "  ⚠ reclamation section %d ended up empty (all sample creates failed)\n", newJID)
		return nil
	}
	if err := elnClient.LinkSamplesToSection(newJID, reclaimedIDs); err != nil {
		return fmt.Errorf("link %d reclaimed sample(s) to section %d: %w",
			len(reclaimedIDs), newJID, err)
	}
	fmt.Fprintf(stdout, "  ✓ linked %d reclaimed sample(s) to section %d\n",
		len(reclaimedIDs), newJID)
	return nil
}

// warnLeftovers checks each source-parent collection for remaining
// data-object children or sub-collections after all mvs completed.
// Prints a yellow-highlighted block to stderr per non-empty source
// so the depositor can eyeball orphaned data before archiving the
// legacy tree.
func warnLeftovers(ctx context.Context, ironClient *iron.Client, sourceParents map[string]bool, stdout, stderr io.Writer) {
	if len(sourceParents) == 0 {
		return
	}
	// Deterministic iteration order for output stability.
	keys := make([]string, 0, len(sourceParents))
	for k := range sourceParents {
		keys = append(keys, k)
	}
	// Simple sort (import "sort" already used elsewhere in this
	// pkg? — check; fallback to len-based sort otherwise).
	sortStrings(keys)

	nWarn := 0
	for _, parent := range keys {
		files, subcolls, err := ironClient.CollectionContents(ctx, parent)
		if err != nil {
			fmt.Fprintf(stderr,
				"  ⚠ could not check leftovers in %s: %v\n", parent, err)
			continue
		}
		if len(files) == 0 && len(subcolls) == 0 {
			// Empty — the depositor can `iron rmdir` this
			// safely if they want to tidy up. Not warning
			// noise-worthy.
			continue
		}
		nWarn++
		fmt.Fprintf(stderr, "\n  \033[33m⚠  LEFTOVER FILES in source collection %s\033[0m\n", parent)
		fmt.Fprintf(stderr, "     mus moved out the files it knew about, but this collection still holds:\n")
		if len(files) > 0 {
			fmt.Fprintf(stderr, "       - %d data object(s):\n", len(files))
			for i, f := range files {
				if i >= 10 {
					fmt.Fprintf(stderr, "         … and %d more\n", len(files)-i)
					break
				}
				fmt.Fprintf(stderr, "         %s/%s\n", parent, f)
			}
		}
		if len(subcolls) > 0 {
			fmt.Fprintf(stderr, "       - %d sub-collection(s):\n", len(subcolls))
			for i, c := range subcolls {
				if i >= 10 {
					fmt.Fprintf(stderr, "         … and %d more\n", len(subcolls)-i)
					break
				}
				fmt.Fprintf(stderr, "         %s\n", c)
			}
		}
		fmt.Fprintf(stderr, "     These are unaccounted-for from the ELN perspective — they may be:\n")
		fmt.Fprintf(stderr, "       - files uploaded outside mus (no ELN comment references them)\n")
		fmt.Fprintf(stderr, "       - ELN comments mus couldn't parse (unknown format)\n")
		fmt.Fprintf(stderr, "       - files from other experiments that share this parent collection\n")
		fmt.Fprintf(stderr, "     Investigate before removing the collection.\n")
	}
	if nWarn == 0 && len(sourceParents) > 0 {
		fmt.Fprintf(stdout, "\n✓ all source collections are now empty — safe to `iron rmdir` if desired:\n")
		for _, p := range keys {
			fmt.Fprintf(stdout, "    %s\n", p)
		}
	}
}

// sortedKeys returns the keys of a set (map[string]bool) as a sorted
// slice. Small helper — deterministic output ordering in messages.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// sortStrings is a tiny alphabetical sort — avoids importing "sort"
// just for one use site. In-place, stable, O(n²) but n is always
// small (< 10 source parents per retrofit).
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j-1] > ss[j]; j-- {
			ss[j-1], ss[j] = ss[j], ss[j-1]
		}
	}
}

// --- small helpers ----------------------------------------------------------

func nz(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// retrofitHumanBytes formats a byte count as a compact human-readable string.
// Very simple — MB / GB / TB with one decimal.
func retrofitHumanBytes(n int64) string {
	f := float64(n)
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TiB", f/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", f/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", f/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", f/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func retrofitShortSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}

func ellipsis(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func nzTime(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

// legacySweepContext holds everything the legacy-sweep needs so we
// compute it once (in runELNRetrofit) and pass it to both the plan
// printer and applyRetrofit. When enabled is false, the rest is
// ignored.
type legacySweepContext struct {
	enabled    bool
	sourcePath string // <irods_home>/lab_data_archive/<study>/<exp>
	studyName  string // populated when derived (empty when --legacy-source given)
	expName    string
	expID      int64
	// handled is the set of BASENAMES under sourcePath that mus's
	// main retrofit already touched — subtracted from iron ls to
	// leave only unaccounted-for children.
	handled map[string]bool
}

// handledBasenames returns the basenames of files+folders under a
// given parent that the main retrofit plan will touch (mv or leave
// canonical). Both directions matter: if a row's IRODSPath's parent
// is sourcePath, the row's basename is handled; if the row's
// CanonicalPath's parent is sourcePath, same.
func handledBasenames(plan []retrofitAction, sourcePath string) map[string]bool {
	out := map[string]bool{}
	if sourcePath == "" {
		return out
	}
	for _, a := range plan {
		if a.IronStatErr != nil || a.Skipped {
			continue
		}
		if parentCollection(a.IRODSPath) == sourcePath {
			out[a.Basename] = true
		}
		if a.CanonicalPath != "" && parentCollection(a.CanonicalPath) == sourcePath {
			out[a.Basename] = true
		}
	}
	return out
}

// sanitizeLegacyName converts an ELN study / experiment name to the
// path segment used by the pre-mus Python upload tool:
//   - lowercase
//   - runs of non-alphanumeric characters collapsed to a single '_'
//   - leading + trailing '_' trimmed
//
// Example: "Alethea - pooya microglia data integration" →
//   "alethea_pooya_microglia_data_integration"
func sanitizeLegacyName(s string) string {
	var b strings.Builder
	prevUnderscore := false
	trimmed := strings.TrimSpace(s)
	for _, r := range strings.ToLower(trimmed) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// predictLegacySourcePath builds the iRODS collection the pre-Go
// Python `mus` used for a given experiment: <irods_home>/lab_data_archive/
// <sanitize(study_name)>/<sanitize(experiment_name)>. Returns "" when
// either name is empty or sanitises to empty.
func predictLegacySourcePath(env *config.Env, studyName, expName string) string {
	studyS := sanitizeLegacyName(studyName)
	expS := sanitizeLegacyName(expName)
	if studyS == "" || expS == "" {
		return ""
	}
	home := env.String("irods_home")
	if home == "" {
		home = defaults.IRODSHome
	}
	if home == "" {
		return ""
	}
	return strings.TrimRight(home, "/") + "/lab_data_archive/" + studyS + "/" + expS
}

// stragglerHit is one leftover child (file OR sub-collection) mus
// found under the predicted legacy source collection but did NOT
// handle in the main retrofit.
type stragglerHit struct {
	FullPath  string // e.g. /gbiomed/.../alethea_scripts/orphan.dat
	Basename  string
	IsFolder  bool
	SizeBytes int64  // 0 for folders (would need iron tree to compute)
	Sha256    string // may be blank (empty for folders; AVU-derived for files)
}

// findStragglers enumerates the direct children (files +
// sub-collections) of `sourcePath` on iRODS and returns those NOT
// present in `handled` (basename set of things the main retrofit
// already touched). Non-fatal on errors — returns an empty slice +
// the error so the caller can decide to warn or halt.
func findStragglers(ctx context.Context, ironClient *iron.Client, sourcePath string, handled map[string]bool) ([]stragglerHit, error) {
	files, subcolls, err := ironClient.CollectionContents(ctx, sourcePath)
	if err != nil {
		return nil, err
	}
	var out []stragglerHit
	for _, name := range files {
		if handled[name] {
			continue
		}
		full := sourcePath + "/" + name
		hit := stragglerHit{FullPath: full, Basename: name}
		if info, sErr := ironClient.Stat(ctx, full); sErr == nil {
			hit.SizeBytes = info.Size
			hit.Sha256 = info.Checksum
		}
		out = append(out, hit)
	}
	for _, cp := range subcolls {
		base := lastPathSegment(cp)
		if handled[base] {
			continue
		}
		out = append(out, stragglerHit{
			FullPath: cp,
			Basename: base,
			IsFolder: true,
		})
	}
	return out, nil
}

// promptRetrofitDataProject halts the retrofit interactively and asks the
// depositor for a data_project value. Used when HTML parse + iRODS
// AVU + elnmap all failed to produce one — usually legacy uploads
// that predate the data_project convention.
//
// Validates via dataproject.ValidateName (must match NameYear
// format, e.g. Fiers2025). Loops up to 3 attempts on validation
// failure. Refuses on non-TTY stdin with an actionable error
// pointing the depositor at `mus eln link` or an explicit --
// data-project flag (future work).
func promptRetrofitDataProject(stdin io.Reader, stdout io.Writer, expID int64, expName string) (string, error) {
	if !stdinIsTTY(stdin) {
		return "", fmt.Errorf(
			"data_project unknown for experiment %d and stdin is not a TTY.\n"+
				"  Fix by one of:\n"+
				"    - `mus eln link %d`  (interactive; also fills eln_experiment_name and caches the mapping)\n"+
				"    - `mus config set data_project <NameYear>`  in the folder's mus.cfg\n"+
				"  Then re-run retrofit.",
			expID, expID)
	}
	r := bufio.NewReader(stdin)
	nameHint := ""
	if strings.TrimSpace(expName) != "" {
		nameHint = " (" + expName + ")"
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  ┌─────────────────────────────────────────────────────────────────────")
	fmt.Fprintf(stdout, "  │ data_project is UNKNOWN for experiment %d%s\n", expID, nameHint)
	fmt.Fprintln(stdout, "  │")
	fmt.Fprintln(stdout, "  │ The HTML comment, iRODS AVU, and mus's elnmap all lack it.")
	fmt.Fprintln(stdout, "  │ Retrofit needs it to build the canonical iRODS path")
	fmt.Fprintln(stdout, "  │ (/projects/<YEAR>/<data_project>/<exp_name>/…).")
	fmt.Fprintln(stdout, "  │")
	fmt.Fprintln(stdout, "  │ Format: NameYear (e.g. Fiers2025, Zakeri2020, Sun2026 — the year")
	fmt.Fprintln(stdout, "  │ suffix is required; mus derives the YEAR path segment from it).")
	fmt.Fprintln(stdout, "  │")
	fmt.Fprintln(stdout, "  │ Value will be cached in ~/.local/share/mus/eln_mappings.json so")
	fmt.Fprintln(stdout, "  │ subsequent runs pick it up automatically.")
	fmt.Fprintln(stdout, "  └─────────────────────────────────────────────────────────────────────")
	for attempts := 0; attempts < 3; attempts++ {
		fmt.Fprintf(stdout, "  data_project> ")
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		val := strings.TrimSpace(line)
		if val == "" {
			fmt.Fprintln(stdout, "  (empty input — retrofit needs a value, retrying)")
			continue
		}
		if verr := dataproject.ValidateName(val); verr != nil {
			fmt.Fprintf(stdout, "  ✘ invalid: %v\n", verr)
			continue
		}
		return val, nil
	}
	return "", fmt.Errorf("no valid data_project provided after 3 attempts")
}

// elnFallback holds the data_project + experiment_name that mus's
// local elnmap has stored for the target experiment. Filled once per
// retrofit run; empty when there's no mapping (retrofit still tries
// per-row AVU sources first).
type elnFallback struct {
	dataProject    string
	experimentName string
}

// resolveELNFallback queries elnmap for both the short-form and the
// long-form experiment ID and returns the first non-empty mapping.
// Long-form is what `mus eln link` writes today (18-digit ID); short
// form is what eLabNext's API takes. We check both to be robust to
// however the depositor originally called mus.
func resolveELNFallback(store *elnmap.Store, expID int64) elnFallback {
	if store == nil {
		return elnFallback{}
	}
	shortID := strconv.FormatInt(expID, 10)
	if m, _ := store.Lookup(shortID); m != nil && m.DataProject != "" {
		return elnFallback{dataProject: m.DataProject, experimentName: m.ExperimentName}
	}
	// Long form used by `mus eln link`: `1` + zero-padded short ID
	// (16 digits total). Reconstruct — see FixExperimentID.
	longID := fmt.Sprintf("1%015d", expID)
	if m, _ := store.Lookup(longID); m != nil && m.DataProject != "" {
		return elnFallback{dataProject: m.DataProject, experimentName: m.ExperimentName}
	}
	return elnFallback{}
}

// parentCollection returns the parent iRODS collection of a full
// object path — everything before the final "/". Empty string if the
// path has no "/" (defensive: shouldn't happen for a real iRODS
// object path but guards against a malformed CanonicalPath).
func parentCollection(objectPath string) string {
	i := strings.LastIndex(objectPath, "/")
	if i <= 0 {
		return ""
	}
	return objectPath[:i]
}

func countActive(plan []retrofitAction, failedMoves map[string]bool) int {
	n := 0
	for _, a := range plan {
		if a.IronStatErr != nil || a.Skipped {
			continue
		}
		if failedMoves[a.IRODSPath] {
			continue
		}
		n++
	}
	return n
}

func retrofitNoteHTML(newJID int64) string {
	return fmt.Sprintf(
		`<div style="border:1px solid #fa0; background:#fff8e0; padding:8px; margin-bottom:12px;">`+
			`<strong>⚠ Archived by <code>mus eln retrofit</code></strong> — `+
			`the structured sample records for these uploads now live in a `+
			`SAMPLESOUT section on this experiment (expJournalID=%d). `+
			`This comment is kept for historical reference.`+
			`</div>`+"\n", newJID)
}
