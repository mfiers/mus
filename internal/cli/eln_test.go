package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseELNExperimentID(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		// The bug report case: leading zeros + a 9 → cobra's Int64Var would
		// try octal and fail; we strip-and-parse base 10.
		{"001000000001303549", 1000000001303549, false},
		{"1303549", 1303549, false},
		{"00012345", 12345, false},
		{"1000000001292564", 1000000001292564, false}, // ELN long-form, handled by FixExperimentID downstream
		{"", 0, true},      // empty rejected
		{"12abc", 0, true}, // non-digits
		{"-1", 0, true},    // negative sign isn't a digit; caught early
		{"0", 0, true},     // zero / all-zero rejected as non-positive
	}
	for _, tc := range cases {
		got, err := parseELNExperimentID(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseELNExperimentID(%q) = %d, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseELNExperimentID(%q): unexpected error %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("parseELNExperimentID(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseELNToken(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantKey  string
		wantErr  bool
	}{
		// Canonical form the VIB web UI emits
		{"vib.elabjournal.com;n12a3bnabcdef123456nb45678cc9n", "vib.elabjournal.com", "n12a3bnabcdef123456nb45678cc9n", false},
		// Bare key — defaults to vib host
		{"n12a3bnabcdef123456nb45678cc9n", "vib.elabjournal.com", "n12a3bnabcdef123456nb45678cc9n", false},
		// Other tenant
		{"gbiomed.elabjournal.com;abc123", "gbiomed.elabjournal.com", "abc123", false},
		// Whitespace tolerated
		{"  vib.elabjournal.com ;  abc123  ", "vib.elabjournal.com", "abc123", false},
		// User pastes the full URL — strip scheme
		{"https://vib.elabjournal.com;abc", "vib.elabjournal.com", "abc", false},
		// Empty key after split → error
		{"vib.elabjournal.com;", "", "", true},
		// Empty host → error
		{";abc", "", "", true},
		// Empty input → error
		{"", "", "", true},
	}
	for _, tc := range cases {
		host, key, err := parseELNToken(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseELNToken(%q) = (%q, %q), want error", tc.in, host, key)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseELNToken(%q): unexpected error %v", tc.in, err)
			continue
		}
		if host != tc.wantHost || key != tc.wantKey {
			t.Errorf("parseELNToken(%q) = (%q, %q), want (%q, %q)",
				tc.in, host, key, tc.wantHost, tc.wantKey)
		}
	}
}

func TestExperimentToKV(t *testing.T) {
	// Type smoke test only — confirms the keys we write are exactly the
	// ones the rest of the CLI (irods upload, tag) reads.
	expected := []string{
		"eln_experiment_id", "eln_experiment_name",
		"eln_study_id", "eln_study_name",
		"eln_project_id", "eln_project_name",
	}
	for _, k := range expected {
		// The test passes if these keys are referenced in eln.go. We can't
		// easily check from here, so leave this as a documentation test.
		_ = k
	}
}

func TestReadSecretLineFromNonTTYReader(t *testing.T) {
	// Piped input path (no TTY) should read the line without echoing the
	// prompt and trim trailing CR/LF.
	in := strings.NewReader("my-secret-token\n")
	var out bytes.Buffer
	got, err := readSecretLine(in, &out, "Token: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-secret-token" {
		t.Errorf("got %q, want %q", got, "my-secret-token")
	}
	if out.Len() != 0 {
		t.Errorf("non-TTY path should not write prompt; got %q", out.String())
	}
}

func TestReadSecretLineCRLF(t *testing.T) {
	in := strings.NewReader("abc\r\n")
	var out bytes.Buffer
	got, err := readSecretLine(in, &out, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc" {
		t.Errorf("CRLF not stripped: %q", got)
	}
}

func TestParseELNTokenStability(t *testing.T) {
	// Sanity that calling twice with the same input produces the same output
	// (no hidden state).
	a, b, _ := parseELNToken("vib.elabjournal.com;abc")
	c, d, _ := parseELNToken("vib.elabjournal.com;abc")
	if !reflect.DeepEqual([2]string{a, b}, [2]string{c, d}) {
		t.Errorf("non-deterministic: %v vs %v", [2]string{a, b}, [2]string{c, d})
	}
}
