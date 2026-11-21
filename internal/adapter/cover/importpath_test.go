package cover

import (
	"path/filepath"
	"testing"
)

// The go-anything module is the fixture: it is always a module we can resolve from here.
func TestImportPathResolvesRelativeToDir(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportPath(root, "./internal/domain")
	if err != nil {
		t.Fatal(err)
	}
	const want = "github.com/go-anything/go-anything/internal/domain"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestImportPathFailsForMissingPackage(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	if _, err := ImportPath(root, "./does/not/exist"); err == nil {
		t.Fatal("a missing package must be an error, not a guessed path")
	}
}
