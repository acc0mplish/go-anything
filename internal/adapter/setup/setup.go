// Package setup registers the go-anything MCP server with coding-agent apps and installs its skill.
//
// Safety rules (mirrors rust-anything's setup):
//   - dry-run is the default; nothing is written unless Apply is true;
//   - existing keys in a settings file are preserved (only our entry changes);
//   - an unparseable settings file is an error, never overwritten;
//   - the original file is copied to <path>.bak before any write;
//   - an agent counts as installed only when its own directory exists; setup never creates it.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// ServerName is the key go-anything registers under in each agent's config.
const ServerName = "go-anything"

// Format is how an agent stores its MCP servers.
type Format int

const (
	// FormatMCPServers is a JSON object with a top-level `mcpServers` map (Claude Code, Cursor, Gemini CLI).
	FormatMCPServers Format = iota
	// FormatOpenCode is OpenCode's `mcp.servers` map, where `command` is the whole argv.
	FormatOpenCode
	// FormatCodexTOML is Codex's `[mcp_servers.<name>]` table in config.toml.
	FormatCodexTOML
)

// Agent is one coding-agent app.
type Agent struct {
	Name   string
	Path   string // settings file, relative to home
	Marker string // the app's own directory, relative to home; must exist to count as installed
	Format Format
}

// Agents lists every app go-anything can register with.
var Agents = []Agent{
	{Name: "claude", Path: ".claude.json", Marker: ".claude", Format: FormatMCPServers},
	{Name: "cursor", Path: ".cursor/mcp.json", Marker: ".cursor", Format: FormatMCPServers},
	{Name: "gemini", Path: ".gemini/settings.json", Marker: ".gemini", Format: FormatMCPServers},
	{Name: "opencode", Path: ".config/opencode/opencode.json", Marker: ".config/opencode", Format: FormatOpenCode},
	{Name: "codex", Path: ".codex/config.toml", Marker: ".codex", Format: FormatCodexTOML},
}

// Change describes what setup would do (or did).
type Change struct {
	Agent   string
	Path    string
	Kind    string // "config" or "skill"
	Action  string // "create", "update", "unchanged"
	Applied bool
	Backup  string
}

// Options controls one setup run.
type Options struct {
	Home    string
	Command string   // absolute path to this binary
	Args    []string // e.g. ["mcp"]
	Apply   bool
	Only    string // restrict to one agent name; empty means all
	Skill   bool   // also install the skill where the agent reads skills
}

// Run plans, and when Apply is set performs, registration (and skill install) for each installed agent.
func Run(opts Options) ([]Change, error) {
	if opts.Home == "" {
		return nil, errors.New("setup: home directory is required")
	}
	if opts.Command == "" {
		return nil, errors.New("setup: command path is required")
	}
	var changes []Change
	for _, a := range Agents {
		if opts.Only != "" && a.Name != opts.Only {
			continue
		}
		if fi, err := os.Stat(filepath.Join(opts.Home, a.Marker)); err != nil || !fi.IsDir() {
			continue // not installed: never create its directories
		}
		c, err := planConfig(a, opts)
		if err != nil {
			return changes, err
		}
		changes = append(changes, c)
		if opts.Skill {
			sc, ok, err := installSkill(a.Name, opts.Home, opts.Apply)
			if err != nil {
				return changes, err
			}
			if ok {
				changes = append(changes, sc)
			}
		}
	}
	return changes, nil
}

func planConfig(a Agent, opts Options) (Change, error) {
	path := filepath.Join(opts.Home, a.Path)
	c := Change{Agent: a.Name, Path: path, Kind: "config"}
	var (
		data    []byte
		existed bool
		err     error
	)
	if data, err = os.ReadFile(path); err == nil {
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, fmt.Errorf("setup: read %s: %w", path, err)
	}

	var out []byte
	var same bool
	switch a.Format {
	case FormatMCPServers:
		out, same, err = editMCPServers(data, path, opts)
	case FormatOpenCode:
		out, same, err = editOpenCode(data, path, opts)
	case FormatCodexTOML:
		out, same, err = editCodexTOML(data, path, opts)
	}
	if err != nil {
		return c, err
	}
	if same {
		c.Action = "unchanged"
		return c, nil
	}
	c.Action = "create"
	if existed {
		c.Action = "update"
	}
	if !opts.Apply {
		return c, nil
	}
	backup, err := writeWithBackup(path, out, existed)
	if err != nil {
		return c, err
	}
	c.Applied = true
	c.Backup = backup
	return c, nil
}

func serverEntry(opts Options) map[string]any {
	return map[string]any{"command": opts.Command, "args": opts.Args, "type": "stdio"}
}

// editMCPServers edits a JSON file with a top-level `mcpServers` object.
func editMCPServers(data []byte, path string, opts Options) ([]byte, bool, error) {
	doc, err := parseJSONObject(data, path)
	if err != nil {
		return nil, false, err
	}
	servers, err := childObject(doc, "mcpServers", path)
	if err != nil {
		return nil, false, err
	}
	entry := serverEntry(opts)
	if cur, ok := servers[ServerName].(map[string]any); ok && jsonEqual(cur, entry) {
		return nil, true, nil
	}
	servers[ServerName] = entry
	doc["mcpServers"] = servers
	out, err := marshalJSON(doc)
	return out, false, err
}

// editOpenCode edits opencode.json: `mcp.servers.<name>` with `command` as the full argv.
func editOpenCode(data []byte, path string, opts Options) ([]byte, bool, error) {
	doc, err := parseJSONObject(data, path)
	if err != nil {
		return nil, false, err
	}
	mcp, err := childObject(doc, "mcp", path)
	if err != nil {
		return nil, false, err
	}
	servers, err := childObject(mcp, "servers", path)
	if err != nil {
		return nil, false, err
	}
	argv := append([]string{opts.Command}, opts.Args...)
	entry := map[string]any{"type": "local", "command": argv}
	if cur, ok := servers[ServerName].(map[string]any); ok && jsonEqual(cur, entry) {
		return nil, true, nil
	}
	servers[ServerName] = entry
	mcp["servers"] = servers
	doc["mcp"] = mcp
	out, err := marshalJSON(doc)
	return out, false, err
}

// editCodexTOML edits config.toml without re-serializing it. Every byte outside our own
// [mcp_servers.<ServerName>] table is kept as is, so comments, ordering and formatting survive.
// The file is parsed only to validate it and to read the current entry.
func editCodexTOML(data []byte, path string, opts Options) ([]byte, bool, error) {
	doc := map[string]any{}
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &doc); err != nil {
			return nil, false, fmt.Errorf("setup: %s is not valid TOML; fix it or add the server by hand: %w", path, err)
		}
	}
	servers, err := tomlTable(doc, "mcp_servers", path)
	if err != nil {
		return nil, false, err
	}
	if cur, ok := servers[ServerName].(map[string]any); ok && cur["command"] == opts.Command && sameArgs(cur["args"], opts.Args) {
		return nil, true, nil
	}
	block := codexServerBlock(opts)
	out := spliceTable(string(data), "mcp_servers."+ServerName, block)
	// Validate the result before anything is written: a bad splice must never reach the disk.
	if err := toml.Unmarshal([]byte(out), &map[string]any{}); err != nil {
		return nil, false, fmt.Errorf("setup: splicing %s produced invalid TOML (nothing written): %w", path, err)
	}
	return []byte(out), false, nil
}

// codexServerBlock renders our table. Strings are TOML basic strings, escaped so a path with
// quotes or backslashes cannot break the file.
func codexServerBlock(opts Options) string {
	args := make([]string, len(opts.Args))
	for i, a := range opts.Args {
		args[i] = tomlString(a)
	}
	return "[mcp_servers." + ServerName + "]\n" +
		"command = " + tomlString(opts.Command) + "\n" +
		"args = [" + strings.Join(args, ", ") + "]\n"
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// spliceTable replaces the table with header `[name]` (up to the next table header or end of
// file) by block, or appends block when the table is absent. All other text is untouched.
func spliceTable(text, name, block string) string {
	lines := strings.SplitAfter(text, "\n")
	header := "[" + name + "]"
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start < 0 {
		sep := ""
		if text != "" && !strings.HasSuffix(text, "\n") {
			sep = "\n"
		}
		if text != "" {
			sep += "\n"
		}
		return text + sep + block
	}
	// Our table's own lines run until the next table header. Comment and blank lines directly
	// above that header belong to the next table, so they are kept, not swallowed.
	next := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			next = i
			break
		}
	}
	end := next
	for end > start+1 {
		t := strings.TrimSpace(lines[end-1])
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		end--
	}
	var b strings.Builder
	for _, l := range lines[:start] {
		b.WriteString(l)
	}
	b.WriteString(block)
	// Keep a blank line between our table and whatever follows, so the next table is not glued on.
	if end < len(lines) {
		b.WriteString("\n")
	}
	for _, l := range lines[end:] {
		b.WriteString(l)
	}
	return b.String()
}

func parseJSONObject(data []byte, path string) (map[string]any, error) {
	doc := map[string]any{}
	if len(data) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("setup: %s is not valid JSON; fix it or add the server by hand: %w", path, err)
	}
	return doc, nil
}

func childObject(parent map[string]any, key, path string) (map[string]any, error) {
	raw, present := parent[key]
	if !present || raw == nil {
		return map[string]any{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("setup: `%s` in %s is not an object; add the server by hand", key, path)
	}
	return obj, nil
}

func tomlTable(parent map[string]any, key, path string) (map[string]any, error) {
	raw, present := parent[key]
	if !present {
		return map[string]any{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("setup: `%s` in %s is not a table; add the server by hand", key, path)
	}
	return obj, nil
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func sameArgs(raw any, want []string) bool {
	got, ok := raw.([]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func marshalJSON(doc map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("setup: encode: %w", err)
	}
	return append(out, '\n'), nil
}

func writeWithBackup(path string, out []byte, existed bool) (string, error) {
	backup := ""
	if existed {
		backup = path + ".bak"
		prev, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("setup: read for backup %s: %w", path, err)
		}
		if err := os.WriteFile(backup, prev, 0o600); err != nil {
			return "", fmt.Errorf("setup: write backup %s: %w", backup, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("setup: mkdir: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", fmt.Errorf("setup: write %s: %w", path, err)
	}
	return backup, nil
}

func jsonEqual(a, b map[string]any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
