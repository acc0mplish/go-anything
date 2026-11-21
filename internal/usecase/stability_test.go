package usecase

import (
	"slices"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// seqRunner answers the same case with a sequence of answers, one per call.
type seqRunner struct {
	answers []domain.AppCase
	i       int
}

func (s *seqRunner) Do(c domain.AppCase) (domain.AppCase, error) {
	a := s.answers[s.i%len(s.answers)]
	s.i++
	return a, nil
}

func httpAnswer(status int, body string) domain.AppCase {
	return domain.AppCase{Kind: "http", Status: status, ResponseBody: body}
}

func TestRecordMarksFieldThatVariesBetweenRuns(t *testing.T) {
	// the body changes every run (a timestamp, say); the status does not
	runner := &seqRunner{answers: []domain.AppCase{httpAnswer(200, "t1"), httpAnswer(200, "t2")}}
	cases := []domain.AppCase{{Name: "now", Kind: "http", Method: "GET", Path: "/now"}}
	rec, err := AppRecordFull("svc", cases, runner, RecordOptions{Repeat: 2})
	if err != nil {
		t.Fatal(err)
	}
	u := rec.Cases[0].Unstable
	if !slices.Contains(u, "body") || slices.Contains(u, "status") {
		t.Fatalf("only the body must be unstable, got %v", u)
	}
}

func TestStableCaseHasNoUnstableFields(t *testing.T) {
	runner := &seqRunner{answers: []domain.AppCase{httpAnswer(200, "ok")}}
	rec, _ := AppRecordFull("svc", []domain.AppCase{{Name: "a", Kind: "http", Method: "GET", Path: "/"}}, runner, RecordOptions{Repeat: 3})
	if len(rec.Cases[0].Unstable) != 0 {
		t.Fatalf("a stable case must have no unstable fields: %v", rec.Cases[0].Unstable)
	}
}

func TestReplayIgnoresUnstableBodyButCatchesStatus(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "now", Kind: "http", Method: "GET", Path: "/now", Status: 200, ResponseBody: "t1", Unstable: []string{"body"}},
	}}
	// body differs (expected, unstable): passes
	v, _ := AppReplayFull(rec, &seqRunner{answers: []domain.AppCase{httpAnswer(200, "t9")}}, ReplayOptions{})
	if !v.Passed() {
		t.Fatalf("a varying body must not fail replay: %+v", v.Mismatches)
	}
	// status differs (stable): fails
	v, _ = AppReplayFull(rec, &seqRunner{answers: []domain.AppCase{httpAnswer(500, "t9")}}, ReplayOptions{})
	if v.Passed() {
		t.Fatal("a stable field that changes must still fail")
	}
}

func TestRecordRunsCaseRepeatedly(t *testing.T) {
	runner := &seqRunner{answers: []domain.AppCase{httpAnswer(200, "x")}}
	AppRecordFull("svc", []domain.AppCase{{Name: "a", Kind: "http", Method: "GET", Path: "/"}}, runner, RecordOptions{Repeat: 4})
	if runner.i != 4 {
		t.Fatalf("want 4 runs, got %d", runner.i)
	}
}

func TestBeforeHookRunsBeforeEachRun(t *testing.T) {
	runner := &seqRunner{answers: []domain.AppCase{httpAnswer(200, "x")}}
	calls := 0
	AppRecordFull("svc", []domain.AppCase{{Name: "a", Kind: "http", Method: "GET", Path: "/"}}, runner,
		RecordOptions{Repeat: 2, Before: func() error { calls++; return nil }})
	if calls != 2 {
		t.Fatalf("the before hook must run once per run, got %d", calls)
	}
}
