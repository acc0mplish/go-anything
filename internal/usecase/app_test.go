package usecase

import (
	"errors"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// fakeRunner answers from a fixed table keyed by case name.
type fakeRunner map[string]domain.AppCase

func (f fakeRunner) Do(c domain.AppCase) (domain.AppCase, error) {
	ans, ok := f[c.Name]
	if !ok {
		return domain.AppCase{}, errors.New("unreachable")
	}
	return ans, nil
}

func httpCase(name string) domain.AppCase {
	return domain.AppCase{Name: name, Kind: "http", Method: "GET", Path: "/x"}
}

func TestAppRecordCapturesAnswers(t *testing.T) {
	answers := fakeRunner{"h": {Status: 200, ResponseBody: `{"ok":true}`}}
	rec, err := AppRecord("svc", []domain.AppCase{httpCase("h")}, answers)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Cases[0].Status != 200 || rec.Cases[0].ResponseBody != `{"ok":true}` {
		t.Fatalf("answer not recorded: %+v", rec.Cases[0])
	}
}

func TestAppReplayPassesOnSameAnswer(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/x", Status: 200, ResponseBody: "ok"},
	}}
	v, err := AppReplay(rec, fakeRunner{"h": {Status: 200, ResponseBody: "ok"}})
	if err != nil || !v.Passed() {
		t.Fatalf("same answer must pass: %+v err=%v", v, err)
	}
}

func TestAppReplayFlagsEachDifferingField(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/x", Status: 200, ResponseBody: "ok"},
	}}
	v, _ := AppReplay(rec, fakeRunner{"h": {Status: 500, ResponseBody: "boom"}})
	if v.Passed() {
		t.Fatal("different status and body must fail")
	}
	whats := map[string]bool{}
	for _, m := range v.Mismatches {
		whats[m.What] = true
	}
	if !whats["status"] || !whats["body"] {
		t.Fatalf("both fields must be reported: %+v", v.Mismatches)
	}
}

func TestAppReplayCliExitCodeAndTrailingNewline(t *testing.T) {
	rec := domain.AppRecording{Subject: "tool", Cases: []domain.AppCase{
		{Name: "c", Kind: "cli", Argv: []string{"tool"}, ExitCode: 0, Stdout: "hi\n"},
	}}
	// trailing newline differences are not drift
	if v, _ := AppReplay(rec, fakeRunner{"c": {ExitCode: 0, Stdout: "hi"}}); !v.Passed() {
		t.Fatalf("trailing newline must not count: %+v", v)
	}
	if v, _ := AppReplay(rec, fakeRunner{"c": {ExitCode: 1, Stdout: "hi"}}); v.Passed() {
		t.Fatal("different exit code must fail")
	}
}

func TestAppReplayRunErrorIsMismatchNotPass(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{httpCase("gone")}}
	rec.Cases[0].Status = 200
	v, err := AppReplay(rec, fakeRunner{}) // runner errors for unknown name
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed() || len(v.Mismatches) != 1 || v.Mismatches[0].What != "run" {
		t.Fatalf("a case that cannot run must be a mismatch: %+v", v)
	}
}

func TestAppReplayRejectsInvalidRecording(t *testing.T) {
	if _, err := AppReplay(domain.AppRecording{}, fakeRunner{}); err == nil {
		t.Fatal("empty recording must be rejected")
	}
}
