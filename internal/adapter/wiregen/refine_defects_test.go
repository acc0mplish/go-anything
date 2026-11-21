package wiregen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// crashOrigScript stands in for an original that crashes only on the argument form "orders 007". It
// prints a Python-style traceback on stderr and exits 1, as a real crash would. Every other input is answered.
const crashOrigScript = `#!/bin/sh
if [ "$1" = orders ]; then
  if [ -n "$2" ]; then
    if [ "$2" = 007 ]; then
      echo "Traceback (most recent call last):" >&2
      echo '  File "orders.py", line 4, in <module>' >&2
      echo "ValueError: order 007 cannot be parsed" >&2
      exit 1
    fi
    printf 'order:%s\n' "$2"
  else
    read -r x
    printf 'order:%s\n' "$x"
  fi
  exit 0
fi
echo "usage: orig orders <id>" >&2
exit 1
`

// TestRefineRecordsOriginalDefectOnce checks that a crashing request is listed once in the
// defects file even when the loop runs more than one round. The agent is "true", so nothing is fixed and
// Refine must stop with an error instead of looping.
// This run stops after round 1 (the do-nothing agent leaves the placeholder failing), so it checks one
// defect in one round. The rule across rounds is tested directly in usecase.DefectLog.
func TestRefineRecordsOriginalDefectOnce(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.sh")
	if err := os.WriteFile(orig, []byte(crashOrigScript), 0o755); err != nil {
		t.Fatal(err)
	}
	root := newModule(t)
	if _, err := Gen(root, "orders", ""); err != nil {
		t.Fatal(err)
	}
	casesPath := filepath.Join(dir, "app.json")
	rec := domain.AppRecording{Subject: "orig", Cases: []domain.AppCase{
		{Name: "one", Kind: "cli", Argv: []string{"orders", "1"}, Stdout: "order:1\n"},
	}}
	if err := store.SaveApp(casesPath, rec); err != nil {
		t.Fatal(err)
	}
	data, err := usecase.CLICasesTest("orders", usecase.CasesForCommand(rec, "orders"), domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCases(root, "orders", data); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	// "007" is the 13th fixed boundary value, so 16 probes per round are needed for every round to try it.
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 16, MaxNew: 20, Seed: 1, Agent: "true", Attempts: 1, Log: &log,
	})
	if ok || err == nil {
		t.Fatalf("with an agent that fixes nothing, refine must return an error: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(log.String(), "round 1: ") || !strings.Contains(log.String(), "1 original defect(s) found") {
		t.Fatalf("round 1 must report the crashing request as one original defect; log:\n%s", log.String())
	}

	raw, err := os.ReadFile(casesPath + ".defects.json")
	if err != nil {
		t.Fatalf("the defects file must be written next to the recording: %v", err)
	}
	var defects []usecase.OriginalDefect
	if err := json.Unmarshal(raw, &defects); err != nil {
		t.Fatalf("the defects file must be valid JSON: %v", err)
	}
	// describeRequest writes the argument list and stdin of a command-line probe.
	const crashing = `args=["orders" "007"] stdin=""`
	count := 0
	for _, d := range defects {
		if d.Request == crashing {
			count++
		}
	}
	if count != 1 || len(defects) != 1 {
		t.Fatalf("the crashing request must be listed exactly once: %d matching, %d entries in all: %+v", count, len(defects), defects)
	}
	if !strings.Contains(defects[0].Evidence, "exit 1") || !strings.Contains(defects[0].Evidence, "ValueError") {
		t.Fatalf("the defect must carry the original's exit code and last stderr line: %q", defects[0].Evidence)
	}
}
