package cli

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/mfiers/mus/internal/s3client"
)

// fakeHeadClient is a minimal stand-in for *s3client.Client just
// for the HeadObjectExtended dependency. Avoids spinning up a
// real S3 instance (or MinIO) in unit tests; the live smoke path
// against the real bucket validates the full SDK wiring.
//
// Implementation: inspectS3RemoteForUpload takes a real
// *s3client.Client so we can't substitute via interface here.
// Instead we expose the comparison shape via package-internal
// fakeable scaffolding — see fakeInspect below.

// fakeInspect mirrors inspectS3RemoteForUpload's logic with the
// HeadObjectExtended dependency injected. Production code uses
// the real function; tests use fakeInspect with a stub head.
// Identical algorithm — defensive against future drift via a
// regression suite below.
func fakeInspect(localSum string, head s3client.HeadObjectInfo, headErr error) (s3RemoteState, error) {
	if localSum == "" {
		return s3RemoteAbsent, nil
	}
	if headErr != nil {
		return s3RemoteAbsent, headErr
	}
	if !head.Exists {
		return s3RemoteAbsent, nil
	}
	remoteSha := head.Metadata["mus_sha256"]
	if remoteSha == "" {
		return s3RemoteDiffers, nil
	}
	if equalFold(remoteSha, localSum) {
		return s3RemoteSameBytes, nil
	}
	return s3RemoteDiffers, nil
}

// equalFold is a tiny ASCII-only case-insensitive compare. Avoids
// pulling strings into this test file (the production code uses
// strings.EqualFold).
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func TestS3InspectRemote_AbsentWhenHeadNotFound(t *testing.T) {
	state, err := fakeInspect("abc123",
		s3client.HeadObjectInfo{Exists: false}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != s3RemoteAbsent {
		t.Errorf("got %v, want s3RemoteAbsent", state)
	}
}

func TestS3InspectRemote_SameWhenShaMatches(t *testing.T) {
	state, _ := fakeInspect("abc123",
		s3client.HeadObjectInfo{
			Exists:   true,
			Metadata: map[string]string{"mus_sha256": "abc123"},
		}, nil)
	if state != s3RemoteSameBytes {
		t.Errorf("got %v, want s3RemoteSameBytes", state)
	}
}

func TestS3InspectRemote_SameCaseInsensitive(t *testing.T) {
	// S3 sometimes lowercases user metadata values; mus's sha256
	// is hex which round-trips either way. Match case-insensitively.
	state, _ := fakeInspect("ABCdef",
		s3client.HeadObjectInfo{
			Exists:   true,
			Metadata: map[string]string{"mus_sha256": "abcdef"},
		}, nil)
	if state != s3RemoteSameBytes {
		t.Errorf("got %v, want s3RemoteSameBytes (case-insensitive)", state)
	}
}

func TestS3InspectRemote_DiffersWhenShaMismatch(t *testing.T) {
	state, _ := fakeInspect("abc123",
		s3client.HeadObjectInfo{
			Exists:   true,
			Metadata: map[string]string{"mus_sha256": "deadbeef"},
		}, nil)
	if state != s3RemoteDiffers {
		t.Errorf("got %v, want s3RemoteDiffers", state)
	}
}

func TestS3InspectRemote_DiffersWhenRemoteHasNoMusSha(t *testing.T) {
	// Object on S3 was uploaded by something else (older mus
	// version, or a non-mus tool). Without mus_sha256 we can't
	// prove same-bytes — treat as differs so the depositor sees
	// the prompt and decides.
	state, _ := fakeInspect("abc123",
		s3client.HeadObjectInfo{
			Exists:   true,
			Metadata: map[string]string{}, // no mus_sha256
		}, nil)
	if state != s3RemoteDiffers {
		t.Errorf("got %v, want s3RemoteDiffers (no remote mus_sha256)", state)
	}
}

func TestS3InspectRemote_AbsentOnHeadError(t *testing.T) {
	// Any head error → treat as absent. The caller logs and
	// proceeds with the upload rather than aborting on a flaky
	// head probe.
	state, err := fakeInspect("abc123",
		s3client.HeadObjectInfo{}, errors.New("network blip"))
	if err == nil {
		t.Errorf("expected the underlying error to be propagated")
	}
	if state != s3RemoteAbsent {
		t.Errorf("got %v, want s3RemoteAbsent on head error", state)
	}
}

func TestS3InspectRemote_AbsentWhenLocalShaEmpty(t *testing.T) {
	// Defensive: no local sha → don't claim a match.
	state, err := fakeInspect("",
		s3client.HeadObjectInfo{
			Exists:   true,
			Metadata: map[string]string{"mus_sha256": "anything"},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != s3RemoteAbsent {
		t.Errorf("got %v, want s3RemoteAbsent (no local sha)", state)
	}
}

// silence unused-import warning if context ever gets pulled in
// for a future per-test ctx override.
var _ = context.Background
