// Package iron shells out to the KU Leuven `iron` CLI for iRODS interactions.
// We deliberately do NOT use a native iRODS client; IRON owns auth, sessions,
// SSL and resume logic.
//
// docs: https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html
package iron

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNotFound indicates the `iron` binary is not available on PATH.
var ErrNotFound = errors.New("iron CLI not found on PATH — install from https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html")

// Client is a thin process-spawning wrapper.
type Client struct {
	// Bin overrides the binary name (default: "iron").
	Bin string
	// ExtraArgs is prefixed before every subcommand (e.g. global flags).
	ExtraArgs []string
	// DefaultTimeout for short commands (ls, checksum). 0 = no timeout.
	DefaultTimeout time.Duration
}

// New returns a Client. If `iron` is not on PATH, it returns ErrNotFound.
func New() (*Client, error) {
	c := &Client{Bin: "iron", DefaultTimeout: 60 * time.Second}
	if err := c.Probe(); err != nil {
		return nil, err
	}
	return c, nil
}

// Probe verifies the binary is available.
func (c *Client) Probe() error {
	if _, err := exec.LookPath(c.binary()); err != nil {
		return ErrNotFound
	}
	return nil
}

func (c *Client) binary() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "iron"
}

// Run executes `iron <args...>` and returns stdout. stderr is captured into
// the returned error on failure.
func (c *Client) Run(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	full := append(append([]string{}, c.ExtraArgs...), args...)
	cmd := exec.CommandContext(ctx, c.binary(), full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "IRON_NO_INTERACTIVE=1")
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("iron %s: %s", strings.Join(full, " "), msg)
	}
	return stdout.String(), nil
}

// Stream executes `iron <args...>` connecting stdout/stderr directly to the
// process. Use for long uploads/downloads where the user benefits from live
// progress bars.
func (c *Client) Stream(ctx context.Context, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	full := append(append([]string{}, c.ExtraArgs...), args...)
	cmd := exec.CommandContext(ctx, c.binary(), full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("iron %s: %w", strings.Join(full, " "), err)
	}
	return nil
}

// Upload uploads a local file/directory to an iRODS collection. IRON is
// idempotent by default: files with matching size+modtime are skipped, others
// are overwritten. Pass Exclusive to refuse-overwrite. VerifyChecksum re-
// hashes both ends to confirm transfer integrity.
//
// IRON auto-detects directory vs file source and handles recursion natively;
// no Recursive flag is needed (or supported).
func (c *Client) Upload(ctx context.Context, local, remote string, opts UploadOpts) error {
	args := []string{"upload"}
	if opts.VerifyChecksum {
		args = append(args, "--verify-checksum")
	}
	if opts.Exclusive {
		args = append(args, "--exclusive")
	}
	if opts.Newer {
		args = append(args, "--newer")
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, local, remote)
	return c.Stream(ctx, args...)
}

// UploadOpts toggles flags for Upload. See `iron upload --help` for full
// semantics — these wrap the most commonly useful flags.
type UploadOpts struct {
	VerifyChecksum bool // re-hash both sides after transfer
	Exclusive      bool // refuse to overwrite existing remote objects
	Newer          bool // only upload files newer than remote
	DryRun         bool // print actions, do not perform
}

// Download downloads remote to local. Like Upload, IRON handles
// file-vs-collection automatically.
func (c *Client) Download(ctx context.Context, remote, local string, opts DownloadOpts) error {
	args := []string{"download"}
	if opts.VerifyChecksum {
		args = append(args, "--verify-checksum")
	}
	if opts.Exclusive {
		args = append(args, "--exclusive")
	}
	if opts.Newer {
		args = append(args, "--newer")
	}
	args = append(args, remote, local)
	return c.Stream(ctx, args...)
}

// DownloadOpts toggles flags for Download.
type DownloadOpts struct {
	VerifyChecksum bool
	Exclusive      bool
	Newer          bool
}

// Checksum returns the server-side checksum for `remote` as reported by
// `iron checksum <remote>`. IRON does not accept algorithm flags; the digest
// shape is determined by what was stored on upload. Callers should compare
// case-insensitively (Mango sometimes stores sha2-base64 prefixed).
func (c *Client) Checksum(ctx context.Context, remote string) (string, error) {
	if c.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.DefaultTimeout)
		defer cancel()
	}
	out, err := c.Run(ctx, "checksum", remote)
	if err != nil {
		return "", err
	}
	// Best-effort parse: take the last whitespace-separated token of the last
	// non-empty line. Robust across minor IRON formatting changes.
	for _, line := range reverse(strings.Split(strings.TrimSpace(out), "\n")) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		return fields[len(fields)-1], nil
	}
	return "", fmt.Errorf("could not parse checksum output: %q", out)
}

// TreeJSON runs `iron tree --json <collection>` and returns the raw JSON for
// the caller to decode. The schema follows IRON's tree output: a nested
// object with name + children + columns (size, checksum, date, status, ...).
// Use the `cols` parameter to request specific columns (default is name only).
//
// Useful for inventory scans / mus-irods-scan-style workflows.
func (c *Client) TreeJSON(ctx context.Context, remote string, cols ...string) (string, error) {
	args := []string{"tree", "--json"}
	if len(cols) > 0 {
		args = append(args, "--columns", strings.Join(cols, ","))
	}
	args = append(args, remote)
	return c.Run(ctx, args...)
}

// FindJSON runs `iron find <collection>` with a glob and JSON output.
func (c *Client) FindJSON(ctx context.Context, remote string, cols ...string) (string, error) {
	args := []string{"find", "--json"}
	if len(cols) > 0 {
		args = append(args, "--columns", strings.Join(cols, ","))
	}
	args = append(args, remote)
	return c.Run(ctx, args...)
}

// ListJSON runs `iron ls --json <collection>`.
func (c *Client) ListJSON(ctx context.Context, remote string, cols ...string) (string, error) {
	args := []string{"ls", "--json"}
	if len(cols) > 0 {
		args = append(args, "--columns", strings.Join(cols, ","))
	}
	args = append(args, remote)
	return c.Run(ctx, args...)
}

// Exists reports whether the remote object or collection exists.
func (c *Client) Exists(ctx context.Context, remote string) (bool, error) {
	if c.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.DefaultTimeout)
		defer cancel()
	}
	_, err := c.Run(ctx, "ls", remote)
	if err == nil {
		return true, nil
	}
	// IRON returns nonzero with a "not found" message; treat any failure as
	// "not exists" but pass through context errors.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}
	return false, nil
}

// Mkdir creates a collection (recursive).
func (c *Client) Mkdir(ctx context.Context, remote string) error {
	if c.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.DefaultTimeout)
		defer cancel()
	}
	_, err := c.Run(ctx, "mkdir", "--parents", remote)
	return err
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}
