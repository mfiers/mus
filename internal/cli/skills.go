package cli

// Claude-skills installer.
//
// A Claude "skill" is a markdown file at ~/.claude/skills/<name>/SKILL.md
// with a YAML frontmatter block describing when Claude should auto-invoke
// it. Mus ships a couple of skills as part of its binary so a fresh
// `mus upgrade` (or first-install) drops them into the right place and
// downstream Claude sessions can guide users through deposit / extract
// workflows without the user needing to point them at a doc.
//
// What we install today:
//
//   mus-h5ad-deposit/SKILL.md   — how to annotate + deposit an .h5ad file
//                                  for inclusion in the lab catalog. Auto-
//                                  triggers when the user asks Claude about
//                                  preparing h5ad metadata, mus.cfg vs
//                                  sidecar, vocab fields, deposit / upload
//                                  workflow, etc.
//
// All writes are best-effort and silent on every failure path. mus must
// never refuse an upgrade because we couldn't drop a skill file.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/atomicfile"
)

// InstallClaudeSkills writes (or refreshes) every skill mus ships
// into the user's ~/.claude/skills/ directory. Idempotent — always
// overwrites with the current content so an upgrade installs the
// matching skill version.
//
// Returns nil on every code path; logs to stderr on failures so the
// user at least sees the diagnostic without having the upgrade /
// install command fail.
//
// If Claude isn't installed (no ~/.claude directory), we DO NOT
// create one. Print a one-line polite note and skip — mus is useful
// without Claude and shouldn't grow a phantom config dir on every
// upgrade for users who never use Claude.
func InstallClaudeSkills(stderr io.Writer) error {
	root, err := claudeRootDir()
	if err != nil {
		fmt.Fprintf(stderr, "  note: no home directory found (%v) — skipping Claude skill install\n", err)
		return nil
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		// ~/.claude doesn't exist as a directory. User probably
		// doesn't have Claude installed. Don't create the directory
		// ourselves — that'd be an unsolicited side effect.
		fmt.Fprintf(stderr, "  (no Claude install detected at %s — skipping skill install)\n", root)
		return nil
	}
	dir := filepath.Join(root, "skills")
	for _, sk := range bundledSkills {
		if err := writeSkill(dir, sk, stderr); err != nil {
			fmt.Fprintf(stderr, "  note: failed to write skill %q: %v\n", sk.Name, err)
			continue
		}
	}
	return nil
}

// claudeRootDir returns the Claude config root (~/.claude or the
// env-override equivalent). It does NOT check existence — caller
// stats it to decide whether Claude is actually installed.
func claudeRootDir() (string, error) {
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		return env, nil
	}
	if env := os.Getenv("CLAUDE_HOME"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("no home directory")
	}
	return filepath.Join(home, ".claude"), nil
}

// bundledSkill is one skill the mus binary ships.
type bundledSkill struct {
	Name        string // directory + skill identifier (lowercase-dashes)
	Description string // YAML frontmatter `description:` — Claude's trigger text
	Body        string // markdown content after the frontmatter
}

// writeSkill writes <dir>/<sk.Name>/SKILL.md. Atomic via
// atomicfile.Replace — the temp+rename pattern means the file is
// either fully present with the new content or untouched.
func writeSkill(dir string, sk bundledSkill, stderr io.Writer) error {
	skillDir := filepath.Join(dir, sk.Name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", skillDir, err)
	}
	target := filepath.Join(skillDir, "SKILL.md")

	// Build the file: YAML frontmatter + a thin "installed by mus"
	// note so a curious user knows where it came from + the actual
	// body. The description is the load-bearing field — that's what
	// Claude reads to decide whether to auto-invoke.
	content := "---\n" +
		"name: " + sk.Name + "\n" +
		"description: " + yamlSafeString(sk.Description) + "\n" +
		"---\n\n" +
		"<!-- Installed by `mus upgrade` (or `mus _install-skills`).\n" +
		"     This file is rewritten on every mus upgrade; edits will\n" +
		"     be lost. Source lives in internal/cli/skills.go in the\n" +
		"     mus repo. -->\n\n" +
		sk.Body

	if err := atomicfile.Replace(target, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	fmt.Fprintf(stderr, "  ↻ Claude skill installed: %s\n", target)
	return nil
}

// yamlSafeString wraps a value in double quotes when it contains
// characters that need quoting in YAML (`:`, leading whitespace,
// `#`, …). For our descriptions, which are single-line prose, this
// is good enough — full YAML escaping not required.
func yamlSafeString(s string) string {
	// Conservative: quote if it contains any of these. Description
	// fields are always single-line so we don't worry about
	// newlines.
	for _, r := range s {
		if r == ':' || r == '#' || r == '\'' || r == '"' || r == '[' || r == ']' || r == '{' || r == '}' || r == ',' || r == '&' || r == '*' || r == '|' {
			return `"` + escapeForYAML(s) + `"`
		}
	}
	return s
}

func escapeForYAML(s string) string {
	out := make([]rune, 0, len(s)+8)
	for _, r := range s {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

// bundledSkills is the registry of everything `mus _install-skills`
// (and `mus upgrade`) writes out. Add to this list when introducing
// a new skill.
//
// Body for h5ad-deposit is the same primer text `mus h5ad howto`
// prints — single source of truth, kept inline so we don't have to
// thread file IO into the skill installer.
var bundledSkills = []bundledSkill{
	{
		Name: "mus-h5ad-deposit",
		Description: "Use this skill when the user asks how to annotate, prepare, " +
			"validate, deposit, or extract an .h5ad file for the lab catalog " +
			"(anthive) or for mus / iRODS / S3 upload. Triggers on questions about " +
			"folder vs file annotation (mus.cfg / *.h5ad.mus), the seven " +
			"hard-required vocabulary fields (organism, tissue, cell_type, " +
			"technology, suspension_type, access, source), the joint vocabulary, " +
			"`mus h5ad check`, `mus irods upload`, `mus s3 upload`, " +
			"`mus irods find`, `mus irods get`, the deposit / extract workflow " +
			"generally, OR how to write the catalog documentation files " +
			"(per-file <basename>.md and folder-level index.md with YAML " +
			"frontmatter — title, sha256, obsnum_include, top_cat, group, " +
			"group_id, default_dataset, etc.).",
		Body: h5adHowtoText,
	},
}

// newInstallSkillsCmd is the hidden CLI entry point `mus _install-skills`.
// Underscore prefix mirrors `mus _verify` — present but not advertised
// in `mus --help`. The install.sh installer and `mus upgrade` both
// trigger it; the user generally never types it.
func newInstallSkillsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_install-skills",
		Short:  "(internal) write bundled Claude skills under ~/.claude/skills",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return InstallClaudeSkills(cmd.ErrOrStderr())
		},
	}
}
