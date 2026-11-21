package cover

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewCollectorRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCollector(dir); err == nil {
		t.Fatal("a non-empty counter directory must be refused")
	}
}

func TestNewCollectorAcceptsEmptyDir(t *testing.T) {
	c, err := NewCollector(filepath.Join(t.TempDir(), "cov"))
	if err != nil || c.Dir == "" {
		t.Fatalf("empty dir must be accepted: %v", err)
	}
}

func TestEnvSetsGOCOVERDIR(t *testing.T) {
	c := Collector{Dir: "/x/y"}
	found := false
	for _, kv := range c.Env() {
		if kv == "GOCOVERDIR=/x/y" {
			found = true
		}
	}
	if !found {
		t.Fatal("GOCOVERDIR must be set for the run")
	}
}

func TestBuildRejectsEmptyPackage(t *testing.T) {
	if err := Build("", "/tmp/x"); err == nil {
		t.Fatal("empty package must be rejected")
	}
}
