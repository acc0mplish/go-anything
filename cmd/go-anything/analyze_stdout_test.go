package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeStdoutIsOneJSONDocument(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":       "module example.com/m\n\ngo 1.27.4\n",
		"calc/calc.go": "package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n",
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
	var out bytes.Buffer
	if err := cmdAnalyze([]string{root}, &out); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout must be a single JSON document so it can be piped: %v\n%s", err, out.String())
	}
	if doc["module"] != "example.com/m" {
		t.Fatalf("module field: %v", doc["module"])
	}
}
