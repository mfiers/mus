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

// Put uploads a local file/directory to an iRODS collection. Verify means
// IRON should re-checksum after upload.
func (c *Client) Put(ctx context.Context, local, remote string, opts PutOpts) error {
	args := []string{"put"}
	if opts.Recursive {
		args = append(args, "--recursive")
	}
	if opts.Force {
		args = append(args, "--force")
	}
	if opts.Verify {
		args = append(args, "--verify")
	}
	args = append(args, local, remote)
	return c.Stream(ctx, args...)
}

// PutOpts toggles flags for Put.
type PutOpts struct {
	Recursive bool
	Force     bool
	Verify    bool
}

// Get downloads remote to local.
func (c *Client) Get(ctx context.Context, remote, local string, force bool) error {
	args := []string{"get"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remote, local)
	return c.Stream(ctx, args...)
}

// Checksum returns IRON's reported sha256 (or other server-side digest) for
// `remote`. The exact field surfaced depends on the IRON version; callers
// should compare to local sha256 with care.
//
// Implementation: `iron checksum --algorithm sha256 <remote>` is invoked; if
// the binary uses different flags the call will fail and the caller should
// fall back to `ils -L` parsing or skip remote verification.
func (c *Client) Checksum(ctx context.Context, remote string) (string, error) {
	if c.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.DefaultTimeout)
		defer cancel()
	}
	out, err := c.Run(ctx, "checksum", "--algorithm", "sha256", remote)
	if err != nil {
		return "", err
	}
	// Best-effort parse: take the last whitespace-separated token of the last
	// non-empty line. This keeps us robust across minor IRON formatting
	// changes.
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
