// Package dataproject defines the regex + sanitisation rules for the
// `data_project` config key (e.g. "Fiers2025", "Fiers_Mancuso_2025") and
// for deriving a path-safe component from an ELN experiment name.
//
// Both rules are shared by `mus config set data_project`, `mus eln tag` (to
// validate user-confirmed names + suggested names) and `mus irods upload` (to
// build the remote path). Keep the rules HERE so every callsite enforces
// the same conventions — silent drift in spelling rules has bitten us
// before.
package dataproject

import (
	"fmt"
	"regexp"
	"strings"
)

// nameRE matches the data_project format: must start with a letter, contain
// only letters / digits / underscores, and end with exactly four digits (the
// year). Examples accepted:
//
//	Fiers2025
//	Fiers_Mancuso_2025
//	Mus_Lab_Pilot2024
//
// Examples rejected:
//
//	fiers2025         (lowercase first letter — keep proper-name convention)
//	Fiers-2025        (hyphen)
//	Fiers25           (only two-digit year)
//	2025_Fiers        (doesn't end in year)
//
// Same shape as ant_s3 uses for its S3 group folders, so a single
// data_project label can serve both back-ends.
var nameRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*\d{4}$`)

// MaxLen caps the sanitised experiment-name component on iRODS so paths
// stay readable and below filesystem-friendly limits.
const MaxLen = 60

// ValidateName returns nil if s is a syntactically valid data_project name,
// else a descriptive error.
func ValidateName(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("data_project is empty")
	}
	if !nameRE.MatchString(s) {
		return fmt.Errorf(
			"%q is not a valid data_project name.\n"+
				"  Required: starts with an uppercase letter, only A-Z/a-z/0-9/_, "+
				"and ends in a 4-digit year.\n"+
				"  Examples: Fiers2025, Fiers_Mancuso_2025, MusLab_Pilot2024.",
			s)
	}
	return nil
}

// SanitizeForPath turns an arbitrary ELN experiment name into a path-safe,
// reproducible component. Rules (in order):
//
//  1. Trim outer whitespace.
//  2. Replace any non-[A-Za-z0-9] rune with `_`.
//  3. Collapse runs of `_` to a single `_`.
//  4. Trim leading/trailing `_`.
//  5. Truncate at MaxLen, then re-trim trailing `_`.
//
// Examples:
//
//	"Single-cell pilot — Fiers lab"            → "Single_cell_pilot_Fiers_lab"
//	"Single-cell RNA-seq of microglia (PRS)"   → "Single_cell_RNA_seq_of_microglia_PRS"
//	"   Lots   of   whitespace   "             → "Lots_of_whitespace"
//
// Returns "" if the input contains no alphanumeric characters at all. The
// caller must treat "" as "no safe name derivable" and refuse to use it.
func SanitizeForPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()

	// Collapse runs of underscores.
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	out = strings.Trim(out, "_")

	if len(out) > MaxLen {
		out = strings.TrimRight(out[:MaxLen], "_")
	}
	return out
}
