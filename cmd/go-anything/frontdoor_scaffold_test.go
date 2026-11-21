package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestFrontdoorScaffoldUsageErrors(t *testing.T) {
	repo := t.TempDir()
	for name, args := range map[string][]string{
		"no repo":         {"-cases", "x.json", "-original-cmd", "tool"},
		"no cases":        {repo, "-original-cmd", "tool"},
		"no original-cmd": {repo, "-cases", "x.json"},
		"two repos":       {repo, repo, "-cases", "x.json", "-original-cmd", "tool"},
	} {
		if err := cmdFrontdoorScaffold(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func writeRecording(t *testing.T, path string, cases []domain.AppCase) {
	t.Helper()
	data, err := json.Marshal(domain.AppRecording{Subject: "tool", Cases: cases})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFrontdoorScaffoldMixedKindsNamesCounts(t *testing.T) {
	repo := t.TempDir()
	cases := filepath.Join(repo, "app.json")
	writeRecording(t, cases, []domain.AppCase{
		{Name: "a", Kind: "http", Method: "GET", Path: "/a"},
		{Name: "b", Kind: "cli", Argv: []string{"b"}},
	})
	err := cmdFrontdoorScaffold([]string{repo, "-cases", cases, "-original-cmd", "tool"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "1 http and 1 cli") {
		t.Fatalf("want the counts in the error, got %v", err)
	}
}

func TestFrontdoorScaffoldCLIEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build and processes")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	repo := t.TempDir()
	script := filepath.Join(repo, "tool.py")
	src := "import sys\nif sys.argv[1] == 'fail':\n    sys.stderr.write('no\\n')\n    sys.exit(4)\nprint(' '.join(sys.argv[1:]) + ':' + sys.stdin.read())\n"
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := filepath.Join(repo, "app.json")
	writeRecording(t, cases, []domain.AppCase{
		{Name: "args", Kind: "cli", Argv: []string{"a", "b c"}, Stdout: "a b c:\n"},
		{Name: "stdin", Kind: "cli", Argv: []string{"in"}, Stdin: "xyz", Stdout: "in:xyz\n"},
		{Name: "fail", Kind: "cli", Argv: []string{"fail"}, ExitCode: 4, Stderr: "no\n"},
	})
	var out bytes.Buffer
	err := cmdFrontdoorScaffold([]string{repo, "-cases", cases, "-original-cmd", "python3 '" + script + "'"}, &out)
	t.Log(out.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "check: 3 of 3 recorded case(s) passed through the front door; 3 answered by the original (fallback)") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
	err = cmdFrontdoorScaffold([]string{repo, "-cases", cases, "-original-cmd", "python3 '" + script + "'"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Fatalf("a second scaffold must refuse, got %v", err)
	}
}
