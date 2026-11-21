package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeSecondRunReportsChangesSinceTheFirst(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.27.4\n")
	write("calc/calc.go", "package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n")
	if err := cmdAnalyze([]string{root}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first analyze: %v", err)
	}
	write("text/text.go", "package text\n\n// Upper is a stub.\nfunc Upper(s string) string { return s }\n")
	if err := cmdAnalyze([]string{root}, &bytes.Buffer{}); err != nil {
		t.Fatalf("second analyze: %v", err)
	}
	dirs, _ := filepath.Glob(filepath.Join(root, ".go-anything", "reports", "*", "v2"))
	if len(dirs) != 1 {
		t.Fatalf("want one v2 report folder, got %v", dirs)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "analysis.json")); err != nil {
		t.Fatalf("each version must keep analysis.json for the next comparison: %v", err)
	}
	html, err := os.ReadFile(filepath.Join(dirs[0], "analysis.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	if !strings.Contains(page, "since v1") && !strings.Contains(page, "Since v1") {
		t.Fatalf("the second report must compare with v1")
	}
	if !strings.Contains(page, "text") {
		t.Fatalf("the added package must be named in the report")
	}
}
