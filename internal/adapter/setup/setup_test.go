package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mkAgentDir(t *testing.T, home, rel string) string {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(home string, apply bool) Options {
	return Options{Home: home, Command: "/bin/go-anything", Args: []string{"mcp"}, Apply: apply, Only: "claude"}
}

func TestDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	mkAgentDir(t, home, ".claude/x")
	changes, err := Run(opts(home, false))
	if err != nil || len(changes) != 1 || changes[0].Applied {
		t.Fatalf("dry-run: %+v err=%v", changes, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create the file")
	}
}

func TestApplyPreservesOtherKeysAndBacksUp(t *testing.T) {
	home := t.TempDir()
	mkAgentDir(t, home, ".claude/x")
	p := mkAgentDir(t, home, ".claude.json")
	orig := `{"theme":"dark","mcpServers":{"other":{"command":"x"}}}`
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := Run(opts(home, true))
	if err != nil || len(changes) != 1 || !changes[0].Applied || changes[0].Backup == "" {
		t.Fatalf("apply: %+v err=%v", changes, err)
	}
	if b, _ := os.ReadFile(changes[0].Backup); string(b) != orig {
		t.Fatal("backup must hold the original bytes")
	}
	var doc map[string]any
	data, _ := os.ReadFile(p)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["theme"] != "dark" {
		t.Fatal("unrelated key was lost")
	}
	servers := doc["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("existing MCP server was lost")
	}
	if _, ok := servers[ServerName]; !ok {
		t.Fatal("go-anything entry missing")
	}
}

func TestSecondApplyIsUnchanged(t *testing.T) {
	home := t.TempDir()
	mkAgentDir(t, home, ".claude/x")
	if _, err := Run(opts(home, true)); err != nil {
		t.Fatal(err)
	}
	changes, err := Run(opts(home, true))
	if err != nil || changes[0].Action != "unchanged" {
		t.Fatalf("want unchanged, got %+v err=%v", changes, err)
	}
}

func TestInvalidJSONIsNeverOverwritten(t *testing.T) {
	home := t.TempDir()
	mkAgentDir(t, home, ".claude/x")
	p := mkAgentDir(t, home, ".claude.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(opts(home, true)); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
	if b, _ := os.ReadFile(p); string(b) != "{not json" {
		t.Fatal("invalid file must be left untouched")
	}
}

func TestSkipsAgentsNotInstalled(t *testing.T) {
	home := t.TempDir()
	changes, err := Run(Options{Home: home, Command: "/bin/x", Apply: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("no agent dirs means no changes, got %+v", changes)
	}
}

// TestPiAgentRegistersSkillAndMCP pins the pi entry: the same mcpServers JSON shape as Claude and
// Cursor, and a skill folder under .pi/agent/skills.
func TestPiAgentRegistersSkillAndMCP(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	changes, err := Run(Options{Home: home, Command: "/bin/go-anything", Args: []string{"mcp"}, Apply: true, Only: "pi", Skill: true})
	if err != nil {
		t.Fatal(err)
	}
	var sawConfig, sawSkill bool
	for _, c := range changes {
		if c.Agent != "pi" {
			t.Fatalf("Only=pi must not touch %s", c.Agent)
		}
		switch c.Kind {
		case "config":
			sawConfig = true
			data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]map[string]map[string]any
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("pi mcp.json must stay the shared mcpServers shape: %v", err)
			}
			if cfg["mcpServers"]["go-anything"]["command"] != "/bin/go-anything" {
				t.Fatalf("server entry wrong: %v", cfg["mcpServers"]["go-anything"])
			}
		case "skill":
			sawSkill = true
			if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "skills", "go-anything", "SKILL.md")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !sawConfig || !sawSkill {
		t.Fatalf("pi must get both config and skill, got config=%v skill=%v", sawConfig, sawSkill)
	}
	if p := SkillPath(home, "pi"); p == "" {
		t.Fatal("SkillPath must resolve for pi")
	}
}
