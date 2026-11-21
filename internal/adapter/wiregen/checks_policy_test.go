package wiregen

import (
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/usecase"
)

func TestDefectNoteFollowsTheRunsPolicy(t *testing.T) {
	cases := []struct {
		policy, want, notWant string
	}{
		{string(usecase.PolicyDefect), "NOT recorded as expected behavior", "were recorded as behavior"},
		{string(usecase.PolicyExpected), "were recorded as behavior the rewrite must match", "NOT recorded"},
		{"", "do not name the defect policy", "This run used"},
	}
	for _, c := range cases {
		got := DefectNote(c.policy)
		if !strings.Contains(got, c.want) || strings.Contains(got, c.notWant) {
			t.Errorf("policy %q: note %q must say %q and not %q", c.policy, got, c.want, c.notWant)
		}
	}
}

func TestPageShowsThePolicyOfTheLastRound(t *testing.T) {
	c := newRoundCheck(1, 3, []string{"1"}, 2, 0, nil, 0, true)
	c.CleanStreak = 1
	c.DefectPolicy = string(usecase.PolicyExpected)
	r := BuildRefineReport("orders", []RoundCheck{c}, nil, "2026-01-01T00:00:00Z", 0)
	page, err := RenderRefineHTML(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "-defect-policy expected") {
		t.Fatalf("an expected-policy run must not be described with the default policy text")
	}
}

func TestSingleRoundRowIsNotBadgedConverged(t *testing.T) {
	c := newRoundCheck(1, 3, []string{"1"}, 2, 0, nil, 0, true)
	c.CleanStreak = 1
	page, err := RenderRefineHTML(BuildRefineReport("orders", []RoundCheck{c}, nil, "t", 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), `badge ok">converged`) {
		t.Fatalf("a single clean round must not be badged converged")
	}
	if !strings.Contains(string(page), "stopped after 1 round") {
		t.Fatalf("a single clean round must say the run stopped after one round")
	}
}

func TestConvergedBannerIsNotGreenWhenInputClassesWereNotTried(t *testing.T) {
	mk := func(round, streak int, missing []string) RoundCheck {
		c := newRoundCheck(round, uint64(round), []string{"1"}, 2, 0, nil, 0, streak >= 2)
		c.CleanStreak = streak
		c.MissingClasses = missing
		return c
	}
	full := BuildRefineReport("u", []RoundCheck{mk(1, 1, nil), mk(2, 2, nil)}, nil, "t", 0)
	if class, _, _, _ := refineBanner(full); class != "pass" {
		t.Fatalf("every class tried: want pass, got %s", class)
	}
	partial := BuildRefineReport("u", []RoundCheck{mk(1, 1, nil), mk(2, 2, []string{"signed", "overflow"})}, nil, "t", 0)
	class, verdict, detail, next := refineBanner(partial)
	if class != "warn" || !strings.Contains(verdict, "untried input classes") {
		t.Fatalf("missing classes must lower the banner: class %s verdict %q", class, verdict)
	}
	if !strings.Contains(detail, "signed, overflow") || len(next) == 0 || !strings.Contains(next[0], "-probes") {
		t.Fatalf("the banner must name the missing classes and the fix: %q %v", detail, next)
	}
}
