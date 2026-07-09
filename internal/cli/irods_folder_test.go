package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptPackChoice_ShortcutLetters(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantMode    packMode
		wantCleanup postUploadCleanup
	}{
		{"[a] archive, keep everything", "a\n", packTarGz, postUploadCleanup{}},
		{"[A] archive + clean archive", "A\n", packTarGz, postUploadCleanup{archive: true}},
		{"[X] archive + clean archive + clean source", "X\n", packTarGz, postUploadCleanup{archive: true, source: true}},
		{"[u] unarchived", "u\n", packNone, postUploadCleanup{}},
		{"[c] cancel", "c\n", "cancel", postUploadCleanup{}},
		{"unarchived uppercase falls through to lowercase", "U\n", packNone, postUploadCleanup{}},
		{"empty defaults to cancel", "\n", "cancel", postUploadCleanup{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			mode, cleanup, err := promptPackChoice(strings.NewReader(tc.input), &out)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.wantMode {
				t.Errorf("mode: got %q, want %q (out=%q)", mode, tc.wantMode, out.String())
			}
			if cleanup != tc.wantCleanup {
				t.Errorf("cleanup: got %+v, want %+v", cleanup, tc.wantCleanup)
			}
		})
	}
}

func TestPromptPackChoice_LowerCaseXIsInvalid(t *testing.T) {
	// The destructive [X] letter is deliberately upper-case-only to
	// reduce the risk of a stray keystroke wiping local data. `x`
	// must NOT be treated as a valid choice — falls through to the
	// invalid-choice loop.
	var out bytes.Buffer
	// Feed x + 2 more invalid → 3 invalids total → error.
	_, _, err := promptPackChoice(strings.NewReader("x\nz\nz\n"), &out)
	if err == nil {
		t.Error("expected lowercase x to be treated as invalid")
	}
	if !strings.Contains(out.String(), "invalid choice") {
		t.Errorf("expected 'invalid choice' feedback; got:\n%s", out.String())
	}
}

func TestPromptPackChoice_WordAliases(t *testing.T) {
	// Word-form aliases keep working (helpful for non-interactive
	// callers piping `archive\n` etc.) but the displayed shortcuts
	// are the single-letter forms. Word aliases carry NO cleanup —
	// they map to the "keep everything" variant.
	cases := []struct {
		input       string
		wantMode    packMode
		wantCleanup postUploadCleanup
	}{
		{"archive\n", packTarGz, postUploadCleanup{}},
		{"tar\n", packTarGz, postUploadCleanup{}},
		{"tar.gz\n", packTarGz, postUploadCleanup{}},
		{"targz\n", packTarGz, postUploadCleanup{}},
		{"unarchived\n", packNone, postUploadCleanup{}},
		{"as-is\n", packNone, postUploadCleanup{}},
		{"asis\n", packNone, postUploadCleanup{}},
		{"cancel\n", "cancel", postUploadCleanup{}},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		mode, cleanup, err := promptPackChoice(strings.NewReader(tc.input), &out)
		if err != nil {
			t.Fatalf("input %q: %v", tc.input, err)
		}
		if mode != tc.wantMode {
			t.Errorf("input %q: mode got %q, want %q", tc.input, mode, tc.wantMode)
		}
		if cleanup != tc.wantCleanup {
			t.Errorf("input %q: cleanup got %+v, want %+v", tc.input, cleanup, tc.wantCleanup)
		}
	}
}

func TestPromptPackChoice_PromptMentionsAllFiveLetters(t *testing.T) {
	// First pass with a valid input still prints the prompt once.
	// The displayed letters MUST include the new destructive [A]
	// and [X] variants alongside [a] / [u] / [c].
	var out bytes.Buffer
	mode, _, err := promptPackChoice(strings.NewReader("a\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if mode != packTarGz {
		t.Fatalf("setup failed: %q", mode)
	}
	prompt := out.String()
	for _, want := range []string{
		"[a] archive",
		"[A] archive + clean",
		"[X] excise",
		"[u] unarchived",
		"[c] cancel",
		"Choice [a/A/X/u/c]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// The old wording must NOT appear — regression guard.
	for _, banned := range []string{"[t] pack", "[a] upload as-is", "[t/a/c]", "[a/u/c]"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("prompt still contains old wording %q:\n%s", banned, prompt)
		}
	}
}

func TestPromptPackChoice_GivesUpAfterRepeatedInvalid(t *testing.T) {
	var out bytes.Buffer
	_, _, err := promptPackChoice(strings.NewReader("z\nz\nz\n"), &out)
	if err == nil {
		t.Error("expected an error after 3 invalid choices")
	}
}
