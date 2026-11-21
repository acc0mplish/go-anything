package domain

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintStableAndDetectsEdit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n")
	f1, err := FingerprintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	f2, _ := FingerprintDir(dir)
	if f1 != f2 {
		t.Fatal("unchanged source must have an identical fingerprint")
	}
	writeFile(t, dir, "a.go", "package a // edited\n")
	f3, _ := FingerprintDir(dir)
	if !f1.Drifted(f3) {
		t.Fatal("an edit must be detected as drift")
	}
}

func TestFingerprintIgnoresNonGoAndTests(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n")
	f1, _ := FingerprintDir(dir)
	writeFile(t, dir, "README.md", "docs changed\n")
	f2, _ := FingerprintDir(dir)
	if f1.Drifted(f2) {
		t.Fatal("non-Go file changes must not count as drift")
	}
}

func TestFingerprintDetectsAddedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n")
	f1, _ := FingerprintDir(dir)
	writeFile(t, dir, "b.go", "package a\n")
	f2, _ := FingerprintDir(dir)
	if !f1.Drifted(f2) {
		t.Fatal("an added Go file must be detected as drift")
	}
}

func TestFingerprintSkipsVendorAndHidden(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n")
	f1, _ := FingerprintDir(dir)
	for _, sub := range []string{"vendor", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "x.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f2, _ := FingerprintDir(dir)
	if f1.Drifted(f2) {
		t.Fatal("vendor and hidden directories must not count")
	}
}
