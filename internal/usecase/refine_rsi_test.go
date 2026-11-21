package usecase

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestJudgeProbeExpectedPolicyComparesExitCodeNotTraceback(t *testing.T) {
	want := domain.AppCase{Name: "p", Kind: "cli", Argv: []string{"greet", "007"}, ExitCode: 1, Stderr: pyTraceback}
	sameCode := domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: "ValueError: agent name is classified\n"}
	otherCode := domain.AppCase{Kind: "cli", ExitCode: 0, Stdout: "hello, 007\n"}

	out, marked := JudgeProbe(want, sameCode, true)
	if out != ProbeAgree || !slices.Contains(marked.Unstable, "stderr") {
		t.Fatalf("expected policy, same exit code: want agree with stderr unstable, got %v %v", out, marked.Unstable)
	}
	if out, _ := JudgeProbe(want, otherCode, true); out != ProbeDisagree {
		t.Fatalf("expected policy, different exit code and stdout: want disagree, got %v", out)
	}
	if out, _ := JudgeProbe(want, sameCode, false); out != ProbeOriginalDefect {
		t.Fatalf("defect policy: a crash is a defect, got %v", out)
	}
	// A non-crash answer is compared in full under either policy.
	plain := domain.AppCase{Kind: "cli", ExitCode: 2, Stderr: "usage: greet <name>\n"}
	if out, m := JudgeProbe(plain, domain.AppCase{Kind: "cli", ExitCode: 2, Stderr: "usage\n"}, true); out != ProbeDisagree || len(m.Unstable) != 0 {
		t.Fatalf("a usage error is not a crash: stderr must still be compared, got %v %v", out, m.Unstable)
	}
}

func TestJudgeProbeExpectedPolicyHTTPComparesStatusNotBody(t *testing.T) {
	want := domain.AppCase{Kind: "http", Method: "GET", Path: "/conv/x", Status: 500, ResponseBody: "Traceback (most recent call last):\n..."}
	if out, m := JudgeProbe(want, domain.AppCase{Kind: "http", Status: 500, ResponseBody: "internal error"}, true); out != ProbeAgree || !slices.Contains(m.Unstable, "body") {
		t.Fatalf("same 5xx status must agree with body unstable: %v %v", out, m.Unstable)
	}
	if out, _ := JudgeProbe(want, domain.AppCase{Kind: "http", Status: 200, ResponseBody: "ok"}, true); out != ProbeDisagree {
		t.Fatalf("a different status must disagree, got %v", out)
	}
}

func TestRecordAnswerMasksAndMarksExpectedCrash(t *testing.T) {
	probe := domain.AppCase{Name: "p", Kind: "cli", Argv: []string{"greet", "007"}}
	want := domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: pyTraceback}
	_, marked := JudgeProbe(want, domain.AppCase{Kind: "cli"}, true)
	c := RecordAnswer(probe, marked, "probe-round1-0")
	if c.Name != "probe-round1-0" || c.ExitCode != 1 || !slices.Contains(c.Unstable, "stderr") {
		t.Fatalf("recorded case must keep name, exit code and the stderr mark: %+v", c)
	}
	if strings.Contains(c.Stderr, "/home/alice") || !strings.Contains(c.Stderr, "ValueError: agent name is classified: 007") {
		t.Fatalf("recorded stderr must be masked and keep the exception: %q", c.Stderr)
	}
	// Under the defect policy nothing is marked, so a recorded answer is kept verbatim.
	plain := RecordAnswer(probe, domain.AppCase{Kind: "cli", ExitCode: 2, Stderr: "usage: /home/alice/x/y\n"}, "n")
	if plain.Stderr != "usage: /home/alice/x/y\n" || len(plain.Unstable) != 0 {
		t.Fatalf("a non-crash answer must be recorded as is: %+v", plain)
	}
}

func TestRequestShapeProbesAreBoundedAndSendable(t *testing.T) {
	values := []string{"", "0", "1", "007", "a b", "é", "x/y", "zz"}
	probes := RequestShapeProbes("/conv/", values, ShapeValuesPerRound)
	if len(probes) != 4*ShapeValuesPerRound {
		t.Fatalf("want %d shape probes, got %d: %+v", 4*ShapeValuesPerRound, len(probes), probes)
	}
	seen := map[string]bool{}
	labels := map[string]int{}
	for _, p := range probes {
		if seen[CaseKey(p)] {
			t.Fatalf("duplicate probe %+v", p)
		}
		seen[CaseKey(p)] = true
		labels[strings.Split(p.Name, "-")[2]]++
		if p.Path == "/conv/?x=1" || p.Path == "/conv//" {
			t.Fatalf("the empty value must not get shapes: %q", p.Path)
		}
		// Built the way AskOriginal builds it: the path and method must reach the wire unchanged.
		req, err := http.NewRequest(p.Method, "http://conv.test"+p.Path, nil)
		if err != nil {
			t.Fatalf("http.NewRequest(%q, %q): %v", p.Method, p.Path, err)
		}
		if req.Method != p.Method || req.URL.RequestURI() != p.Path {
			t.Fatalf("request changed on the way: %s %s -> %s %s", p.Method, p.Path, req.Method, req.URL.RequestURI())
		}
	}
	for _, l := range []string{"query", "dslash", "dot", "lower"} {
		if labels[l] != ShapeValuesPerRound {
			t.Fatalf("shape %s: want %d, got %d (%v)", l, ShapeValuesPerRound, labels[l], labels)
		}
	}
	if got := RequestShapeProbes("/", []string{"1"}, 3); len(got) != 3 {
		t.Fatalf("prefix / must skip the doubled slash (it would name a host): %+v", got)
	}
	if got := RequestShapeProbes("/conv/", []string{""}, 3); got != nil {
		t.Fatalf("no non-empty value, no shape probe: %+v", got)
	}
}

func TestParseExtraInputs(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"1\n007\r\n\nbob smith\n", []string{"1", "007", "bob smith"}, false},
		{`["", " 9 ", "a\nb"]`, []string{"", " 9 ", "a\nb"}, false},
		{"\n\n", nil, true},
		{"[1, 2]", nil, true},
		{`["a\u0000b"]`, nil, true},
		{"[]", nil, true},
	}
	for _, c := range cases {
		got, err := ParseExtraInputs([]byte(c.in))
		if (err != nil) != c.wantErr || !slices.Equal(got, c.want) {
			t.Errorf("ParseExtraInputs(%q) = %q, %v", c.in, got, err)
		}
	}
	if _, err := ParseExtraInputs([]byte(strings.Repeat("v\n", MaxExtraInputs+1))); err == nil {
		t.Error("more than MaxExtraInputs values must be rejected")
	}
}

func TestAnswerTextMasksCrash(t *testing.T) {
	got := AnswerText(domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: pyTraceback})
	if strings.Contains(got, "alice") || !strings.Contains(got, "ValueError: agent name is classified: 007") {
		t.Fatalf("crash answer text: %s", got)
	}
}
