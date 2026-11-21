package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFinalizePassesOnlyWhenEverythingMatches(t *testing.T) {
	r := Report{Total: 3, Matched: 3}
	r.Finalize()
	if !r.Passed || len(r.Reasons) != 0 {
		t.Fatalf("clean report must pass: %+v", r)
	}
}

func TestFinalizeFailsOnMismatchStaleOrEmpty(t *testing.T) {
	cases := map[string]Report{
		"mismatch": {Total: 3, Matched: 2},
		"stale":    {Total: 3, Matched: 3, Stale: true},
		"empty":    {Total: 0, Matched: 0},
	}
	for name, r := range cases {
		r.Finalize()
		if r.Passed || len(r.Reasons) == 0 {
			t.Fatalf("%s must fail with a reason: %+v", name, r)
		}
	}
}

func TestFinalizeReasonsAreSpecific(t *testing.T) {
	r := Report{Total: 4, Matched: 3, Stale: true}
	r.Finalize()
	joined := strings.Join(r.Reasons, " | ")
	if !strings.Contains(joined, "1 of 4") || !strings.Contains(joined, "source changed") {
		t.Fatalf("reasons must name the mismatch count and the stale source: %s", joined)
	}
}

func TestMatchRate(t *testing.T) {
	if got := (Report{Total: 4, Matched: 3}).MatchRate(); got != 75 {
		t.Fatalf("want 75, got %v", got)
	}
	if got := (Report{}).MatchRate(); got != 0 {
		t.Fatalf("empty report rate must be 0, got %v", got)
	}
}

func TestMarkdownShowsVerdictAndMismatchItems(t *testing.T) {
	r := Report{
		Subject: "svc", Kind: "app-http", Total: 2, Matched: 1,
		Items: []ReportItem{
			{Name: "GET /ok", Passed: true},
			{Name: "GET /bad", Passed: false, Detail: "body differs"},
		},
		Diffs: []ReportDiff{{Case: "GET /bad", What: "body", Want: "a|b", Got: "c"}},
	}
	r.Finalize()
	md := r.Markdown()
	for _, want := range []string{"# Verification report: svc", "**Verdict: FAIL**", "**mismatch**", "GET /bad", `a\|b`, "## Differences"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestMarkdownPassHasNoFailureSection(t *testing.T) {
	r := Report{Subject: "svc", Kind: "unit", Total: 1, Matched: 1, Items: []ReportItem{{Name: "a", Passed: true}}}
	r.Finalize()
	md := r.Markdown()
	if !strings.Contains(md, "**Verdict: PASS**") {
		t.Fatalf("want PASS:\n%s", md)
	}
	if strings.Contains(md, "## Differences") {
		t.Fatal("a passing report with no diffs must not show a differences section")
	}
}

func TestJSONIsParseable(t *testing.T) {
	r := Report{Subject: "svc", Kind: "unit", Total: 1, Matched: 1}
	r.Finalize()
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("JSON must round-trip: %v", err)
	}
	if !back.Passed || back.Subject != "svc" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestMarkdownShowsInputRunAndVisibleWhitespace(t *testing.T) {
	r := Report{Subject: "svc", Kind: "unit", Total: 1,
		Items: []ReportItem{{Name: "c", Passed: false}},
		Diffs: []ReportDiff{{Case: "c", What: "output", Input: "49", Want: "100\n", Got: "100"}},
		Run:   &ReportRun{Rewrite: "python3 rew.py", Recording: "rec.json", GeneratedAt: "2025-01-02T03:04:05Z", ToolVersion: "9.9.9"}}
	r.Finalize()
	md := r.Markdown()
	for _, want := range []string{"| Case | Field | Input | Expected | Actual |", "| c | output | 49 | 100↵ | 100 |",
		"Rewrite: python3 rew.py", "Recording: rec.json", "go-anything 9.9.9", "Next steps:", "1. Fix the 1 mismatching"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestJSONRunAndInputAreOptional(t *testing.T) {
	b, _ := (Report{Subject: "svc", Diffs: []ReportDiff{{Case: "c"}}}).JSON()
	if strings.Contains(string(b), `"run"`) || strings.Contains(string(b), `"input"`) {
		t.Fatalf("unset run and input must be omitted:\n%s", b)
	}
}
