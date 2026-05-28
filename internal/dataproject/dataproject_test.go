package dataproject

import "testing"

func TestValidateNameAccepts(t *testing.T) {
	cases := []string{
		"Fiers2025",
		"Fiers_Mancuso_2025",
		"MusLab_Pilot2024",
		"Van_Der_Berg2024",
		"A2024", // shortest valid
	}
	for _, in := range cases {
		if err := ValidateName(in); err != nil {
			t.Errorf("ValidateName(%q) rejected valid name: %v", in, err)
		}
	}
}

func TestValidateNameRejects(t *testing.T) {
	cases := []string{
		"",                // empty
		"   ",             // whitespace only
		"fiers2025",       // lowercase first letter
		"Fiers-2025",      // hyphen
		"Fiers 2025",      // space
		"Fiers25",         // 2-digit year
		"2025_Fiers",      // wrong ordering — must end in digits, not start with them
		"Fiers2025_extra", // trailing junk after year
		"!Fiers2025",      // bad first char
		"Élanchol2025",    // non-ASCII letters
	}
	for _, in := range cases {
		if err := ValidateName(in); err == nil {
			t.Errorf("ValidateName(%q) accepted invalid name", in)
		}
	}
}

// Edge-case names the regex accepts that look weird to a human but aren't
// clearly malformed — left in to lock the parser's behaviour.
func TestValidateNameAcceptsEdgeCases(t *testing.T) {
	cases := []string{
		"Fiers20250", // 5 trailing digits — regex matches last 4 as year
		"Fiers_2025", // explicit underscore separator before year
	}
	for _, in := range cases {
		if err := ValidateName(in); err != nil {
			t.Errorf("ValidateName(%q) regression — should still accept: %v", in, err)
		}
	}
}

func TestSanitizeForPath(t *testing.T) {
	cases := map[string]string{
		"Fiers2025":                              "Fiers2025",
		"Single-cell pilot — Fiers lab":          "Single_cell_pilot_Fiers_lab",
		"Single-cell RNA-seq of microglia (PRS)": "Single_cell_RNA_seq_of_microglia_PRS",
		"   Lots   of   whitespace   ":           "Lots_of_whitespace",
		"___leading_trailing___":                 "leading_trailing",
		"":                                       "",
		"!@#$%^&*()":                             "", // no alphanumerics → empty
		"a":                                      "a",
	}
	for in, want := range cases {
		if got := SanitizeForPath(in); got != want {
			t.Errorf("SanitizeForPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeForPathTruncates(t *testing.T) {
	// 100-char input, all letters → must truncate to MaxLen=60 and have no
	// trailing underscore.
	in := ""
	for i := 0; i < 100; i++ {
		in += "a"
	}
	out := SanitizeForPath(in)
	if len(out) != MaxLen {
		t.Errorf("len(out) = %d, want %d", len(out), MaxLen)
	}

	// Input that, after truncation, would leave a trailing underscore:
	in = "abc" + "_xyz_" // pad with underscores then trim
	// Better: make an input where MaxLen would land on an underscore
	in = ""
	for i := 0; i < 59; i++ {
		in += "a"
	}
	in += "_extra_at_end" // total > MaxLen, with underscore at boundary
	out = SanitizeForPath(in)
	if len(out) > MaxLen {
		t.Errorf("not truncated: len=%d", len(out))
	}
	if len(out) > 0 && out[len(out)-1] == '_' {
		t.Errorf("trailing underscore not trimmed after truncation: %q", out)
	}
}
