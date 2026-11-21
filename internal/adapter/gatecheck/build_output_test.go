package gatecheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCheckLeavesNoBinaryInASingleMainModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/solo\n\ngo 1.27.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := buildCheck(root); !got.Passed {
		t.Fatalf("build check failed: %s", got.Detail)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "go.mod" && e.Name() != "main.go" {
			t.Fatalf("the build check left %q in the module", e.Name())
		}
	}
}
