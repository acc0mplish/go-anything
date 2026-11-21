package usecase

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

type fakeSubject map[string]string

func (f fakeSubject) Run(in string) (string, error) {
	out, ok := f[in]
	if !ok {
		return "", errors.New("unknown input")
	}
	return out, nil
}

func rec(cases ...domain.Case) domain.Recording { return domain.Recording{Subject: "t", Cases: cases} }

func TestVerifyPasses(t *testing.T) {
	v, err := Verify(rec(domain.Case{Name: "a", Input: "1", Output: "one"}), fakeSubject{"1": "one"})
	if err != nil || !v.Passed() {
		t.Fatalf("want pass, got %+v err=%v", v, err)
	}
}

func TestVerifyIgnoresTrailingNewlineOnly(t *testing.T) {
	v, _ := Verify(rec(domain.Case{Name: "a", Input: "1", Output: "one\n"}), fakeSubject{"1": "one"})
	if !v.Passed() {
		t.Fatalf("trailing newline should not be drift: %+v", v)
	}
}

func TestVerifyReportsMismatchAndError(t *testing.T) {
	v, err := Verify(rec(
		domain.Case{Name: "wrong", Input: "1", Output: "one"},
		domain.Case{Name: "missing", Input: "2", Output: "two"},
	), fakeSubject{"1": "uno"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed() || len(v.Mismatches) != 2 || v.Matched != 0 {
		t.Fatalf("want 2 mismatches, got %+v", v)
	}
	if v.Mismatches[1].Status != domain.StatusError {
		t.Fatalf("want error status, got %s", v.Mismatches[1].Status)
	}
}

func TestVerifyRecordedErrorMustStayAnError(t *testing.T) {
	r := rec(domain.Case{Name: "boom", Input: "x", Error: "bad input"})
	if v, _ := Verify(r, fakeSubject{"x": "ok"}); v.Passed() {
		t.Fatal("rewrite that succeeds where original failed must not pass")
	}
	if v, _ := Verify(r, fakeSubject{}); !v.Passed() {
		t.Fatal("rewrite that also fails must pass the error case")
	}
}

func TestVerifyRejectsEmptyRecording(t *testing.T) {
	_, err := Verify(domain.Recording{}, fakeSubject{})
	if err == nil || !strings.Contains(err.Error(), "no cases") {
		t.Fatalf("want empty-recording error, got %v", err)
	}
}

func TestRecordCapturesErrorsAndIsDeterministic(t *testing.T) {
	inputs := map[string]string{"b": "2", "a": "1"}
	orig := fakeSubject{"1": "one"}
	r1, err := Record("orig", inputs, orig)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Cases[0].Name != "a" || r1.Cases[1].Name != "b" {
		t.Fatalf("cases not sorted: %+v", r1.Cases)
	}
	if r1.Cases[1].Error == "" {
		t.Fatal("failing original input must be recorded as an error case")
	}
}

func TestRecordingRejectsDuplicateNames(t *testing.T) {
	if err := rec(domain.Case{Name: "x"}, domain.Case{Name: "x"}).Validate(); err == nil {
		t.Fatal("want duplicate-name error")
	}
}
