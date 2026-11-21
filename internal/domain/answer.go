package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Answer is what a program said for one case: the HTTP status, body and recorded headers, or the
// command's exit code, stdout and stderr. It holds only the fields that are compared.
//
// This file and compare.go use only the standard library on purpose: go-anything copies both into each
// unit's generated tests (see ComparisonSources), so the generated tests judge an answer by exactly the
// rule the tool uses, instead of by a second hand-written copy of it.
type Answer struct {
	Kind     string // "http" or "cli"
	Status   int
	Body     string
	Headers  map[string]string
	ExitCode int
	Stdout   string
	Stderr   string
}

// AnswerDiff is one field where two answers differ. Detail names the first difference inside the
// field (a JSON path or a line) when the comparison found one.
type AnswerDiff struct {
	What, Want, Got, Detail string
}

// CompareAnswer reports every field where got differs from want. Fields named in unstable varied
// between recorded runs of the original and are not compared. Only the headers want recorded are
// compared, so a header got happens to add (Date, Content-Length) is not a difference.
func CompareAnswer(want, got Answer, unstable []string, opt CompareOptions) []AnswerDiff {
	var out []AnswerDiff
	skip := func(what string) bool {
		for _, u := range unstable {
			if u == what {
				return true
			}
		}
		return false
	}
	exact := func(what, w, g string) {
		if !skip(what) && w != g {
			out = append(out, AnswerDiff{What: what, Want: w, Got: g})
		}
	}
	body := func(what, w, g string) {
		if skip(what) {
			return
		}
		if eq, why := EqualBody(w, g, opt); !eq {
			out = append(out, AnswerDiff{What: what, Want: w, Got: g, Detail: why})
		}
	}
	exactNormalized := func(what, w, g string) {
		// stderr is compared as text, so the normalizers run here rather than in EqualBody.
		if skip(what) {
			return
		}
		if ns, err := parseNormalizers(opt.Normalize); err == nil {
			w, g = normalizeForCompare(w, ns), normalizeForCompare(g, ns)
		}
		if w != g {
			out = append(out, AnswerDiff{What: what, Want: w, Got: g})
		}
	}
	switch want.Kind {
	case "http":
		exact("status", fmt.Sprint(want.Status), fmt.Sprint(got.Status))
		body("body", want.Body, got.Body)
		names := make([]string, 0, len(want.Headers))
		for h := range want.Headers {
			names = append(names, h)
		}
		sort.Strings(names)
		for _, h := range names {
			exact("header "+h, want.Headers[h], got.Headers[h])
		}
	case "cli":
		exact("exit_code", fmt.Sprint(want.ExitCode), fmt.Sprint(got.ExitCode))
		body("stdout", Normalize(want.Stdout), Normalize(got.Stdout))
		exactNormalized("stderr", Normalize(want.Stderr), Normalize(got.Stderr))
	}
	return out
}

// Normalize trims the trailing newline differences some CLIs introduce, so
// "x\n" and "x" are not counted as drift. It never changes interior bytes.
func Normalize(s string) string {
	return strings.TrimRight(s, "\r\n")
}
