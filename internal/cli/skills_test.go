package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundledSkills_Registry checks that every entry in
// bundledSkills is non-empty and has a well-formed name/description.
// Frontmatter mismatches break Claude's skill loader silently, so
// catching this at unit-test time is worth the few lines.
func TestBundledSkills_Registry(t *testing.T) {
	if len(bundledSkills) == 0 {
		t.Fatal("bundledSkills is empty — at least one skill must ship")
	}
	wantNames := map[string]bool{
		"mus":              false,
		"mus-h5ad-deposit": false,
		"mus-de-deposit":   false,
	}
	for _, sk := range bundledSkills {
		if sk.Name == "" {
			t.Errorf("skill with empty Name: %+v", sk)
		}
		if sk.Description == "" {
			t.Errorf("skill %q: empty Description (load trigger)", sk.Name)
		}
		if len(sk.Description) < 80 {
			t.Errorf("skill %q: description too short (%d chars) — Claude needs context to decide to load",
				sk.Name, len(sk.Description))
		}
		if sk.Body == "" {
			t.Errorf("skill %q: empty Body", sk.Name)
		}
		if _, ok := wantNames[sk.Name]; ok {
			wantNames[sk.Name] = true
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("expected skill %q in bundledSkills, not found", name)
		}
	}
}

// TestInstallClaudeSkills_WritesAllSkills runs the installer against
// a stub ~/.claude/skills dir and checks every bundled skill landed
// with the expected frontmatter + body.
func TestInstallClaudeSkills_WritesAllSkills(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	// Installer requires the root to exist as a dir; create it.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := InstallClaudeSkills(&stderr); err != nil {
		t.Fatalf("InstallClaudeSkills returned error: %v", err)
	}
	for _, sk := range bundledSkills {
		target := filepath.Join(root, "skills", sk.Name, "SKILL.md")
		data, err := os.ReadFile(target)
		if err != nil {
			t.Errorf("skill %q: %v", sk.Name, err)
			continue
		}
		text := string(data)
		// Frontmatter check.
		if !strings.HasPrefix(text, "---\n") {
			t.Errorf("skill %q: missing YAML frontmatter open", sk.Name)
		}
		if !strings.Contains(text, "name: "+sk.Name+"\n") {
			t.Errorf("skill %q: frontmatter missing name field", sk.Name)
		}
		// The body should land verbatim somewhere after the frontmatter.
		bodySnippet := sk.Body
		if len(bodySnippet) > 80 {
			bodySnippet = bodySnippet[:80]
		}
		if !strings.Contains(text, bodySnippet) {
			t.Errorf("skill %q: body not embedded in SKILL.md", sk.Name)
		}
	}
}

// TestInstallClaudeSkills_NoOpWithoutClaudeDir confirms the safety
// rail: if ~/.claude doesn't exist, the installer does NOT create
// it. mus must not grow a phantom config dir for non-Claude users.
func TestInstallClaudeSkills_NoOpWithoutClaudeDir(t *testing.T) {
	tmp := t.TempDir()
	nonExistent := filepath.Join(tmp, "no-such-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", nonExistent)
	var stderr bytes.Buffer
	if err := InstallClaudeSkills(&stderr); err != nil {
		t.Fatalf("returned error: %v", err)
	}
	if _, err := os.Stat(nonExistent); err == nil {
		t.Errorf("installer created %s; expected no-op when Claude isn't installed", nonExistent)
	}
	if !strings.Contains(stderr.String(), "no Claude install detected") {
		t.Errorf("expected polite skip note on stderr; got: %s", stderr.String())
	}
}

// TestMusSkill_BodyMentionsDEFlow checks the umbrella skill body
// actually carries the step-by-step DE-deposit guidance the user
// asked for. Catch removal of the load-bearing section by accident.
func TestMusSkill_BodyMentionsDEFlow(t *testing.T) {
	var musBody string
	for _, sk := range bundledSkills {
		if sk.Name == "mus" {
			musBody = sk.Body
			break
		}
	}
	if musBody == "" {
		t.Fatal("`mus` umbrella skill not found")
	}
	// Each bullet here represents a step Claude must perform when
	// helping a depositor — losing any is a regression.
	requirements := []string{
		"Pre-flight checklist",
		"mus eln status",
		"mus secret status",
		"mus config show",
		"mus de check",
		"mus h5ad check",
		"mus s3 upload",
		"de_dataset_id",
		"data_project",
		"DON'T skip",
	}
	for _, req := range requirements {
		if !strings.Contains(musBody, req) {
			t.Errorf("mus skill body missing required phrase: %q", req)
		}
	}
}
