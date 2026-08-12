package cli

// Action handlers for `mus serve` — the ones that DO things rather
// than read/write metadata. Split out from serve_handlers.go to
// keep concerns separated:
//
//   serve_handlers.go  — config / cascade / vocab reads + writes
//   serve_actions.go   — pre-flight checklist + upload trigger
//
// Both files share the same package-level state (serveRoot,
// resolveDirParam helpers).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// registerServeActions wires the action endpoints onto mux. Called
// from registerServeRoutes() alongside registerServeAPI().
func registerServeActions(mux *http.ServeMux) {
	mux.HandleFunc("/api/preflight", apiPreflight)
	mux.HandleFunc("/api/upload", apiUpload)
}

// preflightCheck is one row of the pre-flight checklist. The UI
// renders these as green/red chips on the right panel.
type preflightCheck struct {
	Key     string `json:"key"`         // machine name (e.g. "data_project")
	Label   string `json:"label"`       // human label ("data_project set")
	OK      bool   `json:"ok"`          // green ✓ vs red ✗
	Detail  string `json:"detail"`      // present-tense one-liner (the value, or what's missing)
	BlockUp bool   `json:"block_upload"` // if false, upload still proceeds (warning only)
}

// apiPreflight returns the checklist for the folder ?dir=<rel>:
//
//   - data_project set in the cascade
//   - ELN state declared (linked OR disabled)
//   - iRODS secret stored in the keyring
//   - at least one file present besides mus.cfg / sidecars
//
// The UI uses this to gate the upload button. Server-side, the
// underlying `mus * upload` command does its OWN gating — preflight
// is purely for early UX feedback.
func apiPreflight(w http.ResponseWriter, r *http.Request) {
	abs, rel, err := resolveDirParam(r.URL.Query().Get("dir"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	checks := []preflightCheck{}

	env, _ := config.Load(abs)

	// data_project
	dp := ""
	if env != nil {
		dp = env.String("data_project")
	}
	checks = append(checks, preflightCheck{
		Key:     "data_project",
		Label:   "data_project set",
		OK:      dp != "",
		Detail:  ifEmpty(dp, "missing — set with `mus config set data_project <name>`"),
		BlockUp: true,
	})

	// ELN state (linked OR disabled)
	elnLinked := env != nil && env.String("eln_experiment_id") != ""
	elnDisabled := env != nil && env.String("eln_disabled") == "true"
	switch {
	case elnLinked:
		checks = append(checks, preflightCheck{
			Key: "eln", Label: "ELN linked", OK: true,
			Detail: "experiment " + env.String("eln_experiment_id"), BlockUp: true,
		})
	case elnDisabled:
		checks = append(checks, preflightCheck{
			Key: "eln", Label: "ELN disabled for this folder", OK: true,
			Detail: "eln_disabled=true (no experiment linked)", BlockUp: true,
		})
	default:
		checks = append(checks, preflightCheck{
			Key: "eln", Label: "ELN state declared", OK: false,
			Detail:  "undecided — link with `mus eln link <id>` or disable",
			BlockUp: true,
		})
	}

	// iron CLI present on PATH. iRODS auth itself lives in iron's
	// own credentials file (NOT mus's keyring) — we can't probe that
	// without shelling out. The binary's presence is the cheapest
	// proxy: if iron isn't installed, the upload definitely fails.
	// If it IS installed, auth state is its own thing and `mus irods
	// upload` will surface the right error on misconfiguration.
	ironOK := false
	ironDetail := "not on PATH — install from " +
		"https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html, " +
		"then run `iron auth` (or `mus irods login` for the canonical command)"
	if _, err := exec.LookPath("iron"); err == nil {
		ironOK = true
		ironDetail = "on PATH (run `iron auth status` to verify the session)"
	}
	checks = append(checks, preflightCheck{
		Key:     "iron_cli",
		Label:   "iron CLI available",
		OK:      ironOK,
		Detail:  ironDetail,
		BlockUp: true, // mus serve uploads via iron — must be present
	})

	// At least one data file present (ignore mus.cfg + sidecars + dotfiles)
	dataFileCount := 0
	if entries, err := os.ReadDir(abs); err == nil {
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || strings.HasPrefix(n, ".") || n == "mus.cfg" ||
				strings.HasSuffix(n, ".mus") {
				continue
			}
			dataFileCount++
		}
	}
	checks = append(checks, preflightCheck{
		Key:     "files",
		Label:   "data files present",
		OK:      dataFileCount > 0,
		Detail:  fmt.Sprintf("%d non-config file(s) in this folder", dataFileCount),
		BlockUp: true,
	})

	writeJSON(w, map[string]any{
		"dir":    rel,
		"checks": checks,
	})
}

// apiUpload shells out to `mus irods upload`, streaming the
// combined stdout+stderr back as the response body.
//
// Method: POST. Body:
//
//	{"paths": ["<rel1>", "<rel2>", ...], "message": "<msg>"}
//
// Each path is resolved against serveRoot with the same path-safety
// guard the read API uses (no absolute paths, no `..`). "" and "."
// both resolve to serveRoot (whole-folder upload). mus irods upload
// accepts a mix of files and folders — same here.
//
// S3 (= anthive catalog) upload is intentionally NOT exposed by
// mus serve. The GUI is for the everyday "deposit-some-files-to-
// iRODS-and-log-to-ELN" workflow; catalog deposits go through the
// CLI where the depositor has the full DE-check / h5ad-check
// machinery in front of them.
//
// The browser opens the response as a stream and shows lines in a
// log pane. Synchronous — the request stays open for the duration
// of the upload. For v1 this is fine; multi-GB uploads block the
// browser tab, but lab depositors are uploading folders measured
// in hundreds of MB.
//
// We invoke OURSELVES (os.Executable) rather than re-implementing
// the upload logic. This way the underlying gates (ELN, vocab,
// sidecar check) run identically to the CLI path — no second
// implementation to drift.
func apiUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Paths   []string `json:"paths"`
		Message string   `json:"message"`
		DryRun  bool     `json:"dry_run"`
		// Pack=true → pass --pack tar.gz to mus irods upload so
		// dense folders get archived before upload. Default off:
		// the depositor opts in via the GUI checkbox.
		Pack bool `json:"pack"`
		// Force=true → pass --force so a file whose bytes differ from
		// the existing remote object is OVERWRITTEN without the
		// interactive [y/N] prompt (which the non-TTY serve subprocess
		// can't answer). Default off — overwriting the archive is
		// destructive, so the depositor opts in via the GUI checkbox.
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 {
		http.Error(w, "at least one path is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w,
			"upload message is required — describe what/why this upload is happening",
			http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Message, "\r\n") {
		http.Error(w, "upload message must be a single line (no newlines)",
			http.StatusBadRequest)
		return
	}
	// Resolve every path. Refuse the whole batch if ANY single
	// path escapes — never upload a partial set silently.
	absPaths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		abs, err := resolveAnyPathParam(p)
		if err != nil {
			http.Error(w, fmt.Sprintf("path %q: %v", p, err), http.StatusBadRequest)
			return
		}
		absPaths = append(absPaths, abs)
	}
	// Skip-already-uploaded: for any path pointing at a single
	// FILE, peek the matching sidecar. If it carries an
	// irods_uploaded_at timestamp, drop it from this batch — mus's
	// 0.2.27 skip-if-same gate would no-op the upload anyway, but
	// the GUI promise is "never re-upload" so we drop pre-emptively
	// and surface what was skipped in the log. Folders pass through
	// unchanged (mus walks them and the per-file skip-if-same gate
	// inside `mus irods upload` does the right thing).
	upload, skipped := filterAlreadyUploaded(absPaths)
	if len(upload) == 0 {
		// Everything in the batch was already uploaded — return a
		// friendly note via the streaming response, not a 400.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fmt.Fprintln(w, "✓ nothing to upload — every selected file already has a sidecar")
		fmt.Fprintln(w, "  recording an iRODS upload. Re-tagging or re-uploading on top of")
		fmt.Fprintln(w, "  these would be a no-op; use the CLI if you really need to force.")
		for _, p := range skipped {
			fmt.Fprintln(w, "  - skipped: "+p)
		}
		return
	}
	subcommand := []string{"irods", "upload", "-m", req.Message}
	if req.DryRun {
		// iron prints what it WOULD upload without transferring
		// bytes. Useful as a "what does mus think I'm about to do"
		// preview before the real run.
		subcommand = append(subcommand, "--dry-run")
	}
	if req.Pack {
		// Auto-archive dense folders into a single tar.gz before
		// upload. Mirrors `mus irods upload --pack tar.gz`. No-op
		// for file args.
		subcommand = append(subcommand, "--pack", "tar.gz")
	}
	if req.Force {
		// Overwrite remote objects whose bytes differ, without the
		// [y/N] prompt the non-TTY subprocess can't answer. Mirrors
		// `mus irods upload --force`.
		subcommand = append(subcommand, "--force")
	}
	subcommand = append(subcommand, upload...)
	self, err := os.Executable()
	if err != nil {
		http.Error(w, fmt.Sprintf("locate mus binary: %v", err), http.StatusInternalServerError)
		return
	}
	// Drive output as a stream — the browser polls with `fetch()`
	// and reads response.body chunks. Content-Type: text/plain with
	// X-Content-Type-Options nosniff keeps it from being treated as
	// HTML.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	flushLine := func(line string) {
		fmt.Fprintln(w, line)
		if flusher != nil {
			flusher.Flush()
		}
	}
	for _, p := range skipped {
		flushLine("  ↷ skipping " + p + " — sidecar already records an iRODS upload")
	}
	flushLine(fmt.Sprintf("$ mus %s", strings.Join(subcommand, " ")))
	cmd := exec.CommandContext(r.Context(), self, subcommand...)
	// Run from serveRoot so the cascade `mus.cfg` cwd lookup picks
	// up the right cascade even when the user has selected a
	// sub-folder.
	cmd.Dir = serveRoot
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		flushLine(fmt.Sprintf("ERROR: stdout pipe: %v", err))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		flushLine(fmt.Sprintf("ERROR: stderr pipe: %v", err))
		return
	}
	if err := cmd.Start(); err != nil {
		flushLine(fmt.Sprintf("ERROR: start: %v", err))
		return
	}
	// Fan-in stdout+stderr into the response. Two scanners so we
	// preserve any interleaving the child produced; line buffering
	// keeps the browser responsive without an explicit poll.
	done := make(chan struct{}, 2)
	go streamLines(stdout, flushLine, done)
	go streamLines(stderr, flushLine, done)
	<-done
	<-done
	if err := cmd.Wait(); err != nil {
		flushLine(fmt.Sprintf("EXIT: %v", err))
		return
	}
	flushLine("EXIT: 0 (upload completed)")
}

// filterAlreadyUploaded splits `paths` into (toUpload, skipped).
// A path is skipped ONLY when we can prove it's a no-op re-upload:
//   - it points at an existing file (not a directory)
//   - a matching <basename>.mus sidecar exists with a non-zero
//     irods_uploaded_at timestamp
//   - AND the file is unchanged since that sidecar was written, i.e.
//     its current size + mtime still match the sidecar (doc.Stale).
//
// The staleness gate is the important part: a file that was uploaded
// and then locally EDITED must NOT be skipped — its recorded sha256 no
// longer describes the bytes on disk, so re-uploading is real work, not
// a no-op. Without this check the changed version would silently never
// reach iRODS.
//
// Directories pass through unchanged — mus irods upload's own
// per-file skip-if-same gate handles their contents at upload time.
//
// Any I/O error (stat, sidecar parse) is treated as "we can't prove
// this is already uploaded, so let it through." Better to risk a no-op
// re-run than to silently exclude a file the depositor selected.
func filterAlreadyUploaded(paths []string) (toUpload []string, skipped []string) {
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			toUpload = append(toUpload, p)
			continue
		}
		scPath := sidecar.SidecarPath(p)
		doc, err := readMaybe(scPath)
		if err != nil || doc == nil || doc.IRODS == nil ||
			doc.IRODS.UploadedAt.IsZero() {
			toUpload = append(toUpload, p)
			continue
		}
		// Uploaded before — but only skip if the local file is
		// unchanged since. A stale sidecar (size/mtime differ) means
		// the file was edited: upload the new version.
		if doc.Stale(st) {
			toUpload = append(toUpload, p)
			continue
		}
		skipped = append(skipped, p)
	}
	return toUpload, skipped
}

// streamLines copies lines from r into flushLine until r is
// exhausted. Signals done when finished. bufio.Scanner has a
// default 64KB line cap — mus output lines are short, well under.
func streamLines(r io.Reader, flush func(string), done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		flush(sc.Text())
	}
}

// ifEmpty returns fallback when s is empty, else s.
func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
