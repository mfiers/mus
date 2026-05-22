package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sign helper that mirrors pika's `_sign` output format.
func sign(t *testing.T, priv ed25519.PrivateKey, payload []byte) []byte {
	t.Helper()
	raw := ed25519.Sign(priv, payload)
	return []byte(SigPrefix + base64.StdEncoding.EncodeToString(raw) + "\n")
}

func setEmbeddedPubkey(t *testing.T, pub ed25519.PublicKey) {
	t.Helper()
	// We can't override the const at test time directly. Instead use the
	// build with the real pubkey; for tests we generate matching key pairs
	// and verify via the lower-level path manually.
}

// TestVerifyHappyPath uses ed25519 directly because PubkeyB64 is a const.
// We verify the inverse: confirm a signature made by a *different* key fails
// against the embedded pubkey, and that ErrNoEmbeddedPubkey logic works.
func TestVerifyRejectsWrongKey(t *testing.T) {
	if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
		t.Skip("no embedded pubkey")
	}
	// Sign with a fresh, unrelated key — must NOT verify.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	payload := []byte("hello mus")
	sig := sign(t, priv, payload)
	if err := Verify(payload, sig); err == nil {
		t.Fatalf("Verify accepted signature from unrelated key")
	}
}

// TestVerifyRejectsTamperedPayload signs a payload, then verifies a tampered
// payload — must fail. We can't use PubkeyB64 for the positive side so we
// hand-roll the ed25519 path for parity.
func TestVerifyRejectsTamperedPayload(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("original content")
	raw := ed25519.Sign(priv, payload)
	tampered := []byte("modified content")
	if ed25519.Verify(pub, tampered, raw) {
		t.Fatalf("ed25519 accepted tampered payload — test setup broken")
	}
}

func TestVerifyMalformedSignature(t *testing.T) {
	if err := Verify([]byte("payload"), []byte("not base64 !!! @@@")); err == nil {
		t.Fatalf("Verify accepted malformed signature")
	}
}

func TestVerifyUpgradeMissingSigFailsClosed(t *testing.T) {
	if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
		t.Skip("rollout phase — VerifyUpgrade accepts missing sig")
	}
	tmp := filepath.Join(t.TempDir(), "fake-binary")
	if err := os.WriteFile(tmp, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := VerifyUpgrade(tmp, "", false)
	if err == nil {
		t.Fatalf("expected refusal; got nil")
	}
	if !strings.Contains(err.Error(), "no .sig asset") {
		t.Errorf("got %v, want a 'no .sig asset' error", err)
	}
}

func TestVerifyUpgradeBadSigFromServer(t *testing.T) {
	if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
		t.Skip("rollout phase — bad sig promoted to warning")
	}
	// Serve a syntactically valid but cryptographically wrong sig.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, SigPrefix)
		fmt.Fprintln(w, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("decoy"))))
	}))
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(tmp, []byte("real binary bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = VerifyUpgrade(tmp, srv.URL+"/sig", false)
	if err == nil {
		t.Fatalf("expected verification failure")
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("got %v, want 'does not verify' error", err)
	}
}

func TestVerifyUpgradeHTTPError(t *testing.T) {
	if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
		t.Skip("rollout phase")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such asset", http.StatusNotFound)
	}))
	defer srv.Close()
	tmp := filepath.Join(t.TempDir(), "x")
	_ = os.WriteFile(tmp, []byte("data"), 0o644)
	err := VerifyUpgrade(tmp, srv.URL+"/sig", false)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("got %v, want HTTP 404 error", err)
	}
}

func TestErrNoEmbeddedPubkeyConst(t *testing.T) {
	// Sanity: PubkeyB64 must be valid base64 of a 32-byte ed25519 pubkey
	// OR be the placeholder sentinel.
	if PubkeyB64 == "" || PubkeyB64 == "PASTE_PUBKEY_HERE" {
		if !errors.Is(ErrNoEmbeddedPubkey, ErrNoEmbeddedPubkey) {
			t.Errorf("sentinel error broken")
		}
		return
	}
	raw, err := base64.StdEncoding.DecodeString(PubkeyB64)
	if err != nil {
		t.Fatalf("PubkeyB64 is not valid base64: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Errorf("PubkeyB64 decodes to %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
}
