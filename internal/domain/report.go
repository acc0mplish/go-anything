package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ReportItem is one checked thing (a function call, an endpoint) and whether it matched.
type ReportItem struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	// Detail explains a failure: which field differed, and the expected and actual values.
	Detail string `json:"detail,omitempty"`
}

// ReportDiff is one difference between the original and the rewrite, for the mismatch section.
type ReportDiff struct {
	Case string `json:"case"`
	What string `json:"what"`
	// Input is what the case sent (unit input, HTTP request line and body, or argv and stdin), so a
	// reviewer can reproduce the difference from the report alone.
	Input string `json:"input,omitempty"`
	Want  string `json:"want"`
	Got   string `json:"got"`
}

// ReportRun says which run produced a report, so a reader can tell what was checked and when.
type ReportRun struct {
	// Rewrite is the command or URL the recording was replayed against.
	Rewrite string `json:"rewrite,omitempty"`
	// Recording is the recording file the expected values came from.
	Recording string `json:"recording,omitempty"`
	// GeneratedAt is when the report was written, in UTC RFC 3339.
	GeneratedAt string `json:"generated_at,omitempty"`
	// ToolVersion is the go-anything version that wrote the report.
	ToolVersion string `json:"tool_version,omitempty"`
	// Compare says how answers were compared: "exact", or JSON semantic and the float tolerance.
	Compare string `json:"compare,omitempty"`
}

// FieldCount is how many differences one field (output, status, body, header X...) has.
type FieldCount struct {
	Field string
	Count int
}

// Report is the result of one verification, in a form people can read and tools can parse.
type Report struct {
	Subject string `json:"subject"`
	// Kind says what was checked: "unit" (function behavior), "app-http" or "app-cli".
	Kind string `json:"kind"`
	// Total and Matched count checked items; Passed is the gate decision.
	Total   int  `json:"total"`
	Matched int  `json:"matched"`
	Passed  bool `json:"passed"`
	// Reasons lists why the gate failed. Empty when Passed.
	Reasons []string `json:"reasons,omitempty"`
	// Stale is set when the recording no longer matches the source it came from.
	Stale bool `json:"stale"`
	// DriftChecked is false when no source was given, so Stale says nothing about drift.
	DriftChecked bool `json:"drift_checked"`
	// Coverage is the original's statement coverage percent when it was measured.
	Coverage *float64     `json:"coverage_percent,omitempty"`
	Items    []ReportItem `json:"items"`
	Diffs    []ReportDiff `json:"diffs,omitempty"`
	// Run describes the run that produced the report. Nil when the caller did not say.
	Run *ReportRun `json:"run,omitempty"`
}

// FailuresByField counts the differences per field, most frequent first (ties by field name), so the
// reader sees which kind of failure dominates.
func (r Report) FailuresByField() []FieldCount {
	counts := map[string]int{}
	for _, d := range r.Diffs {
		counts[d.What]++
	}
	out := make([]FieldCount, 0, len(counts))
	for f, n := range counts {
		out = append(out, FieldCount{Field: f, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// NextSteps derives the ordered actions a reviewer should take from the gate result. A stale
// recording comes before the mismatches: fixing the rewrite against old behavior is wasted work.
func (r Report) NextSteps() []string {
	var steps []string
	if r.Total == 0 {
		steps = append(steps, "Record cases first: nothing was checked, so this report says nothing about the rewrite yet.")
	}
	if r.Stale {
		steps = append(steps, "Re-record the original: its source changed since the recording was made, so the expected values may be out of date.")
	}
	if n := r.Total - r.Matched; n > 0 {
		s := fmt.Sprintf("Fix the %d mismatching case(s) below", n)
		if top := r.FailuresByField(); len(top) > 0 {
			s += fmt.Sprintf(", starting with the field that fails most: %s (%d)", top[0].Field, top[0].Count)
		}
		steps = append(steps, s+".")
	}
	if r.Kind == "unit" && r.Total > 0 && !r.DriftChecked {
		steps = append(steps, "Run verify again with -src set to the original's source directory, so the report can confirm the recording is not stale.")
	}
	if !r.Passed {
		steps = append(steps, "Run the same command again: the rewrite is acceptable only when this report shows PASS.")
	}
	if len(steps) == 0 {
		steps = append(steps, "No action needed. Keep the recording next to the rewrite so later changes are checked against it.")
	}
	return steps
}

// RunLine is the run metadata in one line, or "" when the report does not say which run made it.
func (r Report) RunLine() string {
	if r.Run == nil {
		return ""
	}
	var parts []string
	add := func(label, v string) {
		if v != "" {
			parts = append(parts, label+" "+v)
		}
	}
	add("Rewrite:", r.Run.Rewrite)
	add("Recording:", r.Run.Recording)
	add("Generated:", r.Run.GeneratedAt)
	add("Compare:", r.Run.Compare)
	add("go-anything", r.Run.ToolVersion)
	return strings.Join(parts, " · ")
}

// RedactCommand joins a command line for display, hiding values that look like secrets: the value of
// a flag such as --token=x or "--password x", and the password in a URL. A report is shared more
// widely than a shell history, so the command must not carry credentials into it.
func RedactCommand(argv []string) string {
	out := make([]string, len(argv))
	hideNext := false
	for i, a := range argv {
		switch {
		case hideNext:
			out[i] = "[redacted]"
			hideNext = false
			continue
		case strings.HasPrefix(a, "-"):
			name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if secretName(name) {
				if hasValue {
					out[i] = a[:strings.Index(a, "=")+1] + "[redacted]"
				} else {
					out[i] = a
					hideNext = true
				}
				continue
			}
		}
		out[i] = redactURLPassword(a)
	}
	return strings.Join(out, " ")
}

func secretName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"token", "secret", "password", "passwd", "apikey", "api-key", "api_key", "auth", "credential"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// redactURLPassword hides the password in "scheme://user:pass@host...".
func redactURLPassword(s string) string {
	scheme := strings.Index(s, "://")
	if scheme < 0 {
		return s
	}
	rest := s[scheme+3:]
	at := strings.Index(rest, "@")
	if at < 0 || strings.ContainsAny(rest[:at], "/?#") {
		return s
	}
	user, _, hasPass := strings.Cut(rest[:at], ":")
	if !hasPass {
		return s
	}
	return s[:scheme+3] + user + ":[redacted]" + rest[at:]
}

// MatchRate is the share of checked items that matched, as a percentage.
func (r Report) MatchRate() float64 {
	if r.Total == 0 {
		return 0
	}
	return 100 * float64(r.Matched) / float64(r.Total)
}

// Finalize sets the gate decision and its reasons from the counts and flags. It is the only
// place the pass/fail rule for a report is written, so every command judges the same way.
func (r *Report) Finalize() {
	r.Reasons = nil
	if r.Total == 0 {
		r.Reasons = append(r.Reasons, "nothing was checked")
	}
	if r.Matched != r.Total {
		r.Reasons = append(r.Reasons, fmt.Sprintf("%d of %d items did not match", r.Total-r.Matched, r.Total))
	}
	if r.Stale {
		r.Reasons = append(r.Reasons, "the original's source changed since the recording was made")
	}
	r.Passed = len(r.Reasons) == 0
}

// Markdown renders the report as a readable document with the verdict first.
func (r Report) Markdown() string {
	var b strings.Builder
	verdict := "PASS"
	if !r.Passed {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "# Verification report: %s\n\n", r.Subject)
	if line := r.RunLine(); line != "" {
		fmt.Fprintf(&b, "%s\n\n", line)
	}
	fmt.Fprintf(&b, "**Verdict: %s**\n\n", verdict)
	for _, reason := range r.Reasons {
		fmt.Fprintf(&b, "- %s\n", reason)
	}
	if len(r.Reasons) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("Next steps:\n\n")
	for i, step := range r.NextSteps() {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "| Item | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Kind | %s |\n", r.Kind)
	fmt.Fprintf(&b, "| Matched | %d of %d (%.1f%%) |\n", r.Matched, r.Total, r.MatchRate())
	if r.DriftChecked {
		fmt.Fprintf(&b, "| Stale recording | %t |\n", r.Stale)
	} else {
		fmt.Fprintf(&b, "| Stale recording | not checked (no source directory given) |\n")
	}
	if r.Coverage != nil {
		fmt.Fprintf(&b, "| Original statement coverage | %.1f%% |\n", *r.Coverage)
	}
	b.WriteString("\n## Checked items\n\n| Result | Item | Detail |\n|---|---|---|\n")
	for _, it := range r.Items {
		mark := "match"
		if !it.Passed {
			mark = "**mismatch**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", mark, escapeCell(it.Name), escapeCell(it.Detail))
	}
	if len(r.Diffs) > 0 {
		diffs := append([]ReportDiff(nil), r.Diffs...)
		sort.SliceStable(diffs, func(i, j int) bool {
			if diffs[i].Case != diffs[j].Case {
				return diffs[i].Case < diffs[j].Case
			}
			return diffs[i].What < diffs[j].What
		})
		b.WriteString("\n## Differences\n\n| Case | Field | Input | Expected | Actual |\n|---|---|---|---|---|\n")
		for _, d := range diffs {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", escapeCell(d.Case), escapeCell(d.What),
				escapeValue(d.Input), escapeValue(d.Want), escapeValue(d.Got))
		}
	}
	fmt.Fprintf(&b, "\nValues are shown as plain text, clipped at %d bytes, and may contain personal data. Share with care.\n", ReportValueLimit)
	return b.String()
}

// JSON renders the report for tools and CI.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// escapeValue clips a recorded value and keeps it on one table row. Whitespace is made visible the
// same way the HTML report shows it (newline as ↵, tab as →, trailing space as ·), so a trailing
// newline difference is not lost when rows are joined.
func escapeValue(s string) string {
	var b strings.Builder
	for _, g := range visibleRunes(ClipForReport(s, ReportValueLimit)) {
		b.WriteString(g.text)
	}
	// A backslash keeps a Markdown viewer from reading recorded markup as HTML.
	return strings.NewReplacer("<", `\<`, ">", `\>`).Replace(escapeCell(b.String()))
}

// escapeCell keeps a value from breaking a Markdown table row.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
