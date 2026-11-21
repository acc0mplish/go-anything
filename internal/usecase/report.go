package usecase

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// UnitReport turns a unit verification verdict into a report.
// driftChecked says whether a source directory was given, so the report can say if drift was verified.
func UnitReport(v Verdict, driftChecked bool, coverage *domain.Coverage) domain.Report {
	r := domain.Report{Subject: v.Subject, Kind: "unit", Total: v.Total, Matched: v.Matched, Stale: v.Stale, DriftChecked: driftChecked}
	if coverage != nil {
		pct := coverage.Percent
		r.Coverage = &pct
	}
	// Every case is an item. Matched cases carry no detail; mismatches name what differed.
	failed := map[string]Mismatch{}
	for _, m := range v.Mismatches {
		failed[m.Case] = m
	}
	for _, name := range v.Names {
		if m, bad := failed[name]; bad {
			got := m.Got
			if m.Err != nil {
				// The rewrite failed to run: its error is the actual answer, not an empty output.
				got = "error: " + m.Err.Error()
			}
			detail := fmt.Sprintf("%s: expected %q, got %q", m.Status.Describe(), m.Expected, got)
			if m.Detail != "" {
				// The first difference is enough to act on; the full values are in the diff row.
				detail = m.Status.Describe() + ": " + m.Detail
			}
			r.Items = append(r.Items, domain.ReportItem{Name: name, Passed: false, Detail: detail})
			r.Diffs = append(r.Diffs, domain.ReportDiff{Case: name, What: "output", Input: v.Inputs[name], Want: m.Expected, Got: got})
			continue
		}
		r.Items = append(r.Items, domain.ReportItem{Name: name, Passed: true})
	}
	r.Finalize()
	return r
}

// AppReport turns a whole-program replay verdict into a report. kind is "app-http" or "app-cli".
func AppReport(v AppVerdict, kind string) domain.Report {
	r := domain.Report{Subject: v.Subject, Kind: kind, Total: v.Total, Matched: v.Matched}
	failed := map[string][]AppMismatch{}
	for _, m := range v.Mismatches {
		failed[m.Case] = append(failed[m.Case], m)
	}
	// Every case is an item, in recording order. A matched case says what was compared, so the
	// report shows that a webhook was checked and not just that the answer was.
	for _, name := range v.Names {
		ms, bad := failed[name]
		if !bad {
			r.Items = append(r.Items, domain.ReportItem{Name: name, Passed: true, Detail: "answer and effects match"})
			continue
		}
		var parts []string
		for _, m := range ms {
			r.Diffs = append(r.Diffs, domain.ReportDiff{Case: name, What: m.What, Input: v.Inputs[name], Want: m.Want, Got: m.Got})
			part := m.What + " differs"
			if m.Detail != "" {
				part += " at " + m.Detail
			}
			parts = append(parts, part)
		}
		r.Items = append(r.Items, domain.ReportItem{Name: name, Passed: false, Detail: strings.Join(parts, "; ")})
	}
	r.Finalize()
	return r
}

// AppCaseInput describes what a case sends, so a reviewer can send it again by hand: for HTTP the
// method, path and body; for a CLI the arguments and stdin. Request headers are left out on purpose:
// they are where tokens and cookies live, and a report is shared more widely than the recording.
func AppCaseInput(c domain.AppCase) string {
	switch c.Kind {
	case "http":
		s := strings.TrimSpace(c.Method + " " + c.Path)
		if c.Body != "" {
			s += "\n\n" + c.Body
		}
		return s
	case "cli":
		args := make([]string, len(c.Argv))
		for i, a := range c.Argv {
			args[i] = quoteArg(a)
		}
		s := "argv: " + strings.Join(args, " ")
		if c.Stdin != "" {
			s += "\nstdin:\n" + c.Stdin
		}
		return s
	}
	return ""
}

// quoteArg leaves a plain argument as it is and quotes one that a shell would split or that is empty.
func quoteArg(a string) string {
	if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`") {
		return strconv.Quote(a)
	}
	return a
}
