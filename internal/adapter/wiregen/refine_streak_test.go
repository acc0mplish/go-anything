package wiregen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// readRoundCheck loads one saved round record.
func readRoundCheck(t *testing.T, path string) RoundCheck {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c RoundCheck
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func roundRecordPath(root string, round int) string {
	return filepath.Join(root, ".go-anything", "checks", "orders", fmt.Sprintf("round-%02d.json", round))
}

func TestConvergenceRule(t *testing.T) {
	cases := []struct {
		name          string
		streak        int
		rounds        int
		wantConverged bool
		wantSingle    bool
	}{
		{"streak 0 is not converged", 0, 2, false, false},
		{"streak 1 is not converged", 1, 2, false, false},
		{"streak 2 is converged", 2, 2, true, false},
		{"streak 3 is converged", 3, 4, true, false},
		{"one round: a clean round converges, with the single-round warning", 1, 1, true, true},
		{"one round: no clean round is not converged", 0, 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotConverged, gotSingle := convergence(tc.streak, tc.rounds)
			if gotConverged != tc.wantConverged || gotSingle != tc.wantSingle {
				t.Fatalf("convergence(%d, %d): want converged=%v single=%v, got converged=%v single=%v",
					tc.streak, tc.rounds, tc.wantConverged, tc.wantSingle, gotConverged, gotSingle)
			}
		})
	}
}

// TestCleanStreakReplaysTheLoop replays the round loop on a list of clean and dirty rounds, using the same
// two functions the loop uses. It checks the clean_streak recorded for each round and the round that converges.
func TestCleanStreakReplaysTheLoop(t *testing.T) {
	cases := []struct {
		name        string
		clean       []bool
		rounds      int
		wantStreaks []int
		wantStop    int // round where the run converged; 0 if it never did
		wantWarn    bool
	}{
		{"two clean rounds converge on round 2", []bool{true, true}, 4, []int{1, 2}, 2, false},
		{"a disagreement resets the streak", []bool{true, false, true, true}, 4, []int{1, 0, 1, 2}, 4, false},
		{"a disagreement just before the second clean round restarts the count", []bool{true, false, true}, 3, []int{1, 0, 1}, 0, false},
		{"one clean round alone does not converge with 2 rounds", []bool{true, false}, 2, []int{1, 0}, 0, false},
		{"a dirty first round does not stop a later pair of clean rounds", []bool{false, true, true}, 3, []int{0, 1, 2}, 3, false},
		{"a clean streak past 2 stops at round 2", []bool{true, true, true}, 4, []int{1, 2}, 2, false},
		{"one round: a clean round converges on round 1 with the warning", []bool{true}, 1, []int{1}, 1, true},
		{"one round: a dirty round does not converge", []bool{false}, 1, []int{0}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streak := 0
			var gotStreaks []int
			stop, warn := 0, false
			for i, clean := range tc.clean {
				streak = nextCleanStreak(streak, clean)
				gotStreaks = append(gotStreaks, streak)
				if converged, single := convergence(streak, tc.rounds); converged {
					stop, warn = i+1, single
					break
				}
			}
			if !reflect.DeepEqual(gotStreaks, tc.wantStreaks) {
				t.Errorf("clean_streak per round: want %v, got %v", tc.wantStreaks, gotStreaks)
			}
			if stop != tc.wantStop || warn != tc.wantWarn {
				t.Errorf("converged at round %d (warn=%v): want round %d (warn=%v)", stop, warn, tc.wantStop, tc.wantWarn)
			}
		})
	}
}

func TestSeedsOfNamesTheCleanRounds(t *testing.T) {
	cases := []struct {
		round, n int
		want     string
	}{
		{1, 1, "seed 41"},
		{2, 2, "seeds 41 and 42"},
		{3, 3, "seeds 41 to 43"},
	}
	for _, tc := range cases {
		if got := seedsOf(40, tc.round, tc.n); got != tc.want {
			t.Errorf("seedsOf(40, %d, %d) = %q, want %q", tc.round, tc.n, got, tc.want)
		}
	}
}

// newRefineFixture writes the original script, a fix script for the agent, a module with the placeholder
// rewrite, and the recording that refine works from.
func newRefineFixture(t *testing.T) (root, orig, fix, casesPath string) {
	t.Helper()
	dir := t.TempDir()
	orig = filepath.Join(dir, "orig.sh")
	fix = filepath.Join(dir, "fix.sh")
	if err := os.WriteFile(orig, []byte(origScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fix, []byte(fixScript), 0o644); err != nil {
		t.Fatal(err)
	}
	root = newModule(t)
	if _, err := Gen(root, "orders", ""); err != nil {
		t.Fatal(err)
	}
	casesPath = filepath.Join(dir, "app.json")
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
	return root, orig, fix, casesPath
}

// correctRewriteModule is newRefineFixture with the rewrite already fixed, so every round is clean.
func correctRewriteModule(t *testing.T) (root, orig, casesPath string) {
	t.Helper()
	var fix string
	root, orig, fix, casesPath = newRefineFixture(t)
	cmd := exec.Command("sh", fix)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write the correct rewrite: %v\n%s", err, out)
	}
	return root, orig, casesPath
}

// A rewrite that is already correct converges only after two clean rounds, and the agent is never asked.
func TestRefineConvergesOnlyAfterTwoCleanRounds(t *testing.T) {
	root, orig, casesPath := correctRewriteModule(t)
	marker := filepath.Join(t.TempDir(), "agent-ran")
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 4, Probes: 8, MaxNew: 20, Seed: 1, Agent: "touch " + marker, Attempts: 1, Log: &logBuf,
	})
	if err != nil || !ok {
		t.Fatalf("a correct rewrite must converge: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a clean round must not ask the agent to fix anything")
	}
	if !strings.Contains(logBuf.String(), "converged after 2 consecutive clean rounds (seeds 2 and 3)") {
		t.Fatalf("the converged line must name the clean rounds and their seeds:\n%s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "not a convergence proof") {
		t.Fatalf("a two-round run must not print the single-round warning:\n%s", logBuf.String())
	}
	first := readRoundCheck(t, roundRecordPath(root, 1))
	if first.Converged || first.CleanStreak != 1 || first.Disagreements != 0 {
		t.Fatalf("round 1: want a clean, not converged round with clean_streak 1, got converged=%v streak=%d disagreements=%d",
			first.Converged, first.CleanStreak, first.Disagreements)
	}
	second := readRoundCheck(t, roundRecordPath(root, 2))
	if !second.Converged || second.CleanStreak != 2 {
		t.Fatalf("round 2: want converged with clean_streak 2, got converged=%v streak=%d", second.Converged, second.CleanStreak)
	}
}

// With one round, a clean round still succeeds, and the log says one round is not a convergence proof.
func TestRefineSingleRoundConvergesWithWarning(t *testing.T) {
	root, orig, casesPath := correctRewriteModule(t)
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 1, Probes: 8, MaxNew: 20, Seed: 1, Log: &logBuf,
	})
	if err != nil || !ok {
		t.Fatalf("one clean round must still succeed with rounds=1: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "not a convergence proof") {
		t.Fatalf("rounds=1 must warn that one round is not a convergence proof:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "1 clean round (seed 2), not yet converged") {
		t.Fatalf("the converged line must say one clean round and its seed:\n%s", logBuf.String())
	}
	c := readRoundCheck(t, roundRecordPath(root, 1))
	if !c.Converged || c.CleanStreak != 1 {
		t.Fatalf("round 1: want converged with clean_streak 1, got converged=%v streak=%d", c.Converged, c.CleanStreak)
	}
}

// A disagreement in round 1 resets the streak. The agent fixes the rewrite, and two clean rounds follow.
func TestRefineDisagreementResetsCleanStreak(t *testing.T) {
	root, orig, fix, casesPath := newRefineFixture(t)
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 4, Probes: 8, MaxNew: 20, Seed: 3, Agent: "sh " + fix, Attempts: 1, Log: &logBuf,
	})
	if err != nil || !ok {
		t.Fatalf("refine must converge once the agent fixes the rewrite: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	r1 := readRoundCheck(t, roundRecordPath(root, 1))
	if r1.Disagreements == 0 || r1.CleanStreak != 0 || r1.Converged {
		t.Fatalf("round 1 disagrees: want clean_streak 0 and not converged, got disagreements=%d streak=%d converged=%v",
			r1.Disagreements, r1.CleanStreak, r1.Converged)
	}
	r2 := readRoundCheck(t, roundRecordPath(root, 2))
	if r2.Disagreements != 0 || r2.CleanStreak != 1 || r2.Converged {
		t.Fatalf("round 2 is the first clean round: want clean_streak 1, got disagreements=%d streak=%d converged=%v",
			r2.Disagreements, r2.CleanStreak, r2.Converged)
	}
	r3 := readRoundCheck(t, roundRecordPath(root, 3))
	if r3.Disagreements != 0 || r3.CleanStreak != 2 || !r3.Converged {
		t.Fatalf("round 3 is the second clean round: want converged with clean_streak 2, got disagreements=%d streak=%d converged=%v",
			r3.Disagreements, r3.CleanStreak, r3.Converged)
	}
}
