package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// packageModule writes a small pure-Go module with two binaries and returns its root.
func packageModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":            "module example.com/pkgcmd\n\ngo 1.22\n",
		"cmd/one/main.go":   "package main\n\nvar version = \"dev\"\n\nfunc main() { println(version) }\n",
		"cmd/two/main.go":   "package main\n\nfunc main() {}\n",
		"internal/lib/l.go": "package lib\n",
	}
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	if err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPackageDryRunWritesNothing(t *testing.T) {
	root := packageModule(t)
	before := countFiles(t, root)
	var out bytes.Buffer
	if err := cmdPackage([]string{root, "-dry-run"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if after := countFiles(t, root); after != before {
		t.Fatalf("dry run wrote files: %d -> %d", before, after)
	}
	s := out.String()
	for _, want := range []string{
		"module  example.com/pkgcmd", "example.com/pkgcmd/cmd/one", "example.com/pkgcmd/cmd/two",
		"cgo     no", "targets linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64",
		"create .github/workflows/release.yml", "----- docs/INSTALL.md -----",
		"go install example.com/pkgcmd/cmd/one@latest", "cmd/two: -X main.version has no effect", "dry run: nothing written.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q\n%s", want, s)
		}
	}
}

func TestPackageWritesThenKeeps(t *testing.T) {
	root := packageModule(t)
	var out bytes.Buffer
	if err := cmdPackage([]string{"-targets", "linux/amd64,windows/arm64", root}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, p := range []string{".github/workflows/release.yml", "scripts/build-all.sh", "docs/INSTALL.md"} {
		if !strings.Contains(out.String(), "created "+p) {
			t.Errorf("not created: %s\n%s", p, out.String())
		}
	}
	wf, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	if err != nil || !strings.Contains(string(wf), "goarch: arm64") || strings.Contains(string(wf), "goos: darwin") {
		t.Fatalf("workflow ignores -targets: %v\n%s", err, wf)
	}
	out.Reset()
	if err := cmdPackage([]string{root}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "kept ") != 3 || strings.Contains(out.String(), "created ") {
		t.Fatalf("second run:\n%s", out.String())
	}
}

func TestPackageCheckHostTarget(t *testing.T) {
	root := packageModule(t)
	before := countFiles(t, root)
	var out bytes.Buffer
	host := runtime.GOOS + "/" + runtime.GOARCH
	if err := cmdPackage([]string{root, "-check", "-targets", host}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ok     "+host) || !strings.Contains(out.String(), "1 of 1 targets built") {
		t.Fatalf("output:\n%s", out.String())
	}
	if after := countFiles(t, root); after != before {
		t.Fatalf("-check wrote into the module: %d -> %d", before, after)
	}
}

func TestPackageArgErrors(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{
		{},
		{"-targets", "linux"},
		{"a", "b"},
		{"-nope"},
	} {
		if err := cmdPackage(args, &out); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	if err := cmdPackage([]string{t.TempDir()}, &out); err == nil || !strings.Contains(err.Error(), "not a module root") {
		t.Errorf("non-module dir: %v", err)
	}
	out.Reset()
	if err := cmdPackage([]string{"-h"}, &out); err != nil || !strings.Contains(out.String(), "usage: go-anything package") {
		t.Errorf("-h: %v %q", err, out.String())
	}
}
