package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shadowGenModule(t *testing.T) string {
	t.Helper()
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	files := map[string]string{
		"go.mod":        "module example.com/sg\n\ngo 1.22\n",
		"old/old.go":    "package old\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Each(f func(int)) { f(1) }\n\nfunc Sum(xs []int) int { return len(xs) }\n\nfunc Name(n int) string { return \"x\" }\n",
		"neu/neu.go":    "package neu\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Each(f func(int)) { f(1) }\n\nfunc Sum(xs []int) int { return len(xs) }\n\nfunc Name(n uint) string { return \"x\" }\n",
		"cmd/x/main.go": "package main\n\nfunc main() {}\n",
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestShadowGenCmdDryRun(t *testing.T) {
	root := shadowGenModule(t)
	var out bytes.Buffer
	err := cmdShadowGen([]string{"-module-root", root, "-original", "example.com/sg/old", "-rewrite", "example.com/sg/neu", "-dry-run"}, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	t.Log(got)
	for _, want := range []string{
		"mode: GA_SHADOW_OLD=old|shadow|new",
		"shadowed: Add\n",
		"shadowed: Sum\n",
		"not shadowed: Each: parameter f holds a func",
		"not shadowed: Name: signature mismatch",
		"risk: Sum: parameter xs is a slice",
		"dry run: nothing written",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "shadow")); !os.IsNotExist(err) {
		t.Error("dry run wrote files")
	}
}

func TestShadowGenCmdWrites(t *testing.T) {
	root := shadowGenModule(t)
	dir := filepath.Join(root, "internal", "oldshadow")
	var out bytes.Buffer
	err := cmdShadowGen([]string{"-module-root", root, "-original", "example.com/sg/old", "-rewrite", "example.com/sg/neu", "-out", dir}, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "wrote: "+filepath.Join(dir, "shadow.go")) {
		t.Errorf("output: %s", out.String())
	}
	// A second run must refuse, not overwrite.
	out.Reset()
	err = cmdShadowGen([]string{"-module-root", root, "-original", "example.com/sg/old", "-rewrite", "example.com/sg/neu", "-out", dir}, &out)
	if err == nil || !strings.Contains(err.Error(), "already has content") {
		t.Fatalf("second run must refuse: %v", err)
	}
}

func TestShadowGenCmdNeedsPackages(t *testing.T) {
	if err := cmdShadowGen([]string{"-original", "x"}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing -rewrite must fail")
	}
}
