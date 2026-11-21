package usecase

import (
	"strings"
	"testing"
)

func TestLiftReadyWhenEverythingPasses(t *testing.T) {
	in := LiftInput{ReplayTotal: 3, ReplayMatched: 3, GatePassed: true,
		Units: []LiftUnit{{Name: "a", Passed: true}}}
	if b := LiftBlockers(in); len(b) != 0 {
		t.Fatalf("everything passed, nothing may block: %v", b)
	}
}

func TestLiftReportsEveryBlocker(t *testing.T) {
	in := LiftInput{ReplayTotal: 3, ReplayMatched: 2, GatePassed: false,
		Units: []LiftUnit{{Name: "stale-one", Stale: true}, {Name: "bad-one", Passed: false, Mismatch: 2}}}
	b := LiftBlockers(in)
	joined := strings.Join(b, " | ")
	for _, want := range []string{"replay: 1 of 3", "stale-one", "bad-one", "gate"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("blocker %q missing from %v", want, b)
		}
	}
	if len(b) != 4 {
		t.Fatalf("want 4 blockers, got %d: %v", len(b), b)
	}
}

func TestLiftRefusesWithoutAnyReplay(t *testing.T) {
	b := LiftBlockers(LiftInput{GatePassed: true})
	if len(b) == 0 || !strings.Contains(b[0], "no recorded app cases") {
		t.Fatalf("no replay at all must block lift: %v", b)
	}
}
