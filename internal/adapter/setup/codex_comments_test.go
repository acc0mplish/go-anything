package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

const codexWithComments = `# my codex settings, do not reformat
model = "gpt"   # keep this inline comment

# unrelated server, comments above must survive
[mcp_servers.other]
command = "y"   # other tool
args = []

# a table after ours
[profiles.work]
model = "x"
`

func codexOnce(t *testing.T, home string) Change {
	t.Helper()
	changes, err := Run(Options{Home: home, Command: "/bin/go-anything", Args: []string{"mcp"}, Apply: true, Only: "codex"})
	if err != nil || len(changes) != 1 {
		t.Fatalf("run: %+v err=%v", changes, err)
	}
	return changes[0]
}

func TestCodexKeepsCommentsAndOtherTablesByteForByte(t *testing.T) {
	home := t.TempDir()
	p := seed(t, home, ".codex/config.toml", codexWithComments)
	codexOnce(t, home)
	got, _ := os.ReadFile(p)
	s := string(got)
	if !strings.HasPrefix(s, codexWithComments) {
		t.Fatalf("original content must be an unchanged prefix.\nwant prefix:\n%s\ngot:\n%s", codexWithComments, s)
	}
	if !strings.Contains(s, "[mcp_servers.go-anything]") {
		t.Fatalf("our table missing:\n%s", s)
	}
}

func TestCodexReplacesOnlyOurTableWhenCommandChanges(t *testing.T) {
	home := t.TempDir()
	stale := codexWithComments + "\n[mcp_servers.go-anything]\ncommand = \"/old/path\"\nargs = [\"mcp\"]\n\n# trailing note after ours\n[other.end]\nk = 1\n"
	p := seed(t, home, ".codex/config.toml", stale)
	codexOnce(t, home)
	got, _ := os.ReadFile(p)
	s := string(got)
	if strings.Contains(s, "/old/path") {
		t.Fatalf("stale command must be replaced:\n%s", s)
	}
	for _, keep := range []string{"# my codex settings", "# keep this inline comment", "# unrelated server", "# a table after ours", "[profiles.work]", "# trailing note after ours", "[other.end]"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("lost %q:\n%s", keep, s)
		}
	}
}

func TestCodexSecondRunIsUnchanged(t *testing.T) {
	home := t.TempDir()
	seed(t, home, ".codex/config.toml", codexWithComments)
	codexOnce(t, home)
	c := codexOnce(t, home)
	if c.Action != "unchanged" {
		t.Fatalf("second run must be unchanged, got %s", c.Action)
	}
}

func TestCodexSpecialCharactersStayValidTOML(t *testing.T) {
	home := t.TempDir()
	p := seed(t, home, ".codex/config.toml", "")
	changes, err := Run(Options{Home: home, Command: `C:\Program Files\go "x"`, Args: []string{"mcp", "a\tb"}, Apply: true, Only: "codex"})
	if err != nil || len(changes) != 1 {
		t.Fatalf("run: %+v err=%v", changes, err)
	}
	got, _ := os.ReadFile(p)
	var doc map[string]any
	if err := toml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("output must be valid TOML: %v\n%s", err, got)
	}
	srv := doc["mcp_servers"].(map[string]any)["go-anything"].(map[string]any)
	if srv["command"] != `C:\Program Files\go "x"` {
		t.Fatalf("command round-trip failed: %q", srv["command"])
	}
	if args := srv["args"].([]any); args[1] != "a\tb" {
		t.Fatalf("args round-trip failed: %q", args[1])
	}
}

func TestCodexInvalidTOMLStillNotOverwritten(t *testing.T) {
	home := t.TempDir()
	p := seed(t, home, ".codex/config.toml", "# keep me\nbroken = [")
	if _, err := Run(Options{Home: home, Command: "/bin/x", Args: []string{"mcp"}, Apply: true, Only: "codex"}); err == nil {
		t.Fatal("invalid TOML must be an error")
	}
	if b, _ := os.ReadFile(p); string(b) != "# keep me\nbroken = [" {
		t.Fatal("invalid file must be untouched")
	}
	_ = filepath.Join
}
