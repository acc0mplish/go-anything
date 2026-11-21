package usecase

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestUnitReportPassAndMismatch(t *testing.T) {
	v := Verdict{Subject: "f", Total: 3, Matched: 2, Names: []string{"c1", "c2", "c3"}, Mismatches: []Mismatch{
		{Case: "c2", Status: domain.StatusMismatch, Expected: "one", Got: "uno"},
	}}
	r := UnitReport(v, false, nil)
	if r.Passed {
		t.Fatal("a mismatch must fail the report")
	}
	if len(r.Items) != 3 || len(r.Diffs) != 1 {
		t.Fatalf("want 3 items and 1 diff, got %d items %d diffs", len(r.Items), len(r.Diffs))
	}
	if r.Diffs[0].Want != "one" || r.Diffs[0].Got != "uno" {
		t.Fatalf("diff must carry expected and actual: %+v", r.Diffs[0])
	}
	// Matched cases must keep their real names, not positions.
	names := map[string]bool{}
	for _, it := range r.Items {
		names[it.Name] = true
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		if !names[want] {
			t.Fatalf("case %q must be named in the report: %+v", want, r.Items)
		}
	}
}

func TestUnitReportStaleFailsEvenWhenAllMatch(t *testing.T) {
	v := Verdict{Subject: "f", Total: 2, Matched: 2, Names: []string{"a", "b"}}
	v.Stale = true
	r := UnitReport(v, true, nil)
	if r.Passed {
		t.Fatal("stale recording must fail even if every case matches")
	}
}

func TestUnitReportCarriesCoverage(t *testing.T) {
	cov := domain.Coverage{Statements: 10, Covered: 7, Percent: 70}
	r := UnitReport(Verdict{Subject: "f", Total: 1, Matched: 1, Names: []string{"a"}}, false, &cov)
	if r.Coverage == nil || *r.Coverage != 70 {
		t.Fatalf("coverage must reach the report: %+v", r.Coverage)
	}
}

func TestAppReportNamesEachMismatchedField(t *testing.T) {
	v := AppVerdict{Subject: "http://svc", Total: 2, Matched: 1, Names: []string{"hello", "other"}, Mismatches: []AppMismatch{
		{Case: "hello", What: "status", Want: "200", Got: "500"},
		{Case: "hello", What: "body", Want: "hi", Got: "HELLO"},
	}}
	r := AppReport(v, "app-http")
	if r.Passed || len(r.Items) != 2 || len(r.Diffs) != 2 {
		t.Fatalf("want failed report with 2 items and 2 diffs: %+v", r)
	}
	md := r.Markdown()
	if !strings.Contains(md, "status") || !strings.Contains(md, "body") {
		t.Fatalf("both differing fields must appear:\n%s", md)
	}
}

func TestReportDiffsCarryTheInput(t *testing.T) {
	v := Verdict{Subject: "f", Total: 1, Names: []string{"line-4"}, Inputs: map[string]string{"line-4": "49"},
		Mismatches: []Mismatch{{Case: "line-4", Status: domain.StatusMismatch, Expected: "100", Got: "101"}}}
	if r := UnitReport(v, true, nil); r.Diffs[0].Input != "49" {
		t.Fatalf("unit diff must carry the recorded input: %+v", r.Diffs[0])
	}
	av := AppVerdict{Subject: "s", Total: 1, Names: []string{"c"}, Inputs: map[string]string{"c": "GET /x"},
		Mismatches: []AppMismatch{{Case: "c", What: "status", Want: "200", Got: "500"}}}
	if r := AppReport(av, "app-http"); r.Diffs[0].Input != "GET /x" {
		t.Fatalf("app diff must carry the request: %+v", r.Diffs[0])
	}
}

func TestUnitReportShowsRewriteError(t *testing.T) {
	v := Verdict{Subject: "f", Total: 1, Names: []string{"c"},
		Mismatches: []Mismatch{{Case: "c", Status: domain.StatusError, Expected: "1", Err: errors.New("exit status 2")}}}
	if r := UnitReport(v, true, nil); r.Diffs[0].Got != "error: exit status 2" {
		t.Fatalf("a rewrite error must be the actual value: %+v", r.Diffs[0])
	}
}

func TestAppCaseInput(t *testing.T) {
	cases := map[string]domain.AppCase{
		"GET /a":                       {Kind: "http", Method: "GET", Path: "/a", RequestHeaders: map[string]string{"Authorization": "Bearer s"}},
		"POST /a\n\n{\"x\":1}":         {Kind: "http", Method: "POST", Path: "/a", Body: `{"x":1}`},
		"argv: add \"two words\" \"\"": {Kind: "cli", Argv: []string{"add", "two words", ""}},
		"argv: sum\nstdin:\n1\n2\n":    {Kind: "cli", Argv: []string{"sum"}, Stdin: "1\n2\n"},
	}
	for want, c := range cases {
		if got := AppCaseInput(c); got != want {
			t.Fatalf("AppCaseInput(%+v) = %q, want %q", c, got, want)
		}
	}
}

func TestVerifyAndReplayRecordInputs(t *testing.T) {
	rec := domain.AppRecording{Subject: "s", Cases: []domain.AppCase{{Name: "c", Kind: "http", Method: "GET", Path: "/x", Status: 200}}}
	v, err := AppReplay(rec, fixedRunner{domain.AppCase{Status: 500}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Inputs["c"] != "GET /x" {
		t.Fatalf("replay must record each case's input: %+v", v.Inputs)
	}
	v2, err := AppReplayFull(rec, fixedRunner{domain.AppCase{Status: 500}}, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Inputs["c"] != "GET /x" {
		t.Fatalf("full replay must record each case's input: %+v", v2.Inputs)
	}
}

type fixedRunner struct{ out domain.AppCase }

func (f fixedRunner) Do(domain.AppCase) (domain.AppCase, error) { return f.out, nil }
