package envformat

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseBasic(t *testing.T) {
	raw := []byte(`
# top-of-file comment
key=value
another_key = with spaces in value =equals too

# blank line above is OK
empty_value=
tag = alpha, beta, gamma
`)
	opts := Options{ListKeys: map[string]bool{"tag": true}}
	got, err := Parse(raw, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got["key"] != "value" {
		t.Errorf("key = %q", got["key"])
	}
	if got["another_key"] != "with spaces in value =equals too" {
		t.Errorf("another_key = %q", got["another_key"])
	}
	if got["empty_value"] != "" {
		t.Errorf("empty_value = %q", got["empty_value"])
	}
	tags, _ := got["tag"].([]string)
	sort.Strings(tags)
	if !reflect.DeepEqual(tags, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("tag = %v", tags)
	}
}

func TestParseRejectsBadLines(t *testing.T) {
	cases := []string{
		"no equals here",
		"=value-with-no-key",
	}
	for _, in := range cases {
		if _, err := Parse([]byte(in), Options{}); err == nil {
			t.Errorf("Parse(%q) accepted invalid input", in)
		}
	}
}

func TestListRemovalViaResolve(t *testing.T) {
	opts := Options{ListKeys: map[string]bool{"tag": true}}
	got, err := Parse([]byte("tag=alpha,beta,-alpha,gamma"), opts)
	if err != nil {
		t.Fatal(err)
	}
	// Parse preserves -prefix items in source order
	if !reflect.DeepEqual(got["tag"], []string{"alpha", "beta", "-alpha", "gamma"}) {
		t.Errorf("Parse raw = %v", got["tag"])
	}
	Resolve(got, opts)
	tags, _ := got["tag"].([]string)
	sort.Strings(tags)
	if !reflect.DeepEqual(tags, []string{"beta", "gamma"}) {
		t.Errorf("Resolve tag = %v", tags)
	}
}

func TestCascadeRemoval(t *testing.T) {
	// Models the actual cascade: shallow file then deep file with -prefix.
	opts := Options{ListKeys: map[string]bool{"tag": true}}
	dst := map[string]any{}

	shallow, _ := Parse([]byte("tag=lab,shared"), opts)
	MergeInto(dst, shallow, opts)

	deep, _ := Parse([]byte("tag=mine,-lab"), opts)
	MergeInto(dst, deep, opts)

	tags, _ := dst["tag"].([]string)
	sort.Strings(tags)
	if !reflect.DeepEqual(tags, []string{"mine", "shared"}) {
		t.Errorf("cascaded tags = %v, want [mine shared]", tags)
	}
}

func TestMergeIntoListSemantics(t *testing.T) {
	opts := Options{ListKeys: map[string]bool{"tag": true}}
	dst := map[string]any{
		"tag": []string{"lab", "shared"},
		"foo": "from-shallow",
	}
	src, _ := Parse([]byte("tag=mine,-lab\nfoo=from-deep\n"), opts)
	MergeInto(dst, src, opts)
	tags, _ := dst["tag"].([]string)
	sort.Strings(tags)
	if !reflect.DeepEqual(tags, []string{"mine", "shared"}) {
		t.Errorf("merged tag = %v, want [mine shared]", tags)
	}
	if dst["foo"] != "from-deep" {
		t.Errorf("merged foo = %q", dst["foo"])
	}
}

func TestMarshalRoundtrip(t *testing.T) {
	opts := Options{ListKeys: map[string]bool{"tags": true}}
	in := map[string]any{
		"sha256":            "abc123",
		"size":              "1024",
		"eln_experiment_id": "12345",
		"tags":              []string{"raw", "wt"},
	}
	raw, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Parse(raw, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out["sha256"] != "abc123" || out["eln_experiment_id"] != "12345" {
		t.Errorf("scalar roundtrip failed: %+v", out)
	}
	tags, _ := out["tags"].([]string)
	if !reflect.DeepEqual(tags, []string{"raw", "wt"}) {
		t.Errorf("tags = %v", tags)
	}
}

func TestMarshalRejectsBadValues(t *testing.T) {
	if _, err := Marshal(map[string]any{"k": "v\nwith newline"}); err == nil {
		t.Errorf("Marshal accepted scalar with newline")
	}
	if _, err := Marshal(map[string]any{"k=bad": "v"}); err == nil {
		t.Errorf("Marshal accepted key with =")
	}
	if _, err := Marshal(map[string]any{"k": []string{"good", "bad,comma"}}); err == nil {
		t.Errorf("Marshal accepted list item with ,")
	}
	if _, err := Marshal(map[string]any{"k": 123}); err == nil {
		t.Errorf("Marshal accepted non-string non-list value")
	}
}

func TestMarshalDeterministic(t *testing.T) {
	in := map[string]any{
		"z_last":  "z",
		"a_first": "a",
		"m_mid":   "m",
	}
	out1, _ := Marshal(in)
	out2, _ := Marshal(in)
	if string(out1) != string(out2) {
		t.Errorf("non-deterministic output")
	}
	// First line should start with a_first
	if string(out1)[:7] != "a_first" {
		t.Errorf("keys not sorted: %q", string(out1)[:7])
	}
}
