//go:build probe

package anthivecatalog

// Real-world live probe. Tagged `probe` so it doesn't run in the
// normal test suite. Invoke with:
//
//	go test -tags probe -run TestProbe_LiveAnthive4a -v ./internal/anthivecatalog/
//
// Requires the user's mus s3 credentials in the keyring.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"codeberg.org/mfiers/mus/internal/s3client"
	"codeberg.org/mfiers/mus/internal/secret"
)

func TestProbe_LiveAnthive4a(t *testing.T) {
	ctx := context.Background()
	store, err := secret.Open()
	if err != nil {
		t.Skipf("secret.Open failed (S3 creds not set up): %v", err)
	}
	cfg := s3client.Config{}
	for k, f := range map[string]*string{
		"s3_endpoint":   &cfg.Endpoint,
		"s3_region":     &cfg.Region,
		"s3_bucket":     &cfg.Bucket,
		"s3_access_key": &cfg.AccessKey,
		"s3_secret_key": &cfg.SecretKey,
	} {
		v, err := store.Get(k)
		if err != nil {
			t.Skipf("missing secret %q: %v", k, err)
		}
		*f = v
	}
	client, err := s3client.New(ctx, cfg)
	if err != nil {
		t.Fatalf("s3client.New: %v", err)
	}
	mf, err := Load(ctx, LoadOptions{
		Fetcher: client, Bucket: cfg.Bucket,
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Logf("version=%d updated=%s data_projects=%d",
		mf.Version, mf.UpdatedAt, len(mf.DataProjects))

	// The S26 / Sun2026 bug — confirm it resolves to ResolveIsGroupID.
	r := mf.ResolveDatasetID("Sun2026", "S26")
	t.Logf("\nResolve Sun2026/S26:\n  status=%d (want ResolveIsGroupID=%d)\n  group_id=%s\n  available_in_proj=%v",
		r.Status, ResolveIsGroupID, r.Hint.GroupID, r.Hint.AvailableInProj)
	if r.Status != ResolveIsGroupID {
		t.Errorf("S26 should classify as ResolveIsGroupID, got %v", r.Status)
	}
	if r.Hint.GroupID != "S26" {
		t.Errorf("group_id hint: got %q, want S26", r.Hint.GroupID)
	}

	// Real file_id — should match.
	r2 := mf.ResolveDatasetID("Sun2026", "AQR")
	b2, _ := json.MarshalIndent(r2, "", "  ")
	fmt.Println("Resolve Sun2026/AQR:")
	fmt.Println(string(b2))
	if r2.Status != ResolveOK {
		t.Errorf("AQR should resolve cleanly, got %v", r2.Status)
	}

	// Brand-new data_project case.
	r3 := mf.ResolveDatasetID("ThisProjectDoesNotExist2099", "XYZ")
	if r3.Status != ResolveDataProjectNew {
		t.Errorf("missing data_project should be ResolveDataProjectNew, got %v", r3.Status)
	}
}
