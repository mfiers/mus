// Package signing verifies release-binary signatures produced by `pika _sign`.
//
// The signer is the sibling pika project (`pika _sign <file>` → `<file>.sig`);
// we only need to verify here. One shared ed25519 keypair signs releases for
// every Go binary in this maintainer's projects; the public half is embedded
// in PubkeyB64 below. See `/data/1/users/mark/pika/doc/signing.md` for the
// full picture.
package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// PubkeyB64 is the shared ed25519 public key. Identical value lives in pika
// and any other project using this signing scheme — do NOT regenerate
// per-project. Generated 2026-05-22.
const PubkeyB64 = "Oh+qV3lLRso5hkx5aRoWRPgZON8jaMxfiNjR5g6BJMo="

// SigPrefix is the magic-string header on each `.sig` file produced by pika.
// Stripped before base64-decoding the signature bytes.
const SigPrefix = "pika-ed25519-sig-v1\n"

// ErrNoEmbeddedPubkey is returned during the rollout phase when PubkeyB64 is
// blank (template placeholder). Verifies promote to warnings, not failures.
var ErrNoEmbeddedPubkey = errors.New("no signing pubkey embedded in this build")

// Verify checks signatureFile against payload using the embedded pubkey.
func Verify(payload, signatureFile []byte) error {
	if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
		return ErrNoEmbeddedPubkey
	}
	pub, err := base64.StdEncoding.DecodeString(PubkeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("embedded pubkey is malformed (len=%d)", len(pub))
	}
	sigText := strings.TrimSpace(string(signatureFile))
	sigText = strings.TrimPrefix(sigText, strings.TrimSpace(SigPrefix))
	sigText = strings.TrimSpace(sigText)
	sig, err := base64.StdEncoding.DecodeString(sigText)
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes; expected %d",
			len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		return errors.New("signature does not verify against embedded pubkey")
	}
	return nil
}

// VerifyUpgrade fetches the signature at sigURL, reads binaryPath, and
// verifies. During rollout (PubkeyB64 blank) it warns and returns nil;
// once PubkeyB64 is set this is strictly fail-closed.
func VerifyUpgrade(binaryPath, sigURL string, verbose bool) error {
	payload, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("reading downloaded binary: %w", err)
	}
	if sigURL == "" {
		if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
			fmt.Fprintln(os.Stderr,
				"Warning: release has no .sig and this build has no embedded pubkey (rollout phase).")
			return nil
		}
		return errors.New("release has no .sig asset; refusing to upgrade")
	}
	if verbose {
		fmt.Printf("Fetching signature: %s\n", sigURL)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sigURL)
	if err != nil {
		if PubkeyB64 == "" {
			fmt.Fprintf(os.Stderr,
				"Warning: could not fetch signature (%v); accepting in rollout phase.\n", err)
			return nil
		}
		return fmt.Errorf("downloading signature: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if PubkeyB64 == "" {
			fmt.Fprintf(os.Stderr,
				"Warning: signature URL returned HTTP %d; accepting in rollout phase.\n",
				resp.StatusCode)
			return nil
		}
		return fmt.Errorf("signature URL returned HTTP %d", resp.StatusCode)
	}
	sig, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading signature body: %w", err)
	}
	if err := Verify(payload, sig); err != nil {
		if errors.Is(err, ErrNoEmbeddedPubkey) {
			fmt.Fprintln(os.Stderr,
				"Warning: this build has no embedded pubkey; accepting signature unchecked.")
			return nil
		}
		return err
	}
	if verbose {
		fmt.Println("Signature OK")
	}
	return nil
}
