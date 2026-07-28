package cli

// HTTP handlers for `mus serve`. Read-only endpoints in this
// commit: folder tree, cascade view, vocab autocomplete. Write
// endpoints (config set, eln link/disable, upload) land in
// follow-up commits.
//
// Path-safety discipline (the only non-obvious part): every
// endpoint that takes a `dir` query param resolves it against the
// directory the user invoked `mus serve` in, and refuses to walk
// above that root. We don't expose a way for the browser to
// inspect /etc/passwd or the user's home directory at large —
// `mus serve` is scoped to whatever the depositor's cwd was at
// boot time.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/dataproject"
	"codeberg.org/mfiers/mus/internal/elnmap"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"codeberg.org/mfiers/mus/internal/vocab"
)

// serveRoot is the directory the user invoked `mus serve` from.
// Captured at boot; every dir-resolving handler refuses to escape
// this root.
var serveRoot string

// registerServeAPI wires the API endpoints onto mux. Called from
// the same registerServeRoutes() that mounts the index page.
func registerServeAPI(mux *http.ServeMux) {
	// Read-only.
	mux.HandleFunc("/api/cwd", apiCwd)
	mux.HandleFunc("/api/tree", apiTree)
	mux.HandleFunc("/api/cascade", apiCascade)
	mux.HandleFunc("/api/vocab/", apiVocab) // trailing slash → subpath captures field name
	mux.HandleFunc("/api/known-keys", apiKnownKeys)
	// Write.
	mux.HandleFunc("/api/config/set", apiConfigSet)
	mux.HandleFunc("/api/config/unset", apiConfigUnset)
	mux.HandleFunc("/api/eln/disable", apiELNDisable)
	mux.HandleFunc("/api/eln/enable", apiELNEnable)
	mux.HandleFunc("/api/eln/link", apiELNLink)
	mux.HandleFunc("/api/eln/suggest-dp", apiELNSuggestDataProject)
}

// apiCwd returns the directory `mus serve` was invoked from.
// One-shot, no params. Browser uses this to label the root.
func apiCwd(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"root": serveRoot})
}

// treeEntry is one row of the folder/file listing.
type treeEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"` // relative to serveRoot, "/" separated
	Kind      string `json:"kind"` // "dir" | "file" | "sidecar"
	Size      int64  `json:"size,omitempty"`
	HasMusCfg bool   `json:"has_mus_cfg,omitempty"` // dirs only — green-flag the annotated folders
	// UploadedAt is set when this is a `file` AND its
	// <basename>.mus sidecar carries an irods_uploaded_at
	// timestamp. UI renders a ✓ badge so depositors see at a
	// glance which files are already on iRODS.
	UploadedAt string `json:"uploaded_at,omitempty"` // RFC3339 UTC; empty when no upload recorded
	// IRODSPath surfaces the remote path the sidecar records, so
	// the UI tooltip can show where the file landed.
	IRODSPath string `json:"irods_path,omitempty"`
}

// apiTree lists the immediate children of ?dir=<rel> (relative to
// serveRoot). No recursion — the browser expands one level at a
// time as the user clicks into folders. .mus sidecars are flagged
// as a distinct `kind` so the UI can dim them; dotfiles and a
// short blocklist of well-known noise files are skipped.
func apiTree(w http.ResponseWriter, r *http.Request) {
	abs, rel, err := resolveDirParam(r.URL.Query().Get("dir"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, fmt.Sprintf("read %s: %v", rel, err), http.StatusInternalServerError)
		return
	}
	out := make([]treeEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if shouldHideInTree(name) {
			continue
		}
		entryRel := relJoin(rel, name)
		entry := treeEntry{Name: name, Path: entryRel}
		if e.IsDir() {
			entry.Kind = "dir"
			if _, err := os.Stat(filepath.Join(abs, name, "mus.cfg")); err == nil {
				entry.HasMusCfg = true
			}
		} else {
			if sidecar.IsSidecar(name) {
				entry.Kind = "sidecar"
			} else {
				entry.Kind = "file"
				// Peek the matching sidecar (if any) for an
				// already-uploaded marker. Best-effort: any read
				// failure leaves the badge off rather than
				// pretending to know the upload state.
				scPath := sidecar.SidecarPath(filepath.Join(abs, name))
				if doc, err := readMaybe(scPath); err == nil && doc != nil &&
					doc.IRODS != nil && !doc.IRODS.UploadedAt.IsZero() {
					entry.UploadedAt = doc.IRODS.UploadedAt.UTC().Format("2006-01-02T15:04:05Z")
					entry.IRODSPath = doc.IRODS.Path
				}
			}
			if info, err := e.Info(); err == nil {
				entry.Size = info.Size()
			}
		}
		out = append(out, entry)
	}
	// Stable order: dirs first, then files; alphabetical within.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Kind == "dir") != (out[j].Kind == "dir") {
			return out[i].Kind == "dir"
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, map[string]any{
		"dir":     rel,
		"entries": out,
	})
}

// cascadeKey is one effective key in the resolved cascade.
type cascadeKey struct {
	Key    string `json:"key"`
	Value  string `json:"value"` // scalar serialisation; lists comma-joined
	IsList bool   `json:"is_list"`
	// Local is true when the key has a value set in THIS folder's
	// mus.cfg (as opposed to inherited from an ancestor). UI shows
	// an unset button only on local chips — unsetting an inherited
	// key would have no effect at this level.
	Local bool `json:"local"`
}

// apiCascade returns the resolved cascade for ?dir=<rel>, plus
// the list of mus.cfg / legacy .env files that contributed
// (root-most first). Each key is rendered as its display string
// — scalars verbatim, lists comma-joined. The UI uses this to
// populate the form on the right panel.
func apiCascade(w http.ResponseWriter, r *http.Request) {
	abs, rel, err := resolveDirParam(r.URL.Query().Get("dir"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	env, err := config.Load(abs)
	if err != nil {
		http.Error(w, fmt.Sprintf("load cascade for %s: %v", rel, err), http.StatusInternalServerError)
		return
	}
	// Local env (just THIS folder's mus.cfg, no ancestors) — used
	// to tag each cascade value as local vs inherited so the UI
	// can render an unset button only on the editable ones.
	localEnv, _ := config.LoadLocal(abs) // best-effort; nil → none local
	isLocal := func(key string) bool {
		if localEnv == nil {
			return false
		}
		return localEnv.Has(key)
	}
	all := env.All()
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]cascadeKey, 0, len(keys))
	for _, k := range keys {
		v := all[k]
		switch t := v.(type) {
		case string:
			out = append(out, cascadeKey{Key: k, Value: t, Local: isLocal(k)})
		case []string:
			out = append(out, cascadeKey{Key: k, Value: strings.Join(t, ","), IsList: true, Local: isLocal(k)})
		}
	}
	writeJSON(w, map[string]any{
		"dir":    rel,
		"values": out,
		"files":  env.Files(), // contributing cfg files, root-most first
	})
}

// apiKnownKeys returns the canonical list of mus.cfg keys the
// annotation form's key dropdown shows. Plus, for each key, a
// flag indicating whether the value is vocab-controlled (the form
// can then fetch /api/vocab/<key> for the value dropdown).
//
// Query param `manual=true` filters to the keys depositors
// realistically set by hand in the annotation form: biology /
// provenance fields. Hides keys that are owned by other parts of
// mus and shouldn't be edited in this picker:
//   - eln_*  — managed by the ELN picker (link / disable)
//   - de_*   — managed by `mus de check` per-deposit
//   - irods_* — stamped automatically on upload
//   - h5ad_* — stamped automatically by `mus h5ad check`
//   - data_project — derived from the ELN linkage
//
// Without the flag, returns every recognised key (legacy / debug
// path).
func apiKnownKeys(w http.ResponseWriter, r *http.Request) {
	manualOnly := r.URL.Query().Get("manual") == "true"
	type keyDesc struct {
		Key   string `json:"key"`
		Vocab bool   `json:"vocab"`
		// Group is the optgroup label the UI uses to bucket keys
		// (Biology, Provenance, References, Tags, Ontology, Other).
		// Letting the server own this keeps the grouping in one
		// place — UI changes don't require knowing the semantic
		// categories.
		Group string `json:"group"`
	}
	keys := make([]keyDesc, 0, len(knownConfigKeys))
	for _, k := range knownConfigKeys {
		if manualOnly && !isManualAnnotationKey(k) {
			continue
		}
		keys = append(keys, keyDesc{
			Key:   k,
			Vocab: isVocabConfigKey(k),
			Group: groupForConfigKey(k),
		})
	}
	writeJSON(w, map[string]any{"keys": keys})
}

// groupForConfigKey returns the optgroup label for a config key —
// the visible "section heading" the UI's <select> uses to cluster
// related keys. Order matters for the UI; the renderer walks the
// groups in this declared order.
func groupForConfigKey(key string) string {
	switch key {
	case "organism", "technology", "suspension_type",
		"sex", "age", "strain",
		"tissue", "cell_type", "disease", "condition":
		return "Biology"
	case "title", "contributor", "year",
		"source", "license", "source_description",
		"access", "accession":
		return "Provenance"
	case "doi", "pmid", "link":
		return "References"
	case "tag", "collaborator", "note":
		return "Tags & free text"
	case "organism_ontology_term_id", "tissue_ontology_term_id",
		"cell_type_ontology_term_id", "assay_ontology_term_id",
		"sex_ontology_term_id", "disease_ontology_term_id":
		return "Ontology IDs"
	}
	return "Other"
}

// isManualAnnotationKey reports whether `key` is one a depositor
// would normally set in the mus serve annotation form, vs an
// auto-managed key owned by a specific mus subsystem (ELN, DE,
// iRODS upload stamp, h5ad inspector).
func isManualAnnotationKey(key string) bool {
	switch {
	case key == "data_project":
		return false // ELN picker territory
	case strings.HasPrefix(key, "eln_"):
		return false
	case strings.HasPrefix(key, "de_"):
		return false
	case strings.HasPrefix(key, "irods_"):
		return false
	case strings.HasPrefix(key, "h5ad_"):
		return false
	}
	return true
}

// apiVocab returns the controlled-value list for one field. URL
// path: /api/vocab/<field>. Browser uses this to populate
// <datalist> autocomplete; the field name lives in the URL so
// caching at the browser side is trivial.
func apiVocab(w http.ResponseWriter, r *http.Request) {
	field := strings.TrimPrefix(r.URL.Path, "/api/vocab/")
	if field == "" || strings.Contains(field, "/") {
		http.Error(w, "field name required (no slashes)", http.StatusBadRequest)
		return
	}
	res, err := vocab.Load(r.Context(), vocab.LoadOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("load vocab: %v", err), http.StatusInternalServerError)
		return
	}
	values, ok := res.Vocabulary.Vocab[field]
	if !ok {
		// Not a vocab-controlled field — return empty list rather than 404
		// so the UI can render "no controlled vocab" gracefully.
		writeJSON(w, map[string]any{
			"field":  field,
			"vocab":  false,
			"values": []string{},
		})
		return
	}
	writeJSON(w, map[string]any{
		"field":  field,
		"vocab":  true,
		"values": values,
	})
}

// --- helpers -----------------------------------------------------------------

// resolveDirParam takes a relative `dir` URL param and returns
// (absolute path, normalised relative path, error). Refuses any
// path that escapes serveRoot via .. or absolute paths.
// Empty / "." / "/" → serveRoot itself.
func resolveDirParam(raw string) (abs, rel string, err error) {
	if serveRoot == "" {
		return "", "", errors.New("server root not initialised (internal bug)")
	}
	// Refuse intent-to-escape up-front: any `..` segment OR absolute
	// path is rejected. Silent clamping (where ".." just resolves to
	// root) would mask user intent — better to fail loudly so the
	// browser knows the request was malformed.
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") {
		return "", "", fmt.Errorf("absolute path %q not allowed", raw)
	}
	for _, segment := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return "", "", fmt.Errorf("path %q escapes the server root (.. not allowed)", raw)
		}
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "." {
		cleaned = ""
	}
	abs = filepath.Join(serveRoot, cleaned)
	// Belt-and-braces: confirm the abs path is still under serveRoot
	// AFTER resolving any symlinks. Without EvalSymlinks, a symlink
	// inside the tree could point outside and the guard would accept
	// it. Windows path case-insensitivity also comes in here — Rel
	// uses OS-appropriate case handling.
	if err := verifyUnderServeRoot(abs); err != nil {
		return "", "", fmt.Errorf("path %q %v", raw, err)
	}
	rootAbs, _ := filepath.Abs(serveRoot)
	rel, relErr := filepath.Rel(rootAbs, abs)
	if relErr != nil {
		return "", "", fmt.Errorf("path %q escapes the server root", raw)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", fmt.Errorf("not a directory: %s", rel)
	}
	if rel == "." {
		rel = ""
	}
	return abs, rel, nil
}

// verifyUnderServeRoot symlink-resolves both `path` and serveRoot,
// then confirms the resolved path is still lexically inside the
// resolved root. Uses filepath.Rel + HasPrefix(".."), same test as
// the mainline check but AFTER EvalSymlinks so a mid-tree symlink
// can't be used to escape.
//
// On Windows, filepath uses case-insensitive path comparison in
// EvalSymlinks + Abs, so `C:\Users\mark` and `c:\users\Mark` behave
// as the same location — which is what we want.
func verifyUnderServeRoot(abs string) error {
	rootAbs, err := filepath.Abs(serveRoot)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	// EvalSymlinks fails on paths that don't exist yet; that's fine
	// for existing-only reads. For not-yet-created directories the
	// caller would need a different guard (we don't have such a
	// call site today).
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// Root should always exist; if it doesn't, refuse everything.
		return fmt.Errorf("resolve root symlinks: %w", err)
	}
	absResolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Non-existent target — fall through to the lexical check
		// below (the caller will hit os.Stat and get its own ENOENT).
		absResolved = abs
	}
	rel, err := filepath.Rel(rootResolved, absResolved)
	if err != nil {
		return errors.New("escapes the server root")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("escapes the server root (via symlink or otherwise)")
	}
	return nil
}

// resolveAnyPathParam is like resolveDirParam but accepts files
// AND directories. Used by /api/upload where each selected path may
// be a file or a folder. Returns the absolute path; same path-safety
// guards apply (no absolute, no `..`, must resolve under serveRoot).
func resolveAnyPathParam(raw string) (string, error) {
	if serveRoot == "" {
		return "", errors.New("server root not initialised (internal bug)")
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") {
		return "", fmt.Errorf("absolute path not allowed")
	}
	for _, segment := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return "", fmt.Errorf("escapes the server root (.. not allowed)")
		}
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "." {
		cleaned = ""
	}
	abs := filepath.Join(serveRoot, cleaned)
	if err := verifyUnderServeRoot(abs); err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// relJoin joins a parent relative path with a child name using
// forward slashes (so the wire format is OS-independent).
func relJoin(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "/" + child
}

// shouldHideInTree filters out files the UI shouldn't show.
// Conservative — only drops obvious noise. Sidecars are SHOWN
// (with kind=sidecar) so the UI can render them as the file's
// passport rather than hiding them silently.
func shouldHideInTree(name string) bool {
	if name == "" {
		return true
	}
	// Dotfiles are usually noise but EXCEPT the well-known config
	// files mus and the user actually look at.
	if strings.HasPrefix(name, ".") {
		switch name {
		case ".env": // legacy mus config — keep visible
			return false
		}
		return true
	}
	return false
}

// writeJSON writes value as a pretty-printed JSON body with
// Content-Type. Errors here are server-side bugs — log to stderr
// and let the half-written body speak for itself.
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "mus serve: encode JSON: %v\n", err)
	}
}

// --- write endpoints ---------------------------------------------------------

// apiConfigSet — POST /api/config/set with JSON body
// {"dir": "<rel>", "key": "<k>", "value": "<v>", "force": false}.
// Writes the (key, value) to <dir>/mus.cfg after running the same
// resolveConfigKey + resolveConfigValue normalisation + vocab
// validation the CLI applies via `mus config set`.
//
// Response on success: { "key": "<canonical-k>", "value": "<canonical-v>" }.
// Note: canonicalisation may rewrite the key (CellType → cell_type)
// and/or value (HuMaN → human); the response reflects what landed.
func apiConfigSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir   string `json:"dir"`
		Key   string `json:"key"`
		Value string `json:"value"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	abs, _, err := resolveDirParam(req.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Both layers of CLI validation: key normalisation + value vocab check.
	var noteSink strings.Builder
	resolvedKey, err := resolveConfigKey(req.Key, req.Force, &noteSink)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolvedValue, err := resolveConfigValue(r.Context(), resolvedKey, req.Value, req.Force, &noteSink)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Save(abs, map[string]string{resolvedKey: resolvedValue}); err != nil {
		http.Error(w, fmt.Sprintf("save: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"key":   resolvedKey,
		"value": resolvedValue,
		"notes": noteSink.String(), // stderr-style notes from the resolvers, if any
	})
}

// apiConfigUnset — POST /api/config/unset with JSON body
// {"dir": "<rel>", "key": "<k>"}. Removes the key from that
// folder's mus.cfg. Returns 200 on success or no-op.
func apiConfigUnset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir string `json:"dir"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	abs, _, err := resolveDirParam(req.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Unset(abs, req.Key); err != nil {
		http.Error(w, fmt.Sprintf("unset: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiELNDisable — POST /api/eln/disable with JSON body
// {"dir": "<rel>"}. Writes eln_disabled=true to the folder's
// mus.cfg, so subsequent `mus irods upload` / `mus s3 upload`
// from that folder proceed without an ELN linkage. Same effect
// as `mus eln disable`.
func apiELNDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	abs, _, err := resolveDirParam(req.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Save(abs, map[string]string{"eln_disabled": "true"}); err != nil {
		http.Error(w, fmt.Sprintf("save: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiELNEnable — POST /api/eln/enable with JSON body
// {"dir": "<rel>"}. Inverse of disable; removes the
// eln_disabled key so the folder returns to the "undecided"
// state (depositor must explicitly link an experiment or
// re-disable). Idempotent.
func apiELNEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	abs, _, err := resolveDirParam(req.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Unset(abs, "eln_disabled"); err != nil {
		http.Error(w, fmt.Sprintf("unset: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiELNLink — POST /api/eln/link with JSON body
//
//	{"dir": "<rel>", "experiment_id": "<id>", "data_project": "<dp>", "force": false}.
//
// Fetches the experiment metadata from eLabJournal, validates the
// supplied data_project, and stamps both into the folder's
// mus.cfg. Also clears any pre-existing eln_disabled flag — `link`
// implies `enable`, matching the CLI semantics.
//
// `data_project` is REQUIRED in the request (no interactive
// prompt path through HTTP). The GUI is responsible for surfacing
// the chosen value back to the user before POSTing. The endpoint
// will return 400 if it's missing or fails dataproject.ValidateName.
//
// If a different experiment is already linked in this folder, the
// endpoint refuses unless `force` is true. Same idempotency guard
// as the CLI.
func apiELNLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir          string `json:"dir"`
		ExperimentID string `json:"experiment_id"`
		DataProject  string `json:"data_project"`
		Force        bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	abs, _, err := resolveDirParam(req.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	expID, err := parseELNExperimentID(req.ExperimentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.DataProject == "" {
		http.Error(w, "data_project is required (no interactive prompt over HTTP)", http.StatusBadRequest)
		return
	}
	if err := dataproject.ValidateName(req.DataProject); err != nil {
		http.Error(w, fmt.Sprintf("data_project %q rejected: %v", req.DataProject, err), http.StatusBadRequest)
		return
	}
	expIDStr := strconv.FormatInt(expID, 10)
	// Idempotency guard mirrors runELNLink.
	localEnv, _ := config.LoadLocal(abs)
	if existing := localEnv.String("eln_experiment_id"); existing != "" && !req.Force {
		if existing == expIDStr {
			// Re-link to the same ID is a no-op success — saves a refresh round-trip.
			writeJSON(w, map[string]any{"ok": true, "noop": true,
				"eln_experiment_id": existing})
			return
		}
		http.Error(w, fmt.Sprintf(
			"folder is already linked to experiment %s; pass force=true to relink", existing),
			http.StatusConflict)
		return
	}
	client, err := openELN()
	if err != nil {
		http.Error(w, fmt.Sprintf("open ELN: %v", err), http.StatusInternalServerError)
		return
	}
	info, err := client.ExpInfo(expID)
	if err != nil {
		http.Error(w, fmt.Sprintf("fetch experiment %d: %v", expID, err), http.StatusBadGateway)
		return
	}
	kv := experimentToKV(info)
	kv["data_project"] = req.DataProject
	if err := config.Save(abs, kv); err != nil {
		http.Error(w, fmt.Sprintf("save: %v", err), http.StatusInternalServerError)
		return
	}
	// link implies enable.
	if localEnv.Has("eln_disabled") {
		_ = config.Unset(abs, "eln_disabled")
	}
	writeJSON(w, map[string]any{
		"ok":     true,
		"values": kv,
	})
}

// apiELNSuggestDataProject — POST /api/eln/suggest-dp with
//
//	{"experiment_id": "<id>"}
//
// Looks the experiment up via the ELN API, runs the same
// suggestion logic the CLI's `mus eln link` uses (cached value
// from another folder if available, else "FirstCollaboratorSurname"
// + current year), and returns:
//
//	{"data_project": "<proposed>", "source": "cached|derived",
//	 "experiment_name": "...", "collaborators": [...]}
//
// UI pre-fills the data_project input with the suggestion. The
// user can still edit before hitting Link. Anything else mus
// would have prompted for (existing cascade value, --force) is
// out of scope — the GUI's Link button posts to /api/eln/link
// which has its own validation.
func apiELNSuggestDataProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ExperimentID string `json:"experiment_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode body: %v", err), http.StatusBadRequest)
		return
	}
	expID, err := parseELNExperimentID(req.ExperimentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	client, err := openELN()
	if err != nil {
		http.Error(w, fmt.Sprintf("open ELN: %v", err), http.StatusInternalServerError)
		return
	}
	info, err := client.ExpInfo(expID)
	if err != nil {
		http.Error(w, fmt.Sprintf("fetch experiment %d: %v", expID, err), http.StatusBadGateway)
		return
	}
	// First check the cross-folder mapping cache for a known
	// data_project for this experiment — saves the depositor
	// retyping a value an earlier folder already settled on.
	source := "derived"
	suggestion := ""
	if store, err := elnmap.Open(""); err == nil {
		if cached, _ := store.Lookup(strconv.FormatInt(expID, 10)); cached != nil &&
			cached.DataProject != "" {
			suggestion = cached.DataProject
			source = "cached"
		}
	}
	if suggestion == "" {
		suggestion = suggestDataProjectFromELN(info)
	}
	writeJSON(w, map[string]any{
		"data_project":    suggestion,
		"source":          source,
		"experiment_name": info.ExperimentName,
		"collaborators":   info.Collaborators,
	})
}

// _ context import retained so a future endpoint that wants a
// per-request context doesn't have to re-add it.
var _ = context.Background
