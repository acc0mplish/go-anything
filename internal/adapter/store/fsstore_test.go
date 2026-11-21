package store

import (
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rec.json")
	in := domain.Recording{Subject: "s", Cases: []domain.Case{{Name: "a", Input: "1", Output: "one"}}}
	if err := (FileStore{}).Save(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := (FileStore{}).Load(p)
	if err != nil || out.Subject != "s" || out.Cases[0].Output != "one" {
		t.Fatalf("round trip failed: %+v err=%v", out, err)
	}
}

func TestSaveRefusesEmpty(t *testing.T) {
	if err := (FileStore{}).Save(filepath.Join(t.TempDir(), "x.json"), domain.Recording{}); err == nil {
		t.Fatal("empty recording must not be saved")
	}
}
