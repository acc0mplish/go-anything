package usecase

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNextVersionStartsAtOneAndIncrements(t *testing.T) {
	base := t.TempDir()
	n, err := NextVersion(base)
	if err != nil || n != 1 {
		t.Fatalf("empty subject must start at v1: n=%d err=%v", n, err)
	}
	for _, d := range []string{"v1", "v2"} {
		os.Mkdir(filepath.Join(base, d), 0o700)
	}
	n, _ = NextVersion(base)
	if n != 3 {
		t.Fatalf("after v1,v2 the next must be v3, got %d", n)
	}
}

func TestNextVersionIgnoresNonVersionNames(t *testing.T) {
	base := t.TempDir()
	os.Mkdir(filepath.Join(base, "v5"), 0o700)
	os.Mkdir(filepath.Join(base, "notes"), 0o700)
	os.WriteFile(filepath.Join(base, "v9"), []byte("a file, not a version folder"), 0o600)
	n, _ := NextVersion(base)
	if n != 6 {
		t.Fatalf("a file named v9 must not count; want 6, got %d", n)
	}
}

func TestNextVersionUsesNumericOrderNotLexical(t *testing.T) {
	base := t.TempDir()
	for _, d := range []string{"v2", "v10"} {
		os.Mkdir(filepath.Join(base, d), 0o700)
	}
	n, _ := NextVersion(base)
	if n != 11 {
		t.Fatalf("v10 is newer than v2; next must be v11, got %d", n)
	}
}

func TestVersionDirNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	dir, err := VersionDir(root, "svc", 1)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "report.html")
	os.WriteFile(marker, []byte("keep me"), 0o600)
	if _, err := VersionDir(root, "svc", 1); err == nil {
		t.Fatal("reusing an existing version must be refused")
	}
	if b, _ := os.ReadFile(marker); string(b) != "keep me" {
		t.Fatal("the previous version's file must be untouched")
	}
}

func TestVersionDirRejectsBadNumber(t *testing.T) {
	if _, err := VersionDir(t.TempDir(), "svc", 0); err == nil {
		t.Fatal("version 0 must be rejected")
	}
}
