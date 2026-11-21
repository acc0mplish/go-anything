package usecase

import (
	"errors"
	"strings"
	"testing"
)

// fnSubject is an in-process program: a function from input to output or error.
type fnSubject func(string) (string, error)

func (f fnSubject) Run(in string) (string, error) { return f(in) }

func TestFuzzAgreementPasses(t *testing.T) {
	up := fnSubject(func(s string) (string, error) { return strings.ToUpper(s), nil })
	rep := Fuzz(up, up, []string{"a", "é"}, 9)
	if !rep.Passed() || rep.Seed != 9 || rep.Total != 2 {
		t.Fatalf("identical programs must pass: %+v", rep)
	}
}

func TestFuzzFindsOutputDifference(t *testing.T) {
	orig := fnSubject(func(s string) (string, error) { return strings.ToUpper(s), nil })
	bad := fnSubject(func(s string) (string, error) {
		if strings.ContainsAny(s, "é") {
			return "wrong", nil
		}
		return strings.ToUpper(s), nil
	})
	rep := Fuzz(orig, bad, []string{"a", "é"}, 1)
	if rep.Passed() || len(rep.Mismatches) != 1 || rep.Mismatches[0].Input != "é" {
		t.Fatalf("the non-ASCII difference must be found: %+v", rep)
	}
}

func TestFuzzOneSideFailingIsMismatch(t *testing.T) {
	ok := fnSubject(func(s string) (string, error) { return "x", nil })
	fail := fnSubject(func(s string) (string, error) { return "", errors.New("boom") })
	rep := Fuzz(ok, fail, []string{"a"}, 1)
	if rep.Passed() || !rep.Mismatches[0].RewriteFailed {
		t.Fatalf("rewrite failing where original answers must be a mismatch: %+v", rep)
	}
}

func TestFuzzBothFailingAgrees(t *testing.T) {
	fail := fnSubject(func(s string) (string, error) { return "", errors.New("refused") })
	other := fnSubject(func(s string) (string, error) { return "", errors.New("different text") })
	if rep := Fuzz(fail, other, []string{"a"}, 1); !rep.Passed() {
		t.Fatalf("both refusing the input must agree even if the text differs: %+v", rep)
	}
}
