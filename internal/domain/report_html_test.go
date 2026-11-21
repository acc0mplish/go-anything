package domain

import (
	"strings"
	"testing"
)

func passingReport() Report {
	r := Report{Subject: "svc", Kind: "unit", Total: 2, Matched: 2, DriftChecked: true,
		Items: []ReportItem{{Name: "a", Passed: true}, {Name: "b", Passed: true}}}
	r.Finalize()
	return r
}

func TestHTMLIsAFullDocumentWithVerdict(t *testing.T) {
	b, err := passingReport().HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"<!DOCTYPE html>", "Verdict: PASS", "<title>Verification report: svc</title>", "2 of 2"} {
		if !strings.Contains(s, want) {
			t.Fatalf("html missing %q", want)
		}
	}
}

func TestHTMLFailShowsReasonsAndDiffs(t *testing.T) {
	r := Report{Subject: "svc", Kind: "app-http", Total: 2, Matched: 1, DriftChecked: true,
		Items: []ReportItem{{Name: "GET /ok", Passed: true}, {Name: "GET /bad", Passed: false, Detail: "body differs"}},
		Diffs: []ReportDiff{{Case: "GET /bad", What: "body", Want: "hi", Got: "HELLO"}}}
	r.Finalize()
	b, _ := r.HTML()
	s := string(b)
	for _, want := range []string{"Verdict: FAIL", "class=\"verdict fail\"", "1 of 2 items did not match", "HELLO", "mismatch"} {
		if !strings.Contains(s, want) {
			t.Fatalf("fail html missing %q", want)
		}
	}
}

func TestHTMLEscapesRecordedContent(t *testing.T) {
	// A recorded response is untrusted input: markup in it must be shown, never executed.
	payload := `<script>alert(1)</script>`
	r := Report{Subject: `<img src=x onerror=alert(2)>`, Kind: "app-http", Total: 1, Matched: 0, DriftChecked: true,
		Items: []ReportItem{{Name: "GET /x", Passed: false, Detail: payload}},
		Diffs: []ReportDiff{{Case: "GET /x", What: "body", Want: payload, Got: "ok"}}}
	r.Finalize()
	b, err := r.HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "<script>alert") || strings.Contains(s, "<img src=x") {
		t.Fatalf("recorded markup must be escaped, found raw tag:\n%s", s)
	}
	if !strings.Contains(s, "&lt;script&gt;") {
		t.Fatal("escaped form must be present so the text is still visible")
	}
}

func TestHTMLHasNoScriptOrExternalAssets(t *testing.T) {
	b, _ := passingReport().HTML()
	s := strings.ToLower(string(b))
	if strings.Contains(s, "<script") {
		t.Fatal("report must not contain a script element")
	}
	if strings.Contains(s, "src=\"http") || strings.Contains(s, "href=\"http") {
		t.Fatal("report must be self-contained: no external links or assets")
	}
}

func TestHTMLStaleNotCheckedIsStated(t *testing.T) {
	r := Report{Subject: "svc", Kind: "unit", Total: 1, Matched: 1, DriftChecked: false,
		Items: []ReportItem{{Name: "a", Passed: true}}}
	r.Finalize()
	b, _ := r.HTML()
	if !strings.Contains(string(b), "not checked (no source directory given)") {
		t.Fatal("an unchecked drift must be stated in the HTML")
	}
}

func mismatchReport() Report {
	r := Report{Subject: "svc", Kind: "unit", Total: 3, Matched: 2, DriftChecked: true,
		Items: []ReportItem{{Name: "line-1", Passed: true}, {Name: "line-2", Passed: true},
			{Name: "line-4", Passed: false, Detail: `mismatch: expected "100", got "101"`}},
		Diffs: []ReportDiff{{Case: "line-4", What: "output", Input: "49-input", Want: "100", Got: "101"}},
		Run:   &ReportRun{Rewrite: "python3 rew.py", Recording: "rec.json", GeneratedAt: "2025-01-02T03:04:05Z", ToolVersion: "9.9.9"}}
	r.Finalize()
	return r
}

func TestHTMLMismatchShowsInputAndRunMetadata(t *testing.T) {
	b, err := mismatchReport().HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"49-input", "python3 rew.py", "rec.json", "2025-01-02T03:04:05Z", "9.9.9", "<h1>Verification report</h1>"} {
		if !strings.Contains(s, want) {
			t.Fatalf("html missing %q:\n%s", want, s)
		}
	}
	// The mismatch detail is shown once, in the mismatch table, not again as an item detail.
	if strings.Contains(s, "expected &#34;100&#34;") || strings.Contains(s, `expected "100"`) {
		t.Fatal("a mismatch with a diff row must not repeat its detail")
	}
}

func TestHTMLMismatchesComeBeforeMatchedWhichAreCollapsed(t *testing.T) {
	s := string(must(mismatchReport().HTML()))
	mis, det := strings.Index(s, `id="mismatches"`), strings.Index(s, "<details>")
	if mis < 0 || det < 0 || mis > det {
		t.Fatalf("mismatches must come before the collapsed matched cases (mis=%d details=%d)", mis, det)
	}
	block := s[det:strings.Index(s, "</details>")]
	if !strings.Contains(block, "2 matched cases") || !strings.Contains(block, "line-1") || !strings.Contains(block, "line-2") {
		t.Fatalf("matched cases must be inside <details>:\n%s", block)
	}
	if strings.Contains(block, "line-4") {
		t.Fatal("a mismatching case must not be listed with the matched ones")
	}
}

func TestHTMLMarksOnlyTheDifferingMiddle(t *testing.T) {
	want, got := markDiff("100", "101")
	if string(want) != `10<mark class="diff">0</mark>` || string(got) != `10<mark class="diff">1</mark>` {
		t.Fatalf("got want=%q got=%q", want, got)
	}
	want, got = markDiff("a-XY-z", "a-Q-z")
	if string(want) != `a-<mark class="diff">XY</mark>-z` || string(got) != `a-<mark class="diff">Q</mark>-z` {
		t.Fatalf("middle only: want=%q got=%q", want, got)
	}
	want, got = markDiff("same", "same")
	if strings.Contains(string(want)+string(got), "<mark") {
		t.Fatal("equal values must not be marked")
	}
}

func TestHTMLTrailingNewlineAndWhitespaceAreVisible(t *testing.T) {
	want, got := markDiff("ok\n", "ok")
	if !strings.Contains(string(want), `<mark class="diff"><span class="ws">↵</span>`+"\n</mark>") {
		t.Fatalf("trailing newline must be marked and visible as ↵: %q", want)
	}
	if strings.Contains(string(got), "<mark") {
		t.Fatalf("the side without the newline has nothing to mark: %q", got)
	}
	v := string(markValue("a\tb  \nc d", 0, 0))
	for _, sub := range []string{`<span class="ws">→</span>`, `<span class="ws">·</span><span class="ws">·</span>`, "c d"} {
		if !strings.Contains(v, sub) {
			t.Fatalf("visible whitespace missing %q in %q", sub, v)
		}
	}
}

func TestHTMLEscapesRecordedValuesInsideMarks(t *testing.T) {
	r := Report{Subject: "svc", Kind: "app-http", Total: 1,
		Items: []ReportItem{{Name: "GET /x", Passed: false}},
		Diffs: []ReportDiff{{Case: "GET /x", What: "body", Input: "POST /x\n\n<script>alert(3)</script>",
			Want: "<b>ok</b>", Got: "<script>alert(1)</script>"}}}
	r.Finalize()
	s := strings.ToLower(string(must(r.HTML())))
	if strings.Contains(s, "<script") || strings.Contains(s, "<b>") {
		t.Fatalf("recorded markup must be escaped:\n%s", s)
	}
	// The common prefix "<" and suffix ">" stay outside the mark, so the escaped text is split there.
	if !strings.Contains(s, `&lt;<mark class="diff">script&gt;alert(1)&lt;/script</mark>&gt;`) ||
		!strings.Contains(s, "&lt;script&gt;alert(3)&lt;/script&gt;") {
		t.Fatal("escaped value must still be visible")
	}
}

func TestHTMLClipsLongValues(t *testing.T) {
	long := strings.Repeat("x", ReportValueLimit+500)
	r := Report{Subject: "svc", Kind: "unit", Total: 1,
		Items: []ReportItem{{Name: "c", Passed: false}},
		Diffs: []ReportDiff{{Case: "c", What: "output", Input: long, Want: long, Got: "y"}}}
	r.Finalize()
	s := string(must(r.HTML()))
	if strings.Contains(s, strings.Repeat("x", ReportValueLimit+1)) {
		t.Fatal("a value longer than the limit must be clipped")
	}
	if !strings.Contains(s, "[... 500 more bytes not shown]") {
		t.Fatal("the clip must say how much was left out")
	}
}

func TestHTMLFailuresByFieldSortedByCount(t *testing.T) {
	r := Report{Subject: "svc", Kind: "app-http", Total: 3,
		Items: []ReportItem{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Diffs: []ReportDiff{{Case: "a", What: "status"}, {Case: "a", What: "body"}, {Case: "b", What: "body"}, {Case: "c", What: "body"}}}
	r.Finalize()
	got := r.FailuresByField()
	if len(got) != 2 || got[0] != (FieldCount{"body", 3}) || got[1] != (FieldCount{"status", 1}) {
		t.Fatalf("want body 3 then status 1, got %+v", got)
	}
	s := string(must(r.HTML()))
	if !strings.Contains(s, "Failures by field") || strings.Index(s, ">body<") > strings.Index(s, ">status<") {
		t.Fatal("failures by field table must list the most frequent field first")
	}
}

func TestNextSteps(t *testing.T) {
	cases := []struct {
		name string
		r    Report
		want []string // substrings, in order, one per step
	}{
		{"pass", Report{Kind: "unit", Total: 2, Matched: 2, DriftChecked: true}, []string{"No action needed"}},
		{"pass drift unchecked", Report{Kind: "unit", Total: 2, Matched: 2}, []string{"-src"}},
		{"app pass", Report{Kind: "app-http", Total: 2, Matched: 2}, []string{"No action needed"}},
		{"nothing checked", Report{Kind: "unit", DriftChecked: true}, []string{"Record cases first", "Run the same command again"}},
		{"mismatch", Report{Kind: "app-http", Total: 4, Matched: 1, Diffs: []ReportDiff{{What: "status"}, {What: "body"}, {What: "body"}}},
			[]string{"Fix the 3 mismatching case(s) below, starting with the field that fails most: body (2)", "Run the same command again"}},
		{"stale before mismatch", Report{Kind: "unit", Total: 2, Matched: 1, Stale: true, DriftChecked: true, Diffs: []ReportDiff{{What: "output"}}},
			[]string{"Re-record", "Fix the 1 mismatching case(s)", "Run the same command again"}},
		{"stale only", Report{Kind: "unit", Total: 2, Matched: 2, Stale: true, DriftChecked: true}, []string{"Re-record", "Run the same command again"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.r.Finalize()
			got := c.r.NextSteps()
			if len(got) != len(c.want) {
				t.Fatalf("want %d steps, got %d: %q", len(c.want), len(got), got)
			}
			for i, sub := range c.want {
				if !strings.Contains(got[i], sub) {
					t.Fatalf("step %d: want %q in %q", i+1, sub, got[i])
				}
			}
		})
	}
}

func TestRedactCommand(t *testing.T) {
	cases := map[string][]string{
		"python3 rew.py":                          {"python3", "rew.py"},
		"svc --token=[redacted] -v":               {"svc", "--token=x1", "-v"},
		"svc --password [redacted] x":             {"svc", "--password", "hunter2", "x"},
		"http://bob:[redacted]@127.0.0.1:8080/":   {"http://bob:pw@127.0.0.1:8080/"},
		"http://127.0.0.1:8080/a@b":               {"http://127.0.0.1:8080/a@b"},
		"svc -api-key=[redacted]":                 {"svc", "-api-key=k"},
		"svc --authorization=[redacted] --name=a": {"svc", "--authorization=Bearer abc", "--name=a"},
	}
	for want, argv := range cases {
		if got := RedactCommand(argv); got != want {
			t.Fatalf("RedactCommand(%q) = %q, want %q", argv, got, want)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestVerificationViewport(t *testing.T) {
	r, err := passingReport().HTML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r), `name="viewport"`) {
		t.Fatal("the verification report must carry the viewport meta for narrow screens")
	}
}
