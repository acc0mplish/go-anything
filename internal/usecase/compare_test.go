package usecase

import (
	"math"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func jsonHTTPRecording(body string) domain.AppRecording {
	return domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/x", Status: 200, ResponseBody: body},
	}}
}

func TestAppReplayJSONBodyIgnoresSpacingAndKeyOrder(t *testing.T) {
	rec := jsonHTTPRecording(`{"id": 1, "total": 100}`)
	runner := fakeRunner{"h": {Status: 200, ResponseBody: `{"total":100,"id":1}`}}

	exact, err := AppReplayFull(rec, runner, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exact.Passed() {
		t.Fatal("exact comparison must fail on different spacing and key order")
	}

	sem, err := AppReplayFull(rec, runner, ReplayOptions{Compare: domain.CompareOptions{JSONBody: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !sem.Passed() {
		t.Fatalf("JSON semantic comparison must pass: %+v", sem.Mismatches)
	}
	if !sem.Compare.JSONBody {
		t.Fatal("verdict does not carry the comparison used")
	}
}

func TestAppReplayJSONBodyNamesFirstDifferingPath(t *testing.T) {
	rec := jsonHTTPRecording(`{"items":[{"price":1},{"price":2},{"price":1.5}]}`)
	runner := fakeRunner{"h": {Status: 200, ResponseBody: `{"items":[{"price":1},{"price":2},{"price":1.50001}]}`}}
	v, err := AppReplayFull(rec, runner, ReplayOptions{Compare: domain.CompareOptions{JSONBody: true}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed() || len(v.Mismatches) != 1 {
		t.Fatalf("want one mismatch, got %+v", v.Mismatches)
	}
	if m := v.Mismatches[0]; m.What != "body" || m.Detail != "$.items[2].price: want 1.5 got 1.50001" {
		t.Fatalf("mismatch = %+v", m)
	}
	r := AppReport(v, "app-http")
	if !strings.Contains(r.Items[0].Detail, "$.items[2].price") {
		t.Fatalf("report item does not name the path: %q", r.Items[0].Detail)
	}
}

func TestAppReplayUnstableBodyStillSkippedWithJSONBody(t *testing.T) {
	rec := jsonHTTPRecording(`{"t":1}`)
	rec.Cases[0].Unstable = []string{"body"}
	v, _ := AppReplayFull(rec, fakeRunner{"h": {Status: 200, ResponseBody: `{"t":2}`}}, ReplayOptions{Compare: domain.CompareOptions{JSONBody: true}})
	if !v.Passed() {
		t.Fatalf("unstable body must not be compared: %+v", v.Mismatches)
	}
}

func TestAppReplayCLIStdoutTolerance(t *testing.T) {
	rec := domain.AppRecording{Subject: "cli", Cases: []domain.AppCase{
		{Name: "c", Kind: "cli", Argv: []string{"sum"}, Stdout: "0.3\n"},
	}}
	runner := fakeRunner{"c": {Stdout: "0.30000000000000004\n"}}
	if v, _ := AppReplayFull(rec, runner, ReplayOptions{Compare: domain.CompareOptions{FloatTolerance: 1e-9}}); !v.Passed() {
		t.Fatalf("within tolerance must pass: %+v", v.Mismatches)
	}
	v, _ := AppReplayFull(rec, runner, ReplayOptions{})
	if v.Passed() || v.Mismatches[0].Detail == "" {
		t.Fatalf("exact must fail with a detail: %+v", v.Mismatches)
	}
}

func TestAppReplayRejectsInvalidTolerance(t *testing.T) {
	rec := jsonHTTPRecording(`1`)
	for _, tol := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := AppReplayFull(rec, fakeRunner{"h": {Status: 200, ResponseBody: "1"}}, ReplayOptions{Compare: domain.CompareOptions{FloatTolerance: tol}}); err == nil {
			t.Fatalf("tolerance %v accepted", tol)
		}
	}
}

func TestCompareAnswersWithZeroOptionsIsExact(t *testing.T) {
	want := domain.AppCase{Name: "h", Kind: "http", Status: 200, ResponseBody: `{"a": 1}`}
	got := domain.AppCase{Status: 200, ResponseBody: `{"a":1}`}
	if len(CompareAnswersWith(want, got, domain.CompareOptions{})) != len(CompareAnswers(want, got)) {
		t.Fatal("zero options must behave like CompareAnswers")
	}
	if len(CompareAnswersWith(want, got, domain.CompareOptions{JSONBody: true})) != 0 {
		t.Fatal("JSON semantic must accept spacing")
	}
}

func TestVerifyWithJSONOutput(t *testing.T) {
	rec := domain.Recording{Subject: "u", Cases: []domain.Case{
		{Name: "a", Input: "in", Output: `{"n": 1, "s": "x"}` + "\n"},
	}}
	sub := fakeSubject{"in": `{"s":"x","n":1.0}`}
	if v, err := VerifyWith(rec, sub, domain.CompareOptions{}); err != nil || v.Passed() {
		t.Fatalf("exact must fail: %v %+v", err, v)
	}
	v, err := VerifyWith(rec, sub, domain.CompareOptions{JSONBody: true})
	if err != nil || !v.Passed() {
		t.Fatalf("JSON semantic must pass (1 and 1.0 are the same decimal): %v %+v", err, v.Mismatches)
	}
	bad := fakeSubject{"in": `{"s":"x","n":2}`}
	v, _ = VerifyWith(rec, bad, domain.CompareOptions{JSONBody: true})
	if v.Passed() || v.Mismatches[0].Detail != "$.n: want 1 got 2" {
		t.Fatalf("want a path detail, got %+v", v.Mismatches)
	}
	if r := UnitReport(v, false, nil); !strings.Contains(r.Items[0].Detail, "$.n: want 1 got 2") {
		t.Fatalf("unit report item = %q", r.Items[0].Detail)
	}
	if _, err := VerifyWith(rec, sub, domain.CompareOptions{FloatTolerance: -1}); err == nil {
		t.Fatal("negative tolerance accepted")
	}
}

func TestSelectCase(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{httpCase("a"), httpCase("b")}}
	one, err := SelectCase(rec, "b")
	if err != nil || len(one.Cases) != 1 || one.Cases[0].Name != "b" || one.Subject != "svc" {
		t.Fatalf("SelectCase = %+v, %v", one, err)
	}
	_, err = SelectCase(rec, "zz")
	if err == nil || !strings.Contains(err.Error(), `"a", "b"`) || !strings.Contains(err.Error(), `"zz"`) {
		t.Fatalf("unknown case error must list available cases: %v", err)
	}
}

func TestJudgeProbeWithJSONBody(t *testing.T) {
	want := domain.AppCase{Name: "p", Kind: "http", Status: 200, ResponseBody: `{"id": 1}`}
	got := domain.AppCase{Kind: "http", Status: 200, ResponseBody: `{"id":1}`}
	if out, _ := JudgeProbe(want, got, false); out != ProbeDisagree {
		t.Fatalf("exact probe judgment = %v, want disagree", out)
	}
	if out, _ := JudgeProbeWith(want, got, false, domain.CompareOptions{JSONBody: true}); out != ProbeAgree {
		t.Fatalf("JSON semantic probe judgment = %v, want agree", out)
	}
}
