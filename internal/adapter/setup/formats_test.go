package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seed(t *testing.T, home, rel, content string) string {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func only(home, agent string, skill bool) Options {
	return Options{Home: home, Command: "/bin/go-anything", Args: []string{"mcp"}, Apply: true, Only: agent, Skill: skill}
}

func TestOpenCodeWritesArgvAndKeepsOtherKeys(t *testing.T) {
	home := t.TempDir()
	p := seed(t, home, ".config/opencode/opencode.json",
		`{"model":"x","mcp":{"servers":{"other":{"type":"local","command":["y"]}}}}`)
	seed(t, home, ".config/opencode/.keep", "")
	if _, err := Run(only(home, "opencode", false)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{`"model": "x"`, `"other"`, `"go-anything"`, `"/bin/go-anything"`, `"mcp"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in:\n%s", want, s)
		}
	}
}

func TestCodexTOMLKeepsOtherTablesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	p := seed(t, home, ".codex/config.toml",
		"model = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"y\"\nargs = []\n")
	if _, err := Run(only(home, "codex", false)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{`model = "gpt"`, `[mcp_servers.other]`, `[mcp_servers.go-anything]`, `command = "/bin/go-anything"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	again, err := Run(only(home, "codex", false))
	if err != nil || again[0].Action != "unchanged" {
		t.Fatalf("second run must be unchanged: %+v err=%v", again, err)
	}
}

func TestCodexRejectsInvalidTOMLWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	p := seed(t, home, ".codex/config.toml", "this is = not [valid")
	if _, err := Run(only(home, "codex", false)); err == nil {
		t.Fatal("invalid TOML must be an error")
	}
	if b, _ := os.ReadFile(p); string(b) != "this is = not [valid" {
		t.Fatal("invalid file must be untouched")
	}
}

func TestSkillInstalledForClaudeAndSkippedForCursor(t *testing.T) {
	home := t.TempDir()
	seed(t, home, ".claude/x", "")
	seed(t, home, ".cursor/x", "")
	changes, err := Run(Options{Home: home, Command: "/bin/x", Apply: true, Skill: true})
	if err != nil {
		t.Fatal(err)
	}
	var skillSeen, cursorSkill bool
	for _, c := range changes {
		if c.Kind == "skill" && c.Agent == "claude" && c.Applied {
			skillSeen = true
		}
		if c.Kind == "skill" && c.Agent == "cursor" {
			cursorSkill = true
		}
	}
	if !skillSeen {
		t.Fatal("claude skill must be installed")
	}
	if cursorSkill {
		t.Fatal("cursor has no skills folder; no skill must be planned")
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude/skills/go-anything/SKILL.md"))
	if err != nil || string(b) != SkillMD {
		t.Fatalf("installed skill must match embedded copy: %v", err)
	}
}

func TestSkillDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	seed(t, home, ".claude/x", "")
	if _, err := Run(Options{Home: home, Command: "/bin/x", Apply: false, Skill: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/go-anything")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create the skill")
	}
}

func TestEmbeddedSkillHasFrontmatter(t *testing.T) {
	if !strings.HasPrefix(SkillMD, "---\nname: go-anything\n") {
		t.Fatal("embedded skill must start with name frontmatter")
	}
}
