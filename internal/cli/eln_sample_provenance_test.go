package cli

import (
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/eln"
)

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
