package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/eln"
)

func TestBuildProvenanceDict_MangoPURLAlias(t *testing.T) {
	// mango_purl is an alias for purl — same value, different key.
	// Tenants that named the field mango_purl on their sample type
	// (the BADS convention) get it filled without needing to rename.
	prov := sampleProvenance{purl: "https://mango.kuleuven.be/PID/gbiomed/12345/"}
	d := buildProvenanceDict(prov)
	if d["purl"] != prov.purl {
		t.Errorf("purl: got %q, want %q", d["purl"], prov.purl)
	}
	if d["mango_purl"] != prov.purl {
		t.Errorf("mango_purl alias: got %q, want %q", d["mango_purl"], prov.purl)
	}
	// Empty PURL → both keys empty; metasForType then drops them
	// from the outbound POST /samples payload.
	dEmpty := buildProvenanceDict(sampleProvenance{})
	if dEmpty["purl"] != "" || dEmpty["mango_purl"] != "" {
		t.Errorf("empty PURL should propagate as empty on both keys: %+v", dEmpty)
	}
}

func TestBuildProvenanceDict_FilledFields(t *testing.T) {
	upload := time.Date(2026, 7, 3, 12, 30, 0, 0, time.UTC)
	mtime := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	prov := sampleProvenance{
		path:           "irods:///vsc-hard-mounts/leuven-user/foo.tsv",
		uploadFromPath: "/data/1/users/mark/mus/foo.tsv",
		sha256:         "deadbeef",
		sizeBytes:      12345,
		mtime:          mtime,
		uploadTime:     upload,
		purl:           "https://purl.example/foo.tsv",
		kind:           "file",
	}
	d := buildProvenanceDict(prov)

	wantExact := map[string]string{
		"path":               "irods:///vsc-hard-mounts/leuven-user/foo.tsv",
		"upload_from_path":   "/data/1/users/mark/mus/foo.tsv",
		"sha256sum":          "deadbeef",
		"sha256":             "deadbeef",
		"size_bytes":         "12345",
		"size":               "12345",
		"mtime":              "2026-06-01T09:00:00Z",
		"upload_data":        "2026-07-03T12:30:00Z",
		"upload_date":        "2026-07-03T12:30:00Z",
		"purl":               "https://purl.example/foo.tsv",
		"kind":               "file",
	}
	for k, want := range wantExact {
		if got := d[k]; got != want {
			t.Errorf("%s: got %q, want %q", k, got, want)
		}
	}
	// User + host are environment-dependent — just check non-empty.
	if d["upload_user"] == "" {
		t.Error("upload_user should be populated from the current user")
	}
	if d["upload_from_server"] == "" {
		t.Error("upload_from_server should be populated from os.Hostname()")
	}
	if d["mus_version"] == "" {
		t.Error("mus_version should carry cli.Version (even for dev builds it's non-empty)")
	}
}

func TestBuildProvenanceDict_DefaultsAndZeros(t *testing.T) {
	// Zero-valued struct — kind defaults to "file", uploadTime to
	// time.Now(), mtime stays empty.
	d := buildProvenanceDict(sampleProvenance{})
	if d["kind"] != "file" {
		t.Errorf("kind should default to file; got %q", d["kind"])
	}
	if d["mtime"] != "" {
		t.Errorf("mtime should be empty when unset; got %q", d["mtime"])
	}
	if d["upload_data"] == "" {
		t.Error("upload_data should be populated from time.Now() when unset")
	}
	// size_bytes is "0" (not empty) — the caller may or may not
	// want that; metasForType filters empty, so a "0" would leak
	// through.  That's fine — a real caller always sets size.
	if d["size_bytes"] != "0" {
		t.Errorf("size_bytes: got %q, want 0", d["size_bytes"])
	}
}

func TestMetasForType_IntersectAndSkipEmpty(t *testing.T) {
	prov := map[string]string{
		"path":         "/some/path",
		"sha256sum":    "deadbeef",
		"size_bytes":   "12345",
		"mtime":        "",   // empty — should be skipped
		"upload_user":  "mark",
		"mus_version":  "0.2.42",
	}
	typeMetas := []eln.SampleTypeMeta{
		{SampleTypeMetaID: 1, Key: "path", SampleDataType: "TEXT"},
		{SampleTypeMetaID: 2, Key: "sha256sum", SampleDataType: "TEXT"},
		{SampleTypeMetaID: 3, Key: "mtime", SampleDataType: "DATETIME"},   // empty in prov → skip
		{SampleTypeMetaID: 4, Key: "unknown_field", SampleDataType: "TEXT"}, // not in prov → skip
		{SampleTypeMetaID: 5, Key: "mus_version", SampleDataType: "TEXT"},
	}
	got := metasForType(prov, typeMetas)
	byKey := map[string]eln.SampleMetaNew{}
	for _, m := range got {
		byKey[m.Key] = m
	}
	for _, k := range []string{"path", "sha256sum", "mus_version"} {
		m, ok := byKey[k]
		if !ok {
			t.Errorf("expected key %q in metas; got %+v", k, got)
			continue
		}
		if m.Value != prov[k] {
			t.Errorf("%s: value %q, want %q", k, m.Value, prov[k])
		}
	}
	for _, k := range []string{"mtime", "unknown_field"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("%s should have been skipped; got %+v", k, byKey[k])
		}
	}
}

func TestMetasForType_CaseInsensitiveKey(t *testing.T) {
	// Tenants type field names by hand — we must match "Path" and
	// "path" and "PATH" the same way.
	prov := map[string]string{"path": "/foo"}
	typeMetas := []eln.SampleTypeMeta{
		{SampleTypeMetaID: 1, Key: "Path", SampleDataType: "TEXT"},
	}
	got := metasForType(prov, typeMetas)
	if len(got) != 1 || got[0].Value != "/foo" {
		t.Errorf("case-insensitive lookup failed: %+v", got)
	}
	// The Key we send back is the tenant's exact spelling — POST
	// /samples validates against the type's declared key.
	if got[0].Key != "Path" {
		t.Errorf("returned key should preserve tenant casing; got %q", got[0].Key)
	}
}

// fakeSecretStore is a minimal in-memory secret.Store for unit tests.
type fakeSecretStore struct {
	entries map[string]string
}

func (f *fakeSecretStore) Get(name string) (string, error) {
	if v, ok := f.entries[name]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (f *fakeSecretStore) Set(name, value string) error {
	if f.entries == nil {
		f.entries = map[string]string{}
	}
	f.entries[name] = value
	return nil
}
func (f *fakeSecretStore) Delete(name string) error       { delete(f.entries, name); return nil }
func (f *fakeSecretStore) List() ([]string, error)         { return nil, nil }
func (f *fakeSecretStore) Backend() string                 { return "fake" }

// envFromMap builds a config.Env by writing a real mus.cfg into a
// tempdir and calling config.Load. That's the only public path to an
// Env in this package. Overhead per test is ~a few ms.
//
// The tempdir doubles as a sandbox for cascade walk-up — config.Load
// starts at dir and walks toward the filesystem root, so we set
// HOME/XDG_CONFIG_HOME to another empty tempdir to keep the walk
// deterministic (no ambient mus.cfg from the developer's real home).
func envFromMap(t *testing.T, kv map[string]string) *config.Env {
	t.Helper()
	dir := t.TempDir()
	// Isolate cascade walk from the developer's real home.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if len(kv) > 0 {
		var b strings.Builder
		for k, v := range kv {
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(v)
			b.WriteString("\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "mus.cfg"), []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write mus.cfg: %v", err)
		}
	}
	env, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load env: %v", err)
	}
	return env
}

func TestResolveSampleLocationName_CascadePrecedence(t *testing.T) {
	// Cascade beats secret store; nothing set → empty.

	// 1. Cascade wins over secret.
	env := envFromMap(t, map[string]string{"eln_sample_location": "Project Fridge"})
	store := &fakeSecretStore{entries: map[string]string{"eln_sample_location": "User Fridge"}}
	if got := resolveSampleLocationName(env, store, ELNSampleLocationKey); got != "Project Fridge" {
		t.Errorf("cascade precedence: got %q, want Project Fridge", got)
	}

	// 2. No cascade → secret wins.
	empty := envFromMap(t, map[string]string{})
	if got := resolveSampleLocationName(empty, store, ELNSampleLocationKey); got != "User Fridge" {
		t.Errorf("secret precedence: got %q, want User Fridge", got)
	}

	// 3. No cascade + no secret → empty (no placement).
	emptyStore := &fakeSecretStore{}
	if got := resolveSampleLocationName(empty, emptyStore, ELNSampleLocationKey); got != "" {
		t.Errorf("no config: expected empty string (no placement); got %q", got)
	}
}

func TestResolveSampleLocationName_BlankFallsThrough(t *testing.T) {
	// A blank / whitespace-only value at ANY level should fall
	// through to the next level, and if nothing else is set, resolve
	// to the empty string. Mirrors the "no placement" semantic.
	env := envFromMap(t, map[string]string{"eln_sample_location": "   "})
	store := &fakeSecretStore{entries: map[string]string{"eln_sample_location": "\t\n"}}
	if got := resolveSampleLocationName(env, store, ELNSampleLocationKey); got != "" {
		t.Errorf("all-blank config: expected empty; got %q", got)
	}
}

func TestResolveSampleLocationName_NilEnvOrStore(t *testing.T) {
	// Defensive: callers may pass nil for either. Should return empty
	// (not panic).
	if got := resolveSampleLocationName(nil, nil, ELNSampleLocationKey); got != "" {
		t.Errorf("nil env + nil store: got %q, want empty", got)
	}
}

func TestResolveSampleLocationName_KeysAreIndependent(t *testing.T) {
	// The two flows (`mus eln sample` and `mus irods upload`) read
	// different keys and do NOT cross-fall-back. This lets a lab
	// route local samples to one storage unit and iRODS samples to
	// another (e.g. drafts freezer vs permanent-archive freezer)
	// without having them share values.
	env := envFromMap(t, map[string]string{"eln_sample_location": "Drafts"})
	store := &fakeSecretStore{entries: map[string]string{"eln_irods_sample_location": "Archive"}}

	if got := resolveSampleLocationName(env, store, ELNSampleLocationKey); got != "Drafts" {
		t.Errorf("local key: got %q, want Drafts", got)
	}
	if got := resolveSampleLocationName(env, store, ELNIRODSSampleLocationKey); got != "Archive" {
		t.Errorf("iRODS key: got %q, want Archive", got)
	}

	// Setting only the local key does NOT leak to the iRODS key.
	envLocalOnly := envFromMap(t, map[string]string{"eln_sample_location": "Drafts"})
	emptyStore := &fakeSecretStore{}
	if got := resolveSampleLocationName(envLocalOnly, emptyStore, ELNIRODSSampleLocationKey); got != "" {
		t.Errorf("iRODS key should not read local key: got %q, want empty", got)
	}
}

func TestMetasForType_EmptyTypeMetas(t *testing.T) {
	// A sample type with no declared meta fields → nil back, not
	// an empty slice or a panic.
	if got := metasForType(map[string]string{"path": "/x"}, nil); got != nil {
		t.Errorf("expected nil for empty typeMetas; got %+v", got)
	}
}

func TestMetasForType_DefaultsToTEXT(t *testing.T) {
	// If the tenant somehow returned a meta with no SampleDataType
	// (shouldn't happen, but be defensive) we default to TEXT
	// rather than sending an empty string that eLabNext rejects.
	prov := map[string]string{"path": "/foo"}
	typeMetas := []eln.SampleTypeMeta{{Key: "path", SampleDataType: ""}}
	got := metasForType(prov, typeMetas)
	if len(got) != 1 || got[0].SampleDataType != "TEXT" {
		t.Errorf("blank SampleDataType should default to TEXT; got %+v", got)
	}
	// Sanity: pass-through works for other types too.
	prov2 := map[string]string{"upload_data": "2026-07-03T12:30:00Z"}
	typeMetas2 := []eln.SampleTypeMeta{{Key: "upload_data", SampleDataType: "DATETIME"}}
	got2 := metasForType(prov2, typeMetas2)
	if len(got2) != 1 || got2[0].SampleDataType != "DATETIME" {
		t.Errorf("DATETIME type should be preserved; got %+v", got2)
	}
	if !strings.Contains(got2[0].Value, "2026-07-03") {
		t.Errorf("DATETIME value: got %q", got2[0].Value)
	}
}
