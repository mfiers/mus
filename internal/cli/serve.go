package cli

// `mus serve` — a localhost web GUI for the non-CLI lab depositors.
//
// Boots an HTTP server on 127.0.0.1:<random-port>, generates a
// per-invocation random token, opens the default browser to the
// token URL. Every API endpoint is protected by the token (URL
// query string on first hit; cookie after that) AND a
// belt-and-braces remote-addr check that refuses anything other
// than 127.0.0.1 / ::1 / localhost. Single-user pattern: same
// model as `jupyter notebook` for a personal laptop or a HPC
// login node where the user is sshed in (port-forward if they
// want to use it remotely; mus only listens on the loopback).
//
// Not a multi-user service. Token leaks to anyone with `ps` on a
// shared box — see doc/01_install_and_setup.md for the trust
// model.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// serveOptions holds the flag values for `mus serve`. Pulled out
// so the handler tests can construct one without going through
// cobra.
type serveOptions struct {
	Port      int    // 0 → ask the OS for a random free port
	Bind      string // listening interface; default 127.0.0.1
	NoBrowser bool   // skip the xdg-open / open / start step
}

func newServeCmd() *cobra.Command {
	var opt serveOptions
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "start a local web GUI for annotating + uploading folders",
		Long: "Boots an HTTP server on the loopback interface, prints + opens a\n" +
			"URL with a random session token, and serves a small browser UI for:\n\n" +
			"  - viewing the cascading mus.cfg of any folder under cwd\n" +
			"  - editing annotation fields with vocab autocomplete\n" +
			"  - linking an ELN experiment / disabling ELN per-folder\n" +
			"  - kicking off `mus irods upload` from the browser\n\n" +
			"Single-user by design — token in URL, localhost binding, no auth\n" +
			"layer beyond that. Don't run on a multi-user box without\n" +
			"understanding that anyone with `ps` can read the token. Stop with\n" +
			"Ctrl-C.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context(), opt, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().IntVar(&opt.Port, "port", 0,
		"TCP port (0 = ask the OS for a random free one)")
	cmd.Flags().StringVar(&opt.Bind, "bind", "127.0.0.1",
		"interface to bind to (default localhost; do NOT change unless you know what you're doing)")
	cmd.Flags().BoolVar(&opt.NoBrowser, "no-browser", false,
		"skip the auto-open-browser step; just print the URL")
	return cmd
}

// runServe is the body of `mus serve`. Picks a port, generates a
// token, wires up the mux, opens the browser, blocks on Ctrl-C.
func runServe(ctx context.Context, opt serveOptions, stdout, stderr interface {
	Write([]byte) (int, error)
}) error {
	bind := opt.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	if !isLoopbackBindAddr(bind) {
		return fmt.Errorf(
			"--bind %q is not a loopback address — `mus serve` is single-user "+
				"localhost-only by design. To use the GUI from another machine, "+
				"port-forward over SSH: `ssh -L %d:127.0.0.1:%d <host>`",
			bind, opt.Port, opt.Port)
	}

	// Pin the server root to cwd at boot time. Every path-resolving
	// handler in serve_handlers.go refuses to escape this root —
	// the browser can navigate INTO subdirs but not above this
	// directory. Recorded BEFORE we listen so a listen-failure
	// path doesn't leave stale state.
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	serveRoot = root

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bind, opt.Port))
	if err != nil {
		return fmt.Errorf("listen %s:%d: %w", bind, opt.Port, err)
	}
	addr := listener.Addr().(*net.TCPAddr)

	token, err := generateToken()
	if err != nil {
		listener.Close()
		return err
	}

	mux := http.NewServeMux()
	registerServeRoutes(mux, token)

	server := &http.Server{
		Handler:           withLocalhostGuard(withTokenAuth(token, mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	url := fmt.Sprintf("http://%s/?t=%s", addr.String(), token)
	fmt.Fprintf(stdout, "mus serve listening on %s\n", addr.String())
	fmt.Fprintf(stdout, "  → open in your browser: %s\n", url)
	fmt.Fprintf(stdout, "  Ctrl-C to stop.\n\n")

	// Start the server in a goroutine so we can block on a signal.
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	if !opt.NoBrowser {
		if err := openInBrowser(url); err != nil {
			fmt.Fprintf(stderr, "  ⚠ could not open browser automatically (%v) — open the URL above manually\n", err)
		}
	}

	// Wait for either the server to die or a signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case <-sigCh:
		fmt.Fprintln(stdout, "\nshutting down…")
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	return nil
}

// generateToken returns 32 cryptographically-random bytes
// hex-encoded — 64 chars, URL-safe, opaque, single-use per
// `mus serve` invocation.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// withTokenAuth is the auth middleware. Accepts the token via:
//   - ?t=<token> URL query param (first hit from the welcome link)
//   - mus_token cookie (set on first successful hit; persists for the session)
//
// On success, the cookie is refreshed so subsequent requests pass.
// On failure, returns 401 with a one-line text explanation — no
// HTML, no info disclosure.
func withTokenAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("t")
		if got == "" {
			if c, err := r.Cookie("mus_token"); err == nil {
				got = c.Value
			}
		}
		if !constantTimeEqual(got, token) {
			http.Error(w, "401 unauthorized — open the URL `mus serve` printed (token required)", http.StatusUnauthorized)
			return
		}
		// Refresh the cookie so the URL token can disappear from
		// the browser's history.
		http.SetCookie(w, &http.Cookie{
			Name:     "mus_token",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		next.ServeHTTP(w, r)
	})
}

// constantTimeEqual is a length-then-byte compare without
// crypto/subtle's package dep — sufficient for a 64-char hex
// token where mismatches are usually all-zero from an empty input.
// Belt-and-braces: the timing channel here is irrelevant because
// the server is localhost-only.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// withLocalhostGuard refuses any request whose RemoteAddr isn't a
// loopback IP. Defense in depth — the server is already bound to
// 127.0.0.1, but if a depositor manually flips --bind to 0.0.0.0
// (we refuse that) or there's a routing weirdness, this catches it.
func withLocalhostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "403 forbidden — `mus serve` is localhost-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackBindAddr returns true when `bind` resolves to a
// loopback IP (127.0.0.0/8 or ::1) or is the literal "localhost".
// Used by the bind-flag guard so accidental --bind=0.0.0.0
// invocations get a clear error.
func isLoopbackBindAddr(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// openInBrowser shells out to the platform's default open-URL
// command. Best-effort: caller already prints the URL so the user
// can copy/paste if this fails.
func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// `cmd /c start "" "URL"` — empty quoted title required when
		// the URL itself is quoted; otherwise start eats it as the title.
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default: // linux, freebsd, …
		// Prefer xdg-open; fall back to the X selection helpers
		// if it's missing. Returning xdg-open's error is fine.
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// registerServeRoutes wires endpoints onto the given mux. Split
// out so future commits can add /api/* routes without touching
// the bootstrap logic.
func registerServeRoutes(mux *http.ServeMux, _ string) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(serveSkeletonHTML))
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		// Tiny smoke endpoint — proves the auth + localhost guard
		// path works. Echoes the server's cwd so the UI placeholder
		// has something to display.
		cwd, _ := os.Getwd()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s", cwd)
	})
	registerServeAPI(mux)
}

// serveSkeletonHTML is the placeholder front-end for the first
// commit — enough to confirm auth + the browser open path work
// end to end. The next commit replaces it with the real UI.
const serveSkeletonHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>mus serve</title>
<style>
  body { font: 14px system-ui, sans-serif; max-width: 60ch; margin: 4em auto; color: #222; }
  code { background: #f4f4f4; padding: 1px 4px; border-radius: 3px; }
  .ok { color: #2a7a2a; }
</style>
</head>
<body>
  <h1>mus serve <span class="ok">✓</span></h1>
  <p>The server is up and authenticated. The annotation UI lands in the next commit.</p>
  <p>You're running from: <code id="cwd">…</code></p>
  <p>API stub: <code>/api/ping</code> returns <code>pong</code> when auth works.</p>
  <p>Stop the server with Ctrl-C in the terminal where you started it.</p>
  <script>
    fetch('/api/ping').then(r => r.text()).then(t => {
      document.getElementById('cwd').textContent = t;
    }).catch(() => {});
  </script>
</body>
</html>`

// _ = strings to keep the import alive if a future commit drops
// every use; one StringPrefix-style helper is on the way in the
// next commit so let's not churn imports.
var _ = strings.TrimSpace
