package cli

// `mus upgrade` mirrors pika's self-update flow:
//   - look up the latest (or named) release on Codeberg
//   - download the asset matching this binary's OS/ARCH into a temp file
//   - fetch the sibling .sig and verify it via internal/signing
//   - chmod +x and atomically swap into place
//
// Reference: /data/1/users/mark/pika/main.go (upgrade subcommand, ~L1054).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/signing"
	"github.com/spf13/cobra"
)

const (
	upgradeRepoOwner = "mfiers"
	upgradeRepoName  = "mus"
	upgradeAPI       = "https://codeberg.org/api/v1"
)

type upgradeRelease struct {
	TagName string                `json:"tag_name"`
	Name    string                `json:"name"`
	Assets  []upgradeReleaseAsset `json:"assets"`
}

type upgradeReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func newUpgradeCmd() *cobra.Command {
	var tag string
	var force bool
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "fetch the latest signed release from Codeberg and replace this binary",
		Long: "Downloads the binary matching this OS/architecture, verifies its ed25519\n" +
			"signature against the embedded pubkey (signing.PubkeyB64), then atomically\n" +
			"replaces the running mus binary.\n\n" +
			"The signing key is shared across this maintainer's projects (pika is the\n" +
			"signer). Unsigned releases or signature failures abort the upgrade.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(tag, force, checkOnly, cmd.Flags().Changed("verbose"))
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "specific release tag to install (e.g. v0.1.0); default: latest")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "reinstall even if already on the target version")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report what's available; do not install")
	return cmd
}

func runUpgrade(tag string, force, checkOnly, verbose bool) error {
	assetName, err := upgradeAssetName()
	if err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "Platform asset: %s\n", assetName)
	}

	rel, err := upgradeFetchRelease(tag)
	if err != nil {
		return fmt.Errorf("fetching release: %w", err)
	}

	fmt.Printf("Current: %s\n", Version)
	fmt.Printf("Target:  %s\n", rel.TagName)

	if !force && versionEqual(Version, rel.TagName) {
		if checkOnly {
			fmt.Println("Already on the requested release.")
		} else {
			fmt.Println("Already on the requested release. Use --force to reinstall.")
		}
		return nil
	}

	if checkOnly {
		fmt.Println("Run 'mus upgrade' to install.")
		return nil
	}

	// find the asset
	var downloadURL, sigURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case assetName:
			downloadURL = a.URL
		case assetName + ".sig":
			sigURL = a.URL
		}
	}
	if downloadURL == "" {
		var names []string
		for _, a := range rel.Assets {
			names = append(names, a.Name)
		}
		return fmt.Errorf("release %s has no asset %q. Available: %s",
			rel.TagName, assetName, strings.Join(names, ", "))
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating self: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolving self: %w", err)
	}
	dir := filepath.Dir(self)

	if info, err := os.Stat(self); err == nil {
		if info.Mode().Perm()&0o200 == 0 {
			return fmt.Errorf("%s is read-only — re-run with sudo or remove it manually", self)
		}
	}

	tmp, err := os.CreateTemp(dir, ".mus-upgrade-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	fmt.Printf("Downloading %s\n", downloadURL)
	if err := upgradeDownload(downloadURL, tmpPath, verbose); err != nil {
		return fmt.Errorf("downloading: %w", err)
	}

	// Signature verification — refuses if the embedded pubkey is set and no
	// .sig was published, mirroring pika's policy.
	if err := signing.VerifyUpgrade(tmpPath, sigURL, verbose); err != nil {
		return fmt.Errorf("signature check failed: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := upgradeReplace(self, tmpPath); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}
	// File renamed away — don't double-remove on defer.
	tmpPath = ""

	fmt.Printf("Installed %s at %s\n", rel.TagName, self)
	return nil
}

func upgradeAssetName() (string, error) {
	// Names must match what `make build-all` produces in dist/.
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "mus-linux-amd64", nil
	case "linux/arm64":
		return "mus-linux-arm64", nil
	case "darwin/arm64":
		return "mus-darwin-arm64", nil
	default:
		return "", fmt.Errorf("no published binary for %s/%s — build from source",
			runtime.GOOS, runtime.GOARCH)
	}
}

func upgradeFetchRelease(tag string) (*upgradeRelease, error) {
	var url string
	if tag == "" {
		url = fmt.Sprintf("%s/repos/%s/%s/releases/latest",
			upgradeAPI, upgradeRepoOwner, upgradeRepoName)
	} else {
		url = fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
			upgradeAPI, upgradeRepoOwner, upgradeRepoName, tag)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var rel upgradeRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release response missing tag_name")
	}
	return &rel, nil
}

func upgradeDownload(url, dest string, verbose bool) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d while downloading %s", resp.StatusCode, url)
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "Downloaded %d bytes to %s\n", n, dest)
	}
	if n < 1024 {
		return fmt.Errorf("downloaded file is suspiciously small (%d bytes)", n)
	}
	return nil
}

// upgradeReplace swaps the new binary into place. On Linux/macOS, os.Rename
// works fine even with the binary running — the kernel keeps the old inode
// mapped for the current process. (Windows isn't a supported target for mus.)
func upgradeReplace(current, newPath string) error {
	return os.Rename(newPath, current)
}

func versionEqual(current, target string) bool {
	// Strip the leading 'v' the Makefile uses for tags. The compiled-in
	// Version is set by ldflags to whatever VERSION holds at build time —
	// could be "0.1.0" or "v0.1.0" depending on how `make release` was run.
	c := strings.TrimPrefix(current, "v")
	t := strings.TrimPrefix(target, "v")
	return c == t
}
