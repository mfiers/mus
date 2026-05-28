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
type uploadedItem struct {
	Kind       string
	LocalPath  string
	RemotePath string
	URL        string
	PURL       string
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

	title := buildELNCommentTitle(items)
	body := buildELNCommentHTML(items)

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

func buildELNCommentHTML(items []uploadedItem) string {
	var b strings.Builder

	host, _ := os.Hostname()
	var who string
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	cwd, _ := os.Getwd()

	fmt.Fprintf(&b,
		"<p>Uploaded via <code>mus</code> from <code>%s</code>@<code>%s</code> on %s.</p>",
		html.EscapeString(who),
		html.EscapeString(host),
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))
	if cwd != "" {
		fmt.Fprintf(&b, "<p>Working directory: <code>%s</code></p>",
			html.EscapeString(cwd))
	}

	b.WriteString(`<table border="1" cellpadding="4" cellspacing="0">`)
	b.WriteString(`<thead><tr><th>Kind</th><th>Local</th><th>iRODS path</th><th>Size</th><th>Link</th></tr></thead><tbody>`)
	for _, it := range items {
		var linkCell string
		switch {
		case it.PURL != "":
			linkCell = fmt.Sprintf(`<a href="%s">PURL</a>`, html.EscapeString(it.PURL))
		case it.URL != "":
			linkCell = fmt.Sprintf(`<a href="%s">browse</a>`, html.EscapeString(it.URL))
		}
		fmt.Fprintf(&b,
			`<tr><td>%s</td><td><code>%s</code></td><td><code>%s</code></td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(it.Kind),
			html.EscapeString(it.LocalPath),
			html.EscapeString(it.RemotePath),
			html.EscapeString(folder.FormatHumanBytes(it.Size)),
			linkCell)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}
