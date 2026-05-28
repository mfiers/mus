package cli

// Post-upload ELN comment hook.
//
// After a successful `mus irods upload`, if the folder is linked to an ELN
// experiment (`eln_experiment_id` set in the cascade) AND the user has stored
// an ELN token, append a COMMENT section to that experiment summarising the
// uploads (one comment per command invocation, not per file).
//
// Strictly best-effort: every failure path logs a one-line warning to stderr
// and continues. The iRODS upload already succeeded by the time we're called.

import (
	"fmt"
	"html"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/folder"
)

// uploadedItem describes one successful upload from a `mus irods upload`
// invocation. File arg → "file"; folder uploaded as collection → "folder";
// folder packed and uploaded as tar.gz → "archive".
//
// Sha256 is the verifiable hash:
//   - file:    sha256 of the bytes
//   - archive: sha256 of the .tar.gz archive on disk
//   - folder:  Merkle hash over the directory tree (mus's recursive_sha256)
type uploadedItem struct {
	Kind       string
	LocalPath  string
	RemotePath string
	URL        string
	PURL       string
	Sha256     string
	Size       int64
}

// postUploadCommentToELN posts a single COMMENT section summarising `items`
// to the experiment named by env["eln_experiment_id"]. Returns silently
// (no error) on every failure path — the caller's upload already succeeded.
//
// Skips silently when:
//   - enabled == false (user passed --no-eln-comment)
//   - dryRun (no real bytes moved)
//   - items is empty
//   - the cascade has no eln_experiment_id (this folder isn't ELN-linked)
//   - the user hasn't run `mus eln login` (no token in secret store)
func postUploadCommentToELN(env *config.Env, items []uploadedItem,
	dryRun, enabled bool, stderr io.Writer) {

	if !enabled || dryRun || len(items) == 0 {
		return
	}
	expIDStr := env.String("eln_experiment_id")
	if expIDStr == "" {
		return
	}
	expID, err := strconv.ParseInt(expIDStr, 10, 64)
	if err != nil {
		fmt.Fprintf(stderr, "  eln comment skipped: invalid eln_experiment_id %q\n", expIDStr)
		return
	}
	client, err := openELN()
	if err != nil {
		// Most common case: no token. Don't nag — folder may be ELN-linked
		// but this user isn't authenticated against ELN.
		return
	}

	// Warn about items that fell back to URL (no PURL) — that hints at a
	// transient `iron stat` failure during the upload and the comment will
	// show a path-based link instead of the persistent one. Visible to the
	// operator so they can re-run `mus irods upload` to refresh the PURL.
	for _, it := range items {
		if it.PURL == "" {
			fmt.Fprintf(stderr,
				"  NOTE: %s has no PURL — ELN link will be path-based and break on rename/move\n",
				it.RemotePath)
		}
	}

	title := buildELNCommentTitle(items)
	body := buildELNCommentHTML(env, items)

	jid, err := client.CreateCommentSection(expID, title, body)
	if err != nil {
		fmt.Fprintf(stderr, "  WARNING: could not post ELN comment: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "  posted ELN comment to experiment %d (section %d)\n", expID, jid)
}

func buildELNCommentTitle(items []uploadedItem) string {
	nFiles, nFolders, nArchives := 0, 0, 0
	for _, it := range items {
		switch it.Kind {
		case "file":
			nFiles++
		case "folder":
			nFolders++
		case "archive":
			nArchives++
		}
	}
	var parts []string
	if nFiles > 0 {
		parts = append(parts, pluralNoun(nFiles, "file"))
	}
	if nFolders > 0 {
		parts = append(parts, pluralNoun(nFolders, "folder"))
	}
	if nArchives > 0 {
		parts = append(parts, pluralNoun(nArchives, "archive"))
	}
	return "mus: uploaded " + strings.Join(parts, ", ") + " to iRODS"
}

func pluralNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// buildELNCommentHTML renders the comment body. Layout:
//
//   <p>summary line: who / when / data_project / total size</p>
//   <p>working directory</p>
//   <table>
//     Kind | iRODS path (clickable; href = PURL)  | Size | Sha256
//   </table>
//   <p>footnote when any item lacks a PURL (rare)</p>
//
// PURL is always the link target when present (persistent across moves /
// renames). If iron stat failed during the upload, we fall back to the
// path-based URL and add a one-line footnote so the reader knows why.
func buildELNCommentHTML(env *config.Env, items []uploadedItem) string {
	var b strings.Builder

	host, _ := os.Hostname()
	var who string
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	cwd, _ := os.Getwd()

	// --- summary line ---
	var total int64
	for _, it := range items {
		total += it.Size
	}
	fmt.Fprintf(&b,
		"<p>Uploaded via <code>mus</code> by <code>%s</code>@<code>%s</code> at %s — %d item(s), %s total.</p>",
		html.EscapeString(who),
		html.EscapeString(host),
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		len(items),
		html.EscapeString(folder.FormatHumanBytes(total)))

	// data_project / experiment_name pulled from the cascade so the
	// experiment record has redundant context (the experiment is also where
	// this comment lives, but the reader of an exported PDF might not see
	// that).
	var ctxBits []string
	if dp := env.String("data_project"); dp != "" {
		ctxBits = append(ctxBits, "data_project=<code>"+html.EscapeString(dp)+"</code>")
	}
	if en := env.String("eln_experiment_name"); en != "" {
		ctxBits = append(ctxBits, "experiment=<code>"+html.EscapeString(en)+"</code>")
	}
	if len(ctxBits) > 0 {
		fmt.Fprintf(&b, "<p>%s</p>", strings.Join(ctxBits, " &nbsp;·&nbsp; "))
	}
	if cwd != "" {
		fmt.Fprintf(&b, "<p>Source: <code>%s:%s</code></p>",
			html.EscapeString(host),
			html.EscapeString(cwd))
	}

	// --- per-item table ---
	anyFallback := false
	b.WriteString(`<table border="1" cellpadding="4" cellspacing="0">`)
	b.WriteString(`<thead><tr><th>Kind</th><th>iRODS path</th><th>Size</th><th>sha256</th></tr></thead><tbody>`)
	for _, it := range items {
		// Always prefer PURL. Fall back to URL with a visible marker; the
		// stderr message above already nagged the operator.
		href := it.PURL
		marker := ""
		if href == "" && it.URL != "" {
			href = it.URL
			marker = " *"
			anyFallback = true
		}
		var pathCell string
		if href != "" {
			pathCell = fmt.Sprintf(`<a href="%s"><code>%s</code></a>%s`,
				html.EscapeString(href),
				html.EscapeString(it.RemotePath),
				marker)
		} else {
			pathCell = fmt.Sprintf(`<code>%s</code>`, html.EscapeString(it.RemotePath))
		}
		shaCell := "—"
		if it.Sha256 != "" {
			shortSha := it.Sha256
			if len(shortSha) > 12 {
				shortSha = shortSha[:12]
			}
			shaCell = fmt.Sprintf(`<code title="%s">%s</code>`,
				html.EscapeString(it.Sha256), html.EscapeString(shortSha))
		}
		fmt.Fprintf(&b,
			`<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(it.Kind),
			pathCell,
			html.EscapeString(folder.FormatHumanBytes(it.Size)),
			shaCell)
	}
	b.WriteString(`</tbody></table>`)
	if anyFallback {
		b.WriteString(`<p><small>* path-based link only — PURL not available; ` +
			`will break if the object is renamed or moved.</small></p>`)
	}
	return b.String()
}
