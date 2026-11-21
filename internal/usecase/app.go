package usecase

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// AppRunner performs one AppCase against a running program and returns what it answered.
// An adapter implements it: HTTP against a server, or a process for a CLI.
type AppRunner interface {
	Do(c domain.AppCase) (domain.AppCase, error)
}

// AppRecord runs every case and returns the recording: each case with the response filled in.
// The input fields are kept; the response fields are what the original answered.
func AppRecord(subject string, cases []domain.AppCase, runner AppRunner) (domain.AppRecording, error) {
	rec := domain.AppRecording{Subject: subject}
	for _, c := range cases {
		answered, err := runner.Do(c)
		if err != nil {
			return domain.AppRecording{}, fmt.Errorf("app record: case %q: %w", c.Name, err)
		}
		// Keep the request exactly as written; take only the answer from the runner.
		rec.Cases = append(rec.Cases, withAnswer(c, answered))
	}
	return rec, rec.Validate()
}

// withAnswer returns the request c with the response fields copied from answered.
func withAnswer(c, answered domain.AppCase) domain.AppCase {
	c.Status = answered.Status
	c.ResponseBody = answered.ResponseBody
	c.Headers = answered.Headers
	c.ExitCode = answered.ExitCode
	c.Stdout = answered.Stdout
	c.Stderr = answered.Stderr
	return c
}

// AppMismatch is one case where the replayed answer differs from the recording.
type AppMismatch struct {
	Case string
	What string // which field differed
	Want string
	Got  string
	// Detail names the first difference (a JSON path or a line) when the comparison found one.
	Detail string
}

// AppVerdict is the outcome of replaying an app recording.
type AppVerdict struct {
	Subject string
	Total   int
	Matched int
	// Names lists every case in recording order, matched or not, so reports can name each one.
	Names      []string
	Mismatches []AppMismatch
	// Inputs maps a case name to what was sent (request or argv and stdin), so a report can show
	// how to reproduce a failure.
	Inputs map[string]string
	// Compare is how the answers were compared. The zero value is exact.
	Compare domain.CompareOptions
}

// Passed reports whether every case reproduced its recorded answer.
func (v AppVerdict) Passed() bool { return v.Total > 0 && v.Matched == v.Total }

// AppReplay sends every recorded request again and compares the answer field by field.
// A case that fails to run at all counts as a mismatch, never as a pass.
func AppReplay(rec domain.AppRecording, runner AppRunner) (AppVerdict, error) {
	if err := rec.Validate(); err != nil {
		return AppVerdict{}, fmt.Errorf("app replay: %w", err)
	}
	v := AppVerdict{Subject: rec.Subject, Total: len(rec.Cases), Inputs: map[string]string{}}
	for _, want := range rec.Cases {
		v.Names = append(v.Names, want.Name)
		v.Inputs[want.Name] = AppCaseInput(want)
		got, err := runner.Do(want)
		if err != nil {
			v.Mismatches = append(v.Mismatches, AppMismatch{Case: want.Name, What: "run", Want: "answer", Got: err.Error()})
			continue
		}
		diffs := compareApp(want, got)
		if len(diffs) == 0 {
			v.Matched++
			continue
		}
		v.Mismatches = append(v.Mismatches, diffs...)
	}
	return v, nil
}

// compareApp reports every response field that differs, comparing exactly.
func compareApp(want, got domain.AppCase) []AppMismatch {
	return compareAppWith(want, got, domain.CompareOptions{})
}

// compareAppWith reports every response field that differs. Only the answer is compared: the
// request fields are the test's own input and are not expected to change. The HTTP body and CLI
// stdout are compared under opt (domain.EqualBody); every other field is compared exactly.
func compareAppWith(want, got domain.AppCase, opt domain.CompareOptions) []AppMismatch {
	diffs := domain.CompareAnswer(domain.AnswerOf(want), domain.AnswerOf(got), want.Unstable, opt)
	if len(diffs) == 0 {
		return nil
	}
	out := make([]AppMismatch, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, AppMismatch{Case: want.Name, What: d.What, Want: d.Want, Got: d.Got, Detail: d.Detail})
	}
	return out
}

// SelectCase returns the recording reduced to the one case with the given name, so a single failure
// can be replayed on its own. An unknown name is an error that lists the cases that exist.
func SelectCase(rec domain.AppRecording, name string) (domain.AppRecording, error) {
	names := make([]string, 0, len(rec.Cases))
	for _, c := range rec.Cases {
		if c.Name == name {
			return domain.AppRecording{Subject: rec.Subject, Cases: []domain.AppCase{c}}, nil
		}
		names = append(names, strconv.Quote(c.Name))
	}
	const shown = 50
	list := names
	more := ""
	if len(names) > shown {
		list, more = names[:shown], fmt.Sprintf(" and %d more", len(names)-shown)
	}
	return domain.AppRecording{}, fmt.Errorf("no case named %q; recorded cases: %s%s", name, strings.Join(list, ", "), more)
}
