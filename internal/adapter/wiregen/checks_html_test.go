package wiregen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/usecase"
)

// allClassesTried marks every round as having probed every input class, the condition for a green banner.
func allClassesTried(rs []RoundCheck) []RoundCheck {
	for i := range rs {
		rs[i].MissingClasses = nil
	}
	return rs
}

// syntheticRounds is a run of three rounds with the given disagreements, as Refine would record it.
func syntheticRounds(disagreements ...int) []RoundCheck {
	var out []RoundCheck
	streak := 0
	for i, d := range disagreements {
		round := i + 1
		var newCases []string
		if d > 0 {
			newCases = []string{"probe-round1-0"}
		}
		streak = nextCleanStreak(streak, d == 0)
		converged, _ := convergence(streak, len(disagreements))
		c := newRoundCheck(round, uint64(10+round), []string{"0", "1", "x"}, 3, d, newCases, 0, converged)
		c.CleanStreak = streak
		c.DefectPolicy = string(usecase.PolicyDefect) // refine always records the policy it ran with
		if d > 0 {
			c.AgentOutcome = "passed the recorded cases"
		}
		out = append(out, c)
	}
	return out
}

func TestRefineBanner(t *testing.T) {
	notRun := syntheticRounds(0, 3)
	notRun[1].AgentOutcome = "not run: no agent command was given"
	single := syntheticRounds(0)
	critical := syntheticRounds(0)
	critical[0].Converged, critical[0].CleanStreak = false, 0
	critical[0].RBACChecked, critical[0].RBACPrincipals = true, 2
	critical[0].RBACFindings = []usecase.Finding{{Severity: usecase.SeverityCritical, Case: "c", Principal: "bob", Detail: "d"}}
	shortRun := syntheticRounds(4, 0, 0)[:2] // round 2 is clean, streak 1, not converged

	tests := []struct {
		name      string
		rounds    []RoundCheck
		defects   []usecase.OriginalDefect
		class     string
		verdict   []string
		nextHas   string
		nextEmpty bool
	}{
		{name: "converged after two clean rounds", rounds: allClassesTried(syntheticRounds(5, 0, 0)), class: "pass",
			verdict: []string{"Converged after 2 consecutive clean rounds", "seeds 12 and 13"}, nextEmpty: true},
		{name: "disagreement in last round", rounds: syntheticRounds(5, 0, 2), class: "fail",
			verdict: []string{"Not converged", "2 disagreement(s)", "round 3"}, nextHas: "Run more rounds"},
		{name: "no agent ran", rounds: notRun, class: "fail",
			verdict: []string{"3 disagreement(s)"}, nextHas: "No agent ran"},
		{name: "single round", rounds: single, class: "warn",
			verdict: []string{"1 clean round", "not yet converged", "seed 11"}, nextHas: "-rounds 2"},
		{name: "clean but streak short", rounds: shortRun, class: "warn",
			verdict: []string{"clean streak 1 of 2"}, nextHas: "Run more rounds"},
		{name: "critical authorization finding", rounds: critical, class: "fail",
			verdict: []string{"1 critical authorization finding(s)"}, nextHas: "authorization findings"},
		{name: "defects add a step", rounds: allClassesTried(syntheticRounds(5, 0, 0)), class: "pass",
			defects: []usecase.OriginalDefect{{Request: "r", Evidence: "exit 1: E"}},
			verdict: []string{"Converged"}, nextHas: "1 original defect(s)"},
		{name: "nothing recorded", rounds: nil, class: "info", verdict: []string{"No round recorded yet"}, nextHas: "wire refine"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := BuildRefineReport("greet", tc.rounds, tc.defects, "now", 0)
			class, verdict, _, next := refineBanner(r)
			if class != tc.class {
				t.Errorf("class = %q, want %q", class, tc.class)
			}
			for _, w := range tc.verdict {
				if !strings.Contains(verdict, w) {
					t.Errorf("verdict %q does not contain %q", verdict, w)
				}
			}
			if tc.nextEmpty && len(next) != 0 {
				t.Errorf("want no next step, got %q", next)
			}
			if tc.nextHas != "" && !strings.Contains(strings.Join(next, " "), tc.nextHas) {
				t.Errorf("next steps %q do not contain %q", next, tc.nextHas)
			}
		})
	}
}

func renderPage(t *testing.T, r RefineReport) string {
	t.Helper()
	data, err := RenderRefineHTML(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRefinePageConverged(t *testing.T) {
	r := BuildRefineReport("greet", allClassesTried(syntheticRounds(5, 0, 0)), nil, "2026-01-02 03:04:05 UTC", 0)
	if !r.Converged || r.ConvergedAt != 3 {
		t.Fatalf("want converged at round 3, got %v %d", r.Converged, r.ConvergedAt)
	}
	page := renderPage(t, r)
	for _, want := range []string{
		"<title>RSI loop: greet</title>", "3 round(s) run", "banner pass",
		"Converged after 2 consecutive clean rounds (seeds 12 and 13)",
		"5 → 0 → 0", "width: 100%", "width: 0%", "Not checked", "outside the probe classes",
		"NOT recorded as expected behavior",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script") {
		t.Error("the page must not contain scripts")
	}
}

func TestRefinePageNotConvergedNamesCount(t *testing.T) {
	page := renderPage(t, BuildRefineReport("greet", syntheticRounds(5, 0, 4), nil, "now", 0))
	if !strings.Contains(page, "banner fail") || !strings.Contains(page, "Not converged: 4 disagreement(s) with the original in round 3") {
		t.Fatalf("a dirty last round must say not converged and name the count:\n%s", page)
	}
}

func TestRefinePageMissingClassesAreWarnBadges(t *testing.T) {
	page := renderPage(t, BuildRefineReport("greet", syntheticRounds(0, 0), nil, "now", 0))
	for _, class := range []string{"empty", "overflow", "unicode-digit"} {
		if !strings.Contains(page, `<span class="badge warn">`+class+`</span>`) {
			t.Errorf("missing class %q must be a warn badge", class)
		}
	}
	if strings.Contains(page, `<span class="badge warn">ascii-digits</span>`) {
		t.Error("a covered class must not be a warn badge")
	}
}

func TestRefinePageEscapesDefectInput(t *testing.T) {
	d := []usecase.OriginalDefect{{Request: `args=["greet" "<script>alert(1)</script>"]`, Evidence: "exit 1: ValueError: <b>bad</b>"}}
	page := renderPage(t, BuildRefineReport("greet", syntheticRounds(0), d, "now", 0))
	if strings.Contains(page, "<script>alert(1)") || strings.Contains(page, "<b>bad</b>") {
		t.Fatal("defect values must be escaped")
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("the escaped input must be shown:\n%s", page)
	}
	if !strings.Contains(page, "The original crashed") {
		t.Error("a crash defect must say why it is a defect")
	}
}

func TestRefinePageClipsLongValues(t *testing.T) {
	d := []usecase.OriginalDefect{{Request: "r", Evidence: "exit 1: " + strings.Repeat("A", 5000)}}
	page := renderPage(t, BuildRefineReport("greet", syntheticRounds(0), d, "now", 0))
	if strings.Contains(page, strings.Repeat("A", 2100)) || !strings.Contains(page, "more bytes not shown") {
		t.Fatal("a recorded value longer than the limit must be clipped")
	}
}

func TestRefinePageRBACFindings(t *testing.T) {
	rs := syntheticRounds(0, 0)
	rs[1].RBACChecked, rs[1].RBACPrincipals = true, 3
	rs[1].RBACFindings = []usecase.Finding{{Severity: usecase.SeverityHigh, Case: "list", Principal: "alice", Detail: "status differs"}}
	page := renderPage(t, BuildRefineReport("orders", rs, nil, "now", 0))
	if !strings.Contains(page, `<span class="badge warn">high</span>`) || !strings.Contains(page, "Checked as 3 principal(s)") {
		t.Fatalf("RBAC findings must show with severity badges:\n%s", page)
	}
}

// koreanFor returns the Korean half of the Bi pair whose English half is exactly en, or "".
func koreanFor(page, en string) string {
	prefix := `<span class="bi-en">` + en + `</span><span class="bi-ko" lang="ko">`
	i := strings.Index(page, prefix)
	if i < 0 {
		return ""
	}
	rest := page[i+len(prefix):]
	if j := strings.Index(rest, "</span>"); j >= 0 {
		return rest[:j]
	}
	return ""
}

// The RSI page's fixed headings, card titles and table headers carry English first and Korean
// second, while the recorded data (seeds, counts, case names) stays exactly as it was.
func TestRefinePageIsBilingual(t *testing.T) {
	page := renderPage(t, BuildRefineReport("greet", allClassesTried(syntheticRounds(5, 0, 0)), nil, "2026-01-02 03:04:05 UTC", 0))
	tests := []struct{ name, en, ko string }{
		{"banner verdict", "Converged after 2 consecutive clean rounds (seeds 12 and 13)", "연속 2개 통과 라운드 후 수렴 (seeds 12 and 13)"},
		{"not checked heading", "Not checked", "확인하지 않은 항목"},
		{"defects heading", "Original defects", "원본 결함"},
		{"classes heading", "Value classes", "값 클래스"},
		{"card title", "Clean streak", "연속 통과"},
		{"rounds table header", "Round", "라운드"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(page, tc.en) {
				t.Fatalf("the English text must stay primary, missing %q", tc.en)
			}
			if got := koreanFor(page, tc.en); got != tc.ko {
				t.Fatalf("%q Korean = %q, want %q", tc.en, got, tc.ko)
			}
		})
	}
	if !strings.Contains(page, "safe to delete") || !strings.Contains(page, "삭제해도 안전합니다") {
		t.Fatal("the footer note must appear in English and in Korean")
	}
	if !strings.Contains(page, "5 → 0 → 0") {
		t.Fatal("recorded data must stay exactly as it was")
	}
}

// TestRefineBilingualEvidenceLines renders one RSI page with realistic data and logs one English
// line and its Korean pair, as evidence for the bilingual requirement.
func TestRefineBilingualEvidenceLines(t *testing.T) {
	page := renderPage(t, BuildRefineReport("greet", allClassesTried(syntheticRounds(5, 0, 0)), nil, "2026-01-02 03:04:05 UTC", 0))
	t.Logf("rsi EN line: %s", "Not checked")
	t.Logf("rsi KO line: %s", koreanFor(page, "Not checked"))
	t.Logf("rsi banner EN line: %s", "Converged after 2 consecutive clean rounds (seeds 12 and 13)")
	t.Logf("rsi banner KO line: %s", koreanFor(page, "Converged after 2 consecutive clean rounds (seeds 12 and 13)"))
}

func writeRound(t *testing.T, root, unit string, c RoundCheck, mod time.Time) {
	t.Helper()
	p, err := WriteRoundCheck(root, unit, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRefineHTMLFromDiskIgnoresOlderRun(t *testing.T) {
	root := t.TempDir()
	rs := syntheticRounds(5, 0, 0)
	base := time.Now()
	stale := rs[0]
	stale.Round = 4
	writeRound(t, root, "greet", stale, base.Add(-time.Hour)) // left by an earlier, longer run
	for i, c := range rs {
		writeRound(t, root, "greet", c, base.Add(time.Duration(i)*time.Second))
	}
	defectsPath := filepath.Join(root, "app.json.defects.json")
	data, _ := json.Marshal([]usecase.OriginalDefect{{Request: "args=x", Evidence: "exit 1: ZeroDivisionError"}})
	if err := os.WriteFile(defectsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := WriteRefineHTML(root, "greet", defectsPath, base)
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 round(s) run", "1 older round record(s)", "ZeroDivisionError", "Converged after 2"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestWriteRefinePageFailureLogsAndContinues(t *testing.T) {
	root := t.TempDir()
	writeRound(t, root, "greet", syntheticRounds(0)[0], time.Now())
	dir := filepath.Join(root, ".go-anything", "checks", "greet")
	// index.html as a directory makes the write fail even for root; the read-only mode covers other users.
	if err := os.Mkdir(filepath.Join(dir, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	var log bytes.Buffer
	logf := func(format string, args ...any) {
		log.WriteString(strings.TrimSpace(fmt.Sprintf(format, args...)) + "\n")
	}
	writeRefinePage(root, "greet", "", logf) // returns nothing: a failure cannot stop the run
	out := log.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "rsi page not written (the run continues)") {
		t.Fatalf("want one log line about the failed page, got %q", out)
	}
	if _, err := WriteRefineHTML(root, "greet", "", time.Now()); err == nil {
		t.Fatal("the write itself must report the failure to its caller")
	}
}

// Refine rebuilds the page after each round, so a converged run leaves a converged page.
func TestRefineWritesThePage(t *testing.T) {
	root, orig, casesPath := correctRewriteModule(t)
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 4, Probes: 8, MaxNew: 20, Seed: 1, Log: &logBuf,
	})
	if err != nil || !ok {
		t.Fatalf("a correct rewrite must converge: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	if strings.Count(logBuf.String(), "rsi page: ") != 2 {
		t.Fatalf("want the page rebuilt after each of the 2 rounds:\n%s", logBuf.String())
	}
	page, err := os.ReadFile(filepath.Join(root, ".go-anything", "checks", "orders", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "Converged after 2 consecutive clean rounds (seeds 2 and 3)") {
		t.Fatalf("the page must show the convergence of the run:\n%s", page)
	}
}

func TestRefineHTMLViewport(t *testing.T) {
	data, err := RenderRefineHTML(RefineReport{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name="viewport"`) {
		t.Fatal("the refine page must carry the viewport meta for narrow screens")
	}
}
