package cli

// Unit tests for the AVU-building and diffing logic in irods_meta.go.
// We test the pure helpers (avuFromDoc + the set-equality and dedupe
// machinery) directly. applyMetadataToIRODS shells out to iron and is
// covered by the integration suite, not this file.

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/sidecar"
)

func TestAVUFromDoc_EmptyDocIsEmpty(t *testing.T) {
	got := avuFromDoc(&sidecar.Doc{})
	if len(got) != 0 {
		t.Errorf("want empty AVU set, got %v", got)
	}
}

func TestAVUFromDoc_ScalarsAndLists(t *testing.T) {
	doc := &sidecar.Doc{
		DataProject: "Fiers2025",
		File:        sidecar.FileInfo{Sha256: "abc123"},
		Bio: &sidecar.Bio{
			Organism:       "Homo sapiens",
			Technology:     "10x_chromium_v3",
			SuspensionType: "nucleus",
			Sex:            "male",
			Year:           2025,
			Tissue:         []string{"frontal cortex", "hippocampus"},
			DOI:            []string{"10.1/a", "10.1/b"},
		},
	}
	got := avuFromDoc(doc)

	expect := map[string][]string{
		"mus_data_project":    {"Fiers2025"},
		"mus_sha256":          {"abc123"},
		"mus_organism":        {"Homo sapiens"},
		"mus_technology":      {"10x_chromium_v3"},
		"mus_suspension_type": {"nucleus"},
		"mus_sex":             {"male"},
		"mus_year":            {"2025"},
		"mus_tissue":          {"frontal cortex", "hippocampus"},
		"mus_doi":             {"10.1/a", "10.1/b"},
	}
	for key, want := range expect {
		gotVal, ok := got[key]
		if !ok {
			t.Errorf("missing key %q in AVU set; got %v", key, got)
			continue
		}
		if !reflect.DeepEqual(gotVal, want) {
			t.Errorf("key %q: got %v, want %v", key, gotVal, want)
		}
	}
}

func TestAVUFromDoc_OmitsEmptyValues(t *testing.T) {
	doc := &sidecar.Doc{
		Bio: &sidecar.Bio{
			Organism: "", // empty scalar
			Tissue:   []string{"", "frontal cortex", ""},
			Year:     0, // zero year omitted
		},
	}
	got := avuFromDoc(doc)
	if _, ok := got["mus_organism"]; ok {
		t.Error("empty Organism should not produce an AVU triplet")
	}
	if _, ok := got["mus_year"]; ok {
		t.Error("Year=0 should not produce an AVU triplet")
	}
	tis, ok := got["mus_tissue"]
	if !ok {
		t.Fatal("Tissue list should produce an AVU set after empty filter")
	}
	if !reflect.DeepEqual(tis, []string{"frontal cortex"}) {
		t.Errorf("empty-string entries should be filtered: got %v", tis)
	}
}

func TestAVUFromDoc_H5adShapeAndInspection(t *testing.T) {
	when := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	doc := &sidecar.Doc{
		H5ad: &sidecar.H5ad{
			NObs:                  12345,
			NVars:                 30000,
			Layers:                []string{"counts", "logcounts"},
			Embedding:             "X_umap",
			InspectedAt:           when,
			InspectedSha256:       "deadbeef",
			InspectedVocabSha256:  "cafe",
			InspectedCheckVersion: 3,
		},
	}
	got := avuFromDoc(doc)
	if got["mus_h5ad_n_obs"][0] != "12345" {
		t.Errorf("n_obs: %v", got["mus_h5ad_n_obs"])
	}
	if got["mus_h5ad_n_vars"][0] != "30000" {
		t.Errorf("n_vars: %v", got["mus_h5ad_n_vars"])
	}
	if !reflect.DeepEqual(got["mus_h5ad_layers"], []string{"counts", "logcounts"}) {
		t.Errorf("layers: %v", got["mus_h5ad_layers"])
	}
	if got["mus_h5ad_inspected_at"][0] != "2026-05-28T12:00:00Z" {
		t.Errorf("inspected_at: %v", got["mus_h5ad_inspected_at"])
	}
	if got["mus_h5ad_inspected_check_version"][0] != "3" {
		t.Errorf("check_version: %v", got["mus_h5ad_inspected_check_version"])
	}
}

func TestStringSetsEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both empty", nil, nil, true},
		{"same order", []string{"a", "b"}, []string{"a", "b"}, true},
		{"reordered", []string{"a", "b"}, []string{"b", "a"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different content", []string{"a", "b"}, []string{"a", "c"}, false},
		{"duplicate vs single", []string{"a", "a"}, []string{"a"}, true},
		{"single vs single different", []string{"x"}, []string{"y"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stringSetsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("stringSetsEqual(%v,%v)=%v want %v",
					tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestDedupeSorted(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, nil},
		{[]string{}, nil},
		{[]string{"a"}, []string{"a"}},
		{[]string{"b", "a", "b", "a"}, []string{"a", "b"}},
		{[]string{"a", "a", "a"}, []string{"a"}},
	}
	for _, tc := range cases {
		got := dedupeSorted(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("dedupeSorted(%v)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestJoinShort_TruncatesLongValues(t *testing.T) {
	long := ""
	for i := 0; i < 80; i++ {
		long += "x"
	}
	out := joinShort([]string{long})
	if len(out) > 65 {
		t.Errorf("expected truncation to ~60 chars, got %d: %q", len(out), out)
	}
	if out[len(out)-3:] != "..." {
		t.Errorf("expected ellipsis suffix, got %q", out)
	}
}

func TestJoinShort_JoinsList(t *testing.T) {
	if got := joinShort([]string{"a", "b", "c"}); got != "a,b,c" {
		t.Errorf("got %q want a,b,c", got)
	}
	if got := joinShort(nil); got != "(none)" {
		t.Errorf("empty list rendering: %q", got)
	}
}

// TestAVUFromDoc_KeysAreNamespaced is a guard against accidentally
// emitting a triplet whose attribute name doesn't carry mus's prefix.
// AVU naming conventions matter on iRODS — other tools (mango_mdschema
// in particular) own different prefixes and we mustn't collide.
func TestAVUFromDoc_KeysAreNamespaced(t *testing.T) {
	doc := &sidecar.Doc{
		DataProject: "Fiers2025",
		File:        sidecar.FileInfo{Sha256: "abc"},
		Bio: &sidecar.Bio{
			Organism: "Homo sapiens",
			Tissue:   []string{"frontal cortex"},
			DOI:      []string{"10.1/a"},
		},
		H5ad: &sidecar.H5ad{NObs: 1},
		ELN:  &sidecar.ELN{ExperimentID: "EX-1"},
	}
	got := avuFromDoc(doc)
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(k) < 4 || k[:4] != "mus_" {
			t.Errorf("AVU key %q does not carry mus_ prefix", k)
		}
	}
}
