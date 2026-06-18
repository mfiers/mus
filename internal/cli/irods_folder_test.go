package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptPackChoice_ShortcutLetters(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  packMode
	}{
		{"archive shortcut", "a\n", packTarGz},
		{"unarchived shortcut", "u\n", packNone},
		{"cancel shortcut", "c\n", "cancel"},
		{"archive uppercase", "A\n", packTarGz},
		{"unarchived uppercase", "U\n", packNone},
		{"empty defaults to cancel", "\n", "cancel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := promptPackChoice(strings.NewReader(tc.input), &out)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q (out=%q)", got, tc.want, out.String())
			}
		})
	}
}

func TestPromptPackChoice_WordAliases(t *testing.T) {
	// Word-form aliases keep working (helpful for non-interactive
	// callers piping `archive\n` etc.) but the displayed shortcuts
	// are the single-letter forms.
	cases := []struct {
		input string
		want  packMode
	}{
		{"archive\n", packTarGz},
		{"tar\n", packTarGz},
		{"tar.gz\n", packTarGz},
		{"targz\n", packTarGz},
		{"unarchived\n", packNone},
		{"as-is\n", packNone},
		{"asis\n", packNone},
		{"cancel\n", "cancel"},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		got, err := promptPackChoice(strings.NewReader(tc.input), &out)
		if err != nil {
			t.Fatalf("input %q: %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("input %q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPromptPackChoice_PromptMentionsNewLetters(t *testing.T) {
	// First pass with garbage input → loop reprints the prompt.
	// The displayed letters MUST be the new a/u/c set.
	var out bytes.Buffer
	got, err := promptPackChoice(strings.NewReader("a\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if got != packTarGz {
		t.Fatalf("setup failed: %q", got)
	}
	prompt := out.String()
	for _, want := range []string{
		"[a] archive",
		"[u] unarchived",
		"[c] cancel",
		"Choice [a/u/c]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// The old "[t]" and the old "[a] upload as-is" wording must NOT
	// appear — regression guard against a partial revert.
	for _, banned := range []string{"[t] pack", "[a] upload as-is", "[t/a/c]"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("prompt still contains old wording %q:\n%s", banned, prompt)
		}
	}
}

func TestPromptPackChoice_GivesUpAfterRepeatedInvalid(t *testing.T) {
	var out bytes.Buffer
	_, err := promptPackChoice(strings.NewReader("z\nz\nz\n"), &out)
	if err == nil {
		t.Error("expected an error after 3 invalid choices")
	}
}
