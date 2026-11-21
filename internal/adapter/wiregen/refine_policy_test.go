package wiregen

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// The original crashes on "007". Under the default policy that crash is a defect and is not recorded.
// Under the expected policy the crash is recorded as the behavior to match.
func runPolicy(t *testing.T, policy usecase.DefectPolicy) domain.AppRecording {
	t.Helper()
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.sh")
	os.WriteFile(orig, []byte(`#!/bin/sh
if [ "$1" = orders ] && [ "$2" = 007 ]; then echo 'Traceback (most recent call last):' >&2; exit 1; fi
if [ "$1" = orders ] && [ -n "$2" ]; then printf 'order:%s\n' "$2"; exit 0; fi
exit 3
`), 0o755)
	root := newModule(t)
	if _, err := Gen(root, "orders", ""); err != nil {
		t.Fatal(err)
	}
	casesPath := filepath.Join(dir, "app.json")
	store.SaveApp(casesPath, domain.AppRecording{Subject: "orig", Cases: []domain.AppCase{
		{Name: "one", Kind: "cli", Argv: []string{"orders", "1"}, Stdout: "order:1\n"},
	}})
	// No agent: the run stops after round 1, but the recording it wrote is what this test reads.
	Refine(RefineOptions{Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 1, Probes: 20, MaxNew: 50, Seed: 2, Policy: policy, Log: io.Discard})
	rec, err := store.LoadApp(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func hasCrashCase(rec domain.AppRecording) bool {
	for _, c := range rec.Cases {
		if len(c.Argv) == 2 && c.Argv[1] == "007" {
			return true
		}
	}
	return false
}

func TestDefaultPolicyDoesNotRecordCrash(t *testing.T) {
	if hasCrashCase(runPolicy(t, usecase.PolicyDefect)) {
		t.Fatal("the default policy must report the crash as a defect, not record it")
	}
}

func TestExpectedPolicyRecordsCrash(t *testing.T) {
	if !hasCrashCase(runPolicy(t, usecase.PolicyExpected)) {
		t.Fatal("the expected policy must record the crash as the behavior to match")
	}
}
