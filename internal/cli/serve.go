package cli

//go:generate echo "ui/index.html embedded via go:embed below"

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
	_ "embed"
	"encoding/hex"
	"encoding/json"
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
	"golang.org/x/term"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/secret"
)

//go:embed ui/index.html
var serveIndexHTML []byte

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

	// Probe iRODS auth once at startup so the UI can surface the
	// authenticated state up-front. Non-fatal — if `iron pwd` fails
	// (missing binary, expired token), the browser gets a yellow
	// banner with the exact `iron auth ...` command to run.
	probeIRODSAuth(ctx, stdout)

	// If iRODS auth was missing AND we have a stored u-number AND
	// stdin is a TTY, try to run `iron auth` now so the depositor
	// doesn't have to open a second terminal. Iron will prompt for
	// password / SSO in this terminal; when it returns, we re-probe
	// so the browser banner clears automatically on next poll.
	autoIronAuthIfConfigured(ctx, stdout)

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

// authState caches the iron auth probe result so the UI can query
// it without shelling out on every page load. Mutated by
// probeIRODSAuth() at startup + refresh; read by apiAuthStatus.
var authState struct {
	iRODSAuthed  bool
	iRODSPwd     string // output of `iron pwd`, when authed
	iRODSHint    string // suggested command to fix (when unauthed)
	iRODSErrText string // short human explanation when unauthed
	elnAuthed    bool
	elnUser      string
	probedAt     time.Time
}

// probeIRODSAuth runs `iron pwd` and, separately, checks whether an
// ELN token is stored. Populates authState. Best-effort — never
// returns an error. Reports outcome on stdout so the terminal
// launcher sees it too.
func probeIRODSAuth(ctx context.Context, stdout interface {
	Write([]byte) (int, error)
}) {
	authState.probedAt = time.Now()

	// iRODS side.
	if _, err := exec.LookPath("iron"); err != nil {
		authState.iRODSAuthed = false
		authState.iRODSErrText = "iron CLI not on PATH — install from https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html"
		if stdout != nil {
			fmt.Fprintln(stdout, "  ⚠ iRODS: iron CLI not found on PATH")
		}
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(probeCtx, "iron", "pwd").Output()
		if err != nil {
			authState.iRODSAuthed = false
			authState.iRODSErrText = "not authenticated against iRODS"
			authState.iRODSHint = IronAuthHint
			if stdout != nil {
				fmt.Fprintln(stdout, "  ⚠ iRODS: not authenticated (upload will fail until you run `"+IronAuthHint+"`)")
			}
		} else {
			authState.iRODSAuthed = true
			authState.iRODSPwd = strings.TrimSpace(string(out))
			authState.iRODSErrText = ""
			authState.iRODSHint = ""
			if stdout != nil {
				fmt.Fprintf(stdout, "  ✓ iRODS: authenticated (iron pwd = %s)\n", authState.iRODSPwd)
			}
		}
	}

	// ELN side — cheap: openELN reads keyring / age; failure means
	// no token or misconfigured URL.
	if client, err := openELN(); err == nil {
		if u, uerr := client.CurrentUser(); uerr == nil {
			authState.elnAuthed = true
			authState.elnUser = u.FullName()
			if stdout != nil {
				fmt.Fprintf(stdout, "  ✓ ELN: authenticated as %s\n", authState.elnUser)
			}
		} else {
			authState.elnAuthed = false
			if stdout != nil {
				fmt.Fprintf(stdout, "  ⚠ ELN: token stored but verification failed: %v\n", uerr)
			}
		}
	} else {
		authState.elnAuthed = false
		if stdout != nil {
			fmt.Fprintln(stdout, "  ⚠ ELN: no token stored — `mus eln login` to enable ELN linking")
		}
	}
	if stdout != nil {
		fmt.Fprintln(stdout)
	}
}

// autoIronAuthIfConfigured tries to run `iron auth <u> gbiomed
// gbiomed.irods.icts.kuleuven.be` at startup when:
//
//   - the earlier probe reported iRODS-unauthed
//   - stdin is a TTY (interactive terminal — iron auth prompts for
//     password / SSO here; on non-TTY launches like Explorer / Finder
//     right-click, there's no way for the prompt to reach the user,
//     so we skip)
//   - a u-number is stored in the secret store as `irods_username`
//     OR in the global mus.cfg cascade
//
// On success (iron auth completes + iron pwd works), the probe is
// re-run and authState updated. On any failure, the banner stays up
// and the depositor is told what to do.
//
// Returns silently when any precondition is missing.
func autoIronAuthIfConfigured(ctx context.Context, stdout interface {
	Write([]byte) (int, error)
}) {
	if authState.iRODSAuthed {
		return
	}
	if !stdinIsATerminal() {
		if stdout != nil {
			fmt.Fprintln(stdout, "  ↷ skipping auto-`iron auth`: stdin is not a TTY (probably launched from a file manager)")
		}
		return
	}
	u := lookupIRODSUsername()
	if u == "" {
		if stdout != nil {
			fmt.Fprintln(stdout, "  ↷ skipping auto-`iron auth`: no irods_username stored (set via `mus secret set irods_username uNNNNNNN` to enable)")
		}
		return
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "  → running: iron auth %s gbiomed gbiomed.irods.icts.kuleuven.be\n", u)
		fmt.Fprintln(stdout, "    (iron will prompt for your KU Leuven password / SSO in this terminal)")
		fmt.Fprintln(stdout)
	}
	// Inherit stdin/stdout/stderr so iron's interactive prompt works.
	cmd := exec.CommandContext(ctx, "iron", "auth", u,
		"gbiomed", "gbiomed.irods.icts.kuleuven.be")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if stdout != nil {
			fmt.Fprintf(stdout, "  ⚠ `iron auth` failed (%v) — banner will stay up until you re-run it manually\n\n", err)
		}
		return
	}
	// Re-probe so authState reflects post-auth reality.
	probeIRODSAuth(ctx, stdout)
}

// lookupIRODSUsername reads the depositor's KU Leuven u-number from:
//   1. secret store key `irods_username`
//   2. global + folder-cascade mus.cfg key `irods_username`
// Returns "" when neither is set.
func lookupIRODSUsername() string {
	if store, err := secret.Open(); err == nil {
		if v, gerr := store.Get("irods_username"); gerr == nil {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	// Cascade fallback — reuses config.Load on cwd so a project
	// folder can pin it via mus.cfg if a lab prefers.
	cwd, err := os.Getwd()
	if err == nil {
		if env, cerr := config.Load(cwd); cerr == nil {
			if v := strings.TrimSpace(env.String("irods_username")); v != "" {
				return v
			}
		}
	}
	return ""
}

// stdinIsATerminal reports whether os.Stdin is a TTY. `iron auth`
// prompts for password / SSO on stdin — auto-run only works when
// there's a real terminal underneath the server process.
func stdinIsATerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
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
		w.Write(serveIndexHTML)
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		// Tiny smoke endpoint — proves the auth + localhost guard
		// path works. Echoes the server's cwd so the UI can show
		// the user where they are.
		cwd, _ := os.Getwd()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s", cwd)
	})
	mux.HandleFunc("/api/auth-status", apiAuthStatus)
	mux.HandleFunc("/api/auth-refresh", apiAuthRefresh)
	registerServeAPI(mux)
	registerServeActions(mux)
}

// apiAuthStatus returns the cached iRODS + ELN auth state from
// authState. Refreshed manually via /api/auth-refresh.
func apiAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"irods_authed":   authState.iRODSAuthed,
		"irods_pwd":      authState.iRODSPwd,
		"irods_hint":     authState.iRODSHint,
		"irods_err_text": authState.iRODSErrText,
		"eln_authed":     authState.elnAuthed,
		"eln_user":       authState.elnUser,
		"probed_at":      authState.probedAt.Format(time.RFC3339),
	})
}

// apiAuthRefresh re-runs the probe. Called by the UI when the
// depositor clicks "check again" after having run `iron auth` in a
// terminal.
func apiAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	probeIRODSAuth(r.Context(), silentWriter{})
	apiAuthStatus(w, r)
}

// silentWriter discards writes — used during /api/auth-refresh where
// we don't want to spam the server's stdout on every browser click.
type silentWriter struct{}

func (silentWriter) Write(p []byte) (int, error) { return len(p), nil }


