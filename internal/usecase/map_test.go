package usecase

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func stubLoad(recs map[string]domain.Recording) func(string) (domain.Recording, error) {
	return func(p string) (domain.Recording, error) {
		r, ok := recs[p]
		if !ok {
			return domain.Recording{}, errors.New("no such recording")
		}
		return r, nil
	}
}

func srcDir(t *testing.T, body string) (string, domain.Fingerprint) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fp, err := domain.FingerprintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, fp
}

func TestMapReportsEachStateAndDoesNotStopEarly(t *testing.T) {
	okDir, okFP := srcDir(t, "package a\n")
	driftDir, driftFP := srcDir(t, "package a\n")
	// change the drifted source after fingerprinting
	if err := os.WriteFile(filepath.Join(driftDir, "a.go"), []byte("package a // changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recs := map[string]domain.Recording{
		"ok.json":    {Subject: "ok", Source: &okFP, Cases: []domain.Case{{Name: "a"}}},
		"drift.json": {Subject: "drift", Source: &driftFP, Cases: []domain.Case{{Name: "a"}}},
		"nofp.json":  {Subject: "nofp", Cases: []domain.Case{{Name: "a"}}},
	}
	targets := []Target{
		{Recording: "ok.json", Source: okDir},
		{Recording: "drift.json", Source: driftDir},
		{Recording: "nofp.json", Source: okDir},
		{Recording: "missing.json", Source: okDir},
		{Recording: "ok.json", Source: filepath.Join(okDir, "nope")},
	}
	got := Map(targets, stubLoad(recs))
	want := []Status{StatusOK, StatusDrifted, StatusNoFingerprint, StatusError, StatusMissingSource}
	if len(got) != len(want) {
		t.Fatalf("want %d units, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i].Status != w {
			t.Fatalf("unit %d: want %s, got %s (%s)", i, w, got[i].Status, got[i].Detail)
		}
	}
	if Problems(got) != 4 {
		t.Fatalf("want 4 problems, got %d", Problems(got))
	}
}
