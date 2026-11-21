package usecase

import (
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestBenchRefusesTimingWhenOutputsDiffer(t *testing.T) {
	rec := domain.Recording{Subject: "t", Cases: []domain.Case{{Name: "a", Input: "1", Output: "one"}}}
	res, err := Bench(rec, fakeSubject{"1": "one"}, fakeSubject{"1": "uno"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.OutputsMatch {
		t.Fatal("differing outputs must not be reported as a valid timing")
	}
}

func TestBenchMatchingOutputsIsValid(t *testing.T) {
	rec := domain.Recording{Subject: "t", Cases: []domain.Case{{Name: "a", Input: "1", Output: "one"}}}
	res, err := Bench(rec, fakeSubject{"1": "one"}, fakeSubject{"1": "one"}, 2)
	if err != nil || !res.OutputsMatch {
		t.Fatalf("matching outputs must be valid: %+v err=%v", res, err)
	}
}

func TestShadowReturnsOriginalAndFlagsDisagreement(t *testing.T) {
	rec := domain.Recording{Subject: "t", Cases: []domain.Case{{Name: "a", Input: "1", Output: "one"}}}
	res, err := Shadow(rec, fakeSubject{"1": "one"}, fakeSubject{"1": "uno"})
	if err != nil {
		t.Fatal(err)
	}
	r := res["a"]
	if r.Output != "one" {
		t.Fatalf("callers must receive the original's output, got %q", r.Output)
	}
	if len(r.Mismatches) != 1 {
		t.Fatalf("disagreement must be flagged: %+v", r)
	}
}

func TestShadowFlagsRewriteFailureOriginalSucceeded(t *testing.T) {
	rec := domain.Recording{Subject: "t", Cases: []domain.Case{{Name: "a", Input: "1", Output: "one"}}}
	res, _ := Shadow(rec, fakeSubject{"1": "one"}, fakeSubject{})
	if len(res["a"].Mismatches) != 1 || res["a"].Output != "one" {
		t.Fatalf("rewrite failure must be flagged while the original answer is returned: %+v", res["a"])
	}
}
