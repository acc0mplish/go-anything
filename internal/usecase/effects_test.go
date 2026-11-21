package usecase

import (
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// fakeSource hands out a fixed list of effects, one batch per Drain after the first.
type fakeSource struct{ calls [][]domain.Effect }

func (f *fakeSource) Drain() []domain.Effect {
	if len(f.calls) == 0 {
		return nil
	}
	out := f.calls[0]
	f.calls = f.calls[1:]
	return out
}

func TestRecordCapturesEffectsOfEachCase(t *testing.T) {
	cases := []domain.AppCase{{Name: "order", Kind: "http", Method: "POST", Path: "/orders"}}
	// batch 1: stray call before the case (drained away); batch 2: the case's own effect
	src := &fakeSource{calls: [][]domain.Effect{
		{{Method: "POST", Path: "/stray"}},
		{{Method: "POST", Path: "/hook", Body: "x"}},
	}}
	rec, err := AppRecordWithEffects("svc", cases, fakeRunner{"order": {Status: 201}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Cases[0].Effects) != 1 || rec.Cases[0].Effects[0].Path != "/hook" {
		t.Fatalf("only the case's own effect must be kept: %+v", rec.Cases[0].Effects)
	}
}

func TestReplayFailsWhenSameAnswerHasDifferentEffect(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "order", Kind: "http", Method: "POST", Path: "/orders", Status: 201,
			Effects: []domain.Effect{{Method: "POST", Path: "/hook", Body: `{"id":1}`}}},
	}}
	// the answer is identical, but the webhook body differs
	src := &fakeSource{calls: [][]domain.Effect{nil, {{Method: "POST", Path: "/hook", Body: `{"id":2}`}}}}
	v, err := AppReplayWithEffects(rec, fakeRunner{"order": {Status: 201}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed() {
		t.Fatal("a changed side effect must fail even when the answer matches")
	}
	if len(v.Mismatches) != 1 || v.Mismatches[0].What != "effect 1" {
		t.Fatalf("the effect must be named: %+v", v.Mismatches)
	}
}

func TestReplayFailsWhenExpectedEffectIsMissing(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "order", Kind: "http", Method: "POST", Path: "/orders", Status: 201,
			Effects: []domain.Effect{{Method: "POST", Path: "/hook"}}},
	}}
	v, _ := AppReplayWithEffects(rec, fakeRunner{"order": {Status: 201}}, &fakeSource{calls: [][]domain.Effect{nil, nil}})
	if v.Passed() {
		t.Fatal("a webhook that was never sent must fail")
	}
}

func TestReplayPassesWhenEffectsMatch(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "order", Kind: "http", Method: "POST", Path: "/orders", Status: 201,
			Effects: []domain.Effect{{Method: "POST", Path: "/hook", Body: "b"}}},
	}}
	src := &fakeSource{calls: [][]domain.Effect{nil, {{Method: "POST", Path: "/hook", Body: "b"}}}}
	v, _ := AppReplayWithEffects(rec, fakeRunner{"order": {Status: 201}}, src)
	if !v.Passed() {
		t.Fatalf("identical effects must pass: %+v", v.Mismatches)
	}
}

func TestOrderOfEffectsMatters(t *testing.T) {
	want := []domain.Effect{{Path: "/a"}, {Path: "/b"}}
	got := []domain.Effect{{Path: "/b"}, {Path: "/a"}}
	if len(compareEffects("c", want, got)) == 0 {
		t.Fatal("the same calls in a different order must be a difference")
	}
}
