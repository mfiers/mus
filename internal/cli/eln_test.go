package cli

import "testing"

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
