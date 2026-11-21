package setup

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// SkillMD is the workflow skill, embedded in the binary so setup needs no source checkout.
// It covers all four workflows (CLI verify, the refine loop, the security confirmation checklist,
// the authorization audit); an agent that only knows record and verify will misreport what
// go-anything can decide.
//
//go:embed skill/SKILL.md
var SkillMD string

// skillDir returns where the skill is installed for an agent that reads skills from disk.
// Agents without a skills folder (Cursor, Gemini CLI) get no skill.
func skillDir(home, agent string) string {
	switch agent {
	case "claude":
		return filepath.Join(home, ".claude", "skills", ServerName)
	case "codex":
		return filepath.Join(home, ".codex", "skills", ServerName)
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "skills", ServerName)
	case "pi":
		return filepath.Join(home, ".pi", "agent", "skills", ServerName)
	}
	return ""
}

// installSkill writes SKILL.md when it differs from the embedded copy (plan or apply).
func installSkill(agent, home string, apply bool) (Change, bool, error) {
	dir := skillDir(home, agent)
	if dir == "" {
		return Change{}, false, nil
	}
	path := filepath.Join(dir, "SKILL.md")
	c := Change{Agent: agent, Path: path, Kind: "skill"}
	if cur, err := os.ReadFile(path); err == nil {
		if string(cur) == SkillMD {
			c.Action = "unchanged"
			return c, true, nil
		}
		c.Action = "update"
	} else if os.IsNotExist(err) {
		c.Action = "create"
	} else {
		return c, true, fmt.Errorf("setup: read skill %s: %w", path, err)
	}
	if !apply {
		return c, true, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return c, true, fmt.Errorf("setup: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(SkillMD), 0o600); err != nil {
		return c, true, fmt.Errorf("setup: write skill %s: %w", path, err)
	}
	c.Applied = true
	return c, true, nil
}

// SkillPath returns where the skill file for an agent would be installed, or "" when that agent
// has no skill folder. It does not check whether the file exists.
func SkillPath(home, agent string) string {
	dir := skillDir(home, agent)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "SKILL.md")
}
