// Package pikahook emits an audit-trail entry to the user's pika
// note-taking server after each mus data operation. Pika is the
// timeline of "what happened to what data"; mus stays the
// operational tool.
//
// Design constraint, hard: pikahook MUST NEVER disrupt mus. Every
// failure path is silent and returns nil:
//
//   - `pika` binary not on PATH                       → silent
//   - `pika_log_enabled` not set in the .env cascade  → silent
//   - subprocess exits non-zero / hangs / times out   → silent
//   - pika server unreachable                         → silent
//   - JSON marshal fails (shouldn't happen)           → silent
//
// The only externally-visible side effect on a successful call is
// that a note gets written to the pika server. On any other outcome
// mus behaves as if Emit was a no-op.
//
// Wire format: `pika log` accepts a JSON object on stdin (per its
// own --help). We POST {"type": "mus-<action>", "message": "...",
// "meta": {key: value, …}}. This avoids the brittle `--meta k=v`
// CLI form that pika doesn't actually support and gives us a clean
// place to put arbitrary structured fields.
package pikahook

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
)

// EmitTimeout caps the pika subprocess so a slow or stuck pika
// server can't perceptibly delay a mus invocation. 3 seconds is
// generous — pika log typically returns in <100ms.
const EmitTimeout = 3 * time.Second

// PikaBinary is the name of the executable we look up on PATH.
// Exported so tests can stub it (point at a fake on a tempdir PATH).
var PikaBinary = "pika"

// Emit posts an audit-trail entry to pika describing one mus
// operation. ALWAYS returns nil — see package docs for the
// non-disruption contract.
//
// `action` becomes the note type, prefixed: "tag" → "mus-tag",
// "irods-upload" → "mus-irods-upload". `fields` are merged into the
// JSON `meta` object verbatim; the empty `error` field is the
// convention for marking a failed operation.
//
// Whether to actually emit is gated on `pika_log_enabled=true` in
// the mus .env cascade — opt-in by design. Anyone who doesn't set
// it gets the original mus behaviour: zero pika traffic, zero
// dependency on pika even being installed.
func Emit(ctx context.Context, dir, action string, fields map[string]string) error {
	if !shouldEmit(dir) {
		return nil
	}
	if _, err := exec.LookPath(PikaBinary); err != nil {
		return nil // pika not installed — silent
	}

	payload := buildPayload(action, fields)
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil // unreachable for our payload shape, but stay safe
	}

	runCtx, cancel := context.WithTimeout(ctx, EmitTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, PikaBinary)
	cmd.Stdin = bytes.NewReader(raw)
	// Discard pika's output unconditionally. We don't surface it on
	// stderr because the whole point is invisibility — pika's "200 OK"
	// or "could not reach server" is noise on the mus terminal.
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run() // ignore every error
	return nil
}

// shouldEmit checks the mus .env cascade for `pika_log_enabled=true`.
// dir is typically the working directory of the mus invocation;
// pass "" to read from os.Getwd implicitly via config.Load("").
// Any error during config load is treated as "not enabled".
func shouldEmit(dir string) bool {
	if dir == "" {
		dir = "."
	}
	env, err := config.Load(dir)
	if err != nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(env.String("pika_log_enabled")))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

// buildPayload constructs the JSON object pika expects on stdin.
// Stable field ordering for the meta map so the tests can compare
// JSON byte-for-byte. The visible `message` is a human-readable
// one-liner; the full structured data lives in `meta`.
func buildPayload(action string, fields map[string]string) map[string]any {
	noteType := "mus-" + action

	msg := fields["message"]
	if msg == "" {
		msg = composeFallbackMessage(action, fields)
	}

	// Copy fields → meta with deterministic key order. We DON'T strip
	// "message" — keep it in meta as well so the audit trail still
	// shows it on its own field even if pika later reformats the body.
	meta := map[string]string{}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		meta[k] = fields[k]
	}

	return map[string]any{
		"type":    noteType,
		"message": msg,
		"meta":    meta,
	}
}

// composeFallbackMessage builds a one-line summary when the caller
// didn't pass a "message" field explicitly. Picks the obvious-looking
// keys (path, remote, sha256-short) so the timeline reads cleanly
// without needing the meta payload.
func composeFallbackMessage(action string, fields map[string]string) string {
	parts := []string{action}
	if p := fields["path"]; p != "" {
		parts = append(parts, p)
	} else if p := fields["target"]; p != "" {
		parts = append(parts, p)
	}
	if r := fields["remote"]; r != "" {
		parts = append(parts, "→ "+r)
	}
	if e := fields["error"]; e != "" {
		parts = append(parts, "(error: "+e+")")
	}
	return strings.Join(parts, " ")
}

// EmitFireAndForget runs Emit synchronously with a short timeout.
//
// The "fire and forget" name is historical — the original
// implementation was async, but mus's short-lived CLI invocations
// killed in-flight goroutines before pika's HTTP request landed, so
// notes were silently dropped. Synchronous with a 3-second cap is
// more reliable: ~10-100ms per emission against a healthy LAN pika
// server, capped at 3s if pika is slow or unreachable.
//
// Net cost for a heavy batch (e.g. `mus rm` over 50 files): ~5s
// worst case on a stuck server, ~5s typical against a real one.
// Compared to the iron checksums each `mus rm` does anyway,
// pika emission is in the noise. The 3s timeout still applies per
// call, so a wedged pika can't lock the user out indefinitely.
func EmitFireAndForget(action string, fields map[string]string) {
	_ = Emit(context.Background(), "", action, fields)
}
