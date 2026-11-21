package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A tree with a go.mod and Python files: auto picks Go, -lang python picks the Python files, and the
// flag may come before or after the root.
func TestAnalyzeLangFlag(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	root := filepath.Join(t.TempDir(), "mixed repo")
	files := map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.27.4\n",
		"calc/calc.go":   "package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n",
		"tool/run.py":    "import subprocess\n\n\ndef run():\n    subprocess.run(['true'])\n",
		"tool/helper.py": "def helper():\n    return 1\n",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	decode := func(args ...string) map[string]any {
		t.Helper()
		var out bytes.Buffer
		if err := cmdAnalyze(args, &out); err != nil {
			t.Fatalf("analyze %v: %v", args, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("stdout must be one JSON document: %v", err)
		}
		return doc
	}
	if doc := decode(root); doc["language"] != "go" || doc["module"] != "example.com/m" {
		t.Fatalf("auto with a go.mod: %v %v", doc["language"], doc["module"])
	}
	for _, args := range [][]string{{"-lang", "python", root}, {root, "-lang", "python"}} {
		doc := decode(args...)
		if doc["language"] != "python" {
			t.Fatalf("%v: language %v", args, doc["language"])
		}
		pkgs := doc["packages"].([]any)
		if len(pkgs) != 2 || pkgs[0].(map[string]any)["go_package"] != "internal/tool/helper" {
			t.Fatalf("%v: packages %v", args, pkgs)
		}
		if order := doc["port_order"].([]any); len(order) != 2 {
			t.Fatalf("port_order: %v", order)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".go-anything", "reports", "*", "v*", "analysis.html"))
	var python string
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "original in Python") {
			python = string(b)
		}
	}
	if python == "" || !strings.Contains(python, "internal/tool/run") || strings.Contains(python, "non-test Go lines") {
		t.Fatalf("the Python report must be written and read as Python (%d reports)", len(matches))
	}

	if err := cmdAnalyze([]string{"-lang", "cobol", root}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "python") {
		t.Fatalf("an unknown -lang must list the supported languages: %v", err)
	}
	if err := cmdAnalyze([]string{root, root}, &bytes.Buffer{}); err == nil {
		t.Fatal("two roots must be refused")
	}
}
