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
	// Each phrase here is load-bearing: Claude needs the verbatim
	// command name when helping a depositor. Losing any is a
	// regression — DO NOT add a phrase that doesn't correspond to
	// a real mus command (`mus secret status`, `mus eln status`,
	// `mus eln upload`, `mus verify` — all fake; have been removed).
	requirements := []string{
		"Pre-flight checklist",
		"mus config show",   // shows cascade by default (no --cascade flag)
		"mus secret list",   // real, NOT `mus secret status`
		"mus eln login",     // real, replaces the wrong `mus secret store eln`
		"mus s3 login",      // real, replaces the wrong `mus secret store s3`
		"iron auth",         // iRODS auth lives in iron's store
		"mus de check",
		"mus h5ad check",
		"mus s3 upload",
		"mus irods get",     // real, NOT `mus irods download`
		"mus irods check",   // real, NOT `mus verify`
		"de_dataset_id",
		"data_project",
		"DON'T skip",
		// mus 0.2.27+ behaviours — losing any of these means Claude
		// won't surface them when the depositor hits the situation.
		"Skip-if-same",       // iRODS upload skip on matching sha256
		"irods_group",        // group ACL config key
		"UNIX_FILE_OPEN_ERR", // mapped-error message
		"--force",            // overwrite-prompt bypass
		// mus 0.2.28+ — anthive catalog cross-check
		"Catalog cross-check",
		"--no-catalog-check",
		// mus 0.2.29+ — per-format required de_term/de_contrast gate
		"HARD-required PER format",
		"BOTH `de_term` and `de_contrast` REQUIRED",
		// mus 0.2.30+ — de_method always required + de_map_gene for long
		"de_map_gene",
		"de_method` always required",
		// mus 0.2.30+ — post-mapping column gate
		"Post-mapping column gate",
		"required CANONICAL columns",
		"GROUP_ID of Sun2026", // the canonical bug case
		// mus serve GUI (added in 0.2.26+)
		"mus serve",
		// mus 0.2.31+ — reshaped serve UI (iRODS-only, vocab-driven
		// form, selectable tree, dry-run, ✓ uploaded badge)
		"Catalog (S3 / anthive) uploads are intentionally",
		"set in this folder:",
		"inherited from cascade:",
		"Dry-run checkbox",
	}
	for _, req := range requirements {
		if !strings.Contains(musBody, req) {
			t.Errorf("mus skill body missing required phrase: %q", req)
		}
	}
}
