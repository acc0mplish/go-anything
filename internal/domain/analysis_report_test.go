package domain

import (
	"strings"
	"testing"
)

func sampleAnalysis() Analysis {
	return Analysis{Root: "/x", Module: "example.com/m", Packages: []Package{
		{ImportPath: "example.com/m/small", Dir: "small", Files: []string{"a.go"}, Lines: 10, Exported: []string{"A"}, Funcs: []string{"A"}},
		{ImportPath: "example.com/m/big", Dir: "big", Files: []string{"b.go", "c.go"}, Lines: 500, Exported: []string{"B", "C"}, Funcs: []string{"B", "C", "d"}, Internal: true},
	}}
}

func TestNewAnalysisReportOrdersLargestFirstAndTotals(t *testing.T) {
	r := NewAnalysisReport(sampleAnalysis(), 1)
	if r.Packages[0].ImportPath != "example.com/m/big" {
		t.Fatalf("largest package must come first, got %s", r.Packages[0].ImportPath)
	}
	if r.Lines != 510 || r.Exported != 3 {
		t.Fatalf("totals wrong: lines=%d exported=%d", r.Lines, r.Exported)
	}
}

func TestAnalysisHTMLHasVersionAndRows(t *testing.T) {
	b, err := NewAnalysisReport(sampleAnalysis(), 2).HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"<title>Analysis v2: example.com/m</title>", "example.com/m/big", "internal", ">500<"} {
		if !strings.Contains(s, want) {
			t.Fatalf("analysis html missing %q", want)
		}
	}
}

func TestAnalysisHTMLEscapesPathsFromCode(t *testing.T) {
	a := Analysis{Root: "/x", Module: "m", Packages: []Package{
		{ImportPath: `m/<script>x</script>`, Dir: `d"><img src=x>`, Lines: 1},
	}}
	b, _ := NewAnalysisReport(a, 1).HTML()
	s := string(b)
	if strings.Contains(s, "<script>x") || strings.Contains(s, "<img src=x>") {
		t.Fatalf("names from analyzed code must be escaped:\n%s", s)
	}
}

func TestAnalysisHTMLIsSelfContained(t *testing.T) {
	b, _ := NewAnalysisReport(sampleAnalysis(), 1).HTML()
	s := strings.ToLower(string(b))
	if strings.Contains(s, "<script") || strings.Contains(s, "src=\"http") {
		t.Fatal("analysis report must have no script and no external assets")
	}
}

func TestAnalysisHTMLStripsModulePrefixButKeepsTitle(t *testing.T) {
	a := Analysis{Root: "/x", Module: "github.com/o/r", Packages: []Package{
		{ImportPath: "github.com/o/r/internal/lib", Dir: "internal/lib", Lines: 5, Tests: 1,
			Functions: []FuncInfo{{Name: "Leaf", Exported: true, PlainSignature: true, Pos: "internal/lib/a.go:3"}}},
	}}
	a.StartHere = StartHere(a)
	b, err := NewAnalysisReport(a, 1).HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`title="github.com/o/r/internal/lib">internal/lib<`,
		`title="github.com/o/r/internal/lib.Leaf">internal/lib.Leaf<`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, ">github.com/o/r/internal/lib<") {
		t.Fatal("a cell must not show the module prefix")
	}
}

func TestRelPath(t *testing.T) {
	for _, c := range []struct{ module, in, want string }{
		{"m/x", "m/x/a/b", "a/b"},
		{"m/x", "m/x/a.F", "a.F"},
		{"m/x", "m/x", "(root)"},
		{"m/x", "m/x.F", "(root).F"},
		{"m/x", "m/xy/a", "m/xy/a"},
		{"", "m/x/a", "m/x/a"},
	} {
		if got := RelPath(c.module, c.in); got != c.want {
			t.Errorf("RelPath(%q, %q) = %q, want %q", c.module, c.in, got, c.want)
		}
	}
}

func TestRelText(t *testing.T) {
	for _, c := range []struct{ module, in, want string }{
		{"g.com/o/r", "func(opts Options) []g.com/o/r/internal/domain.GateCheck", "func(opts Options) []internal/domain.GateCheck"},
		{"g.com/o/r", "func(a *g.com/o/r/x.T, b map[string]g.com/o/r/y.U)", "func(a *x.T, b map[string]y.U)"},
		{"m", "func(x example.com/m/x.T) m/y.U", "func(x example.com/m/x.T) y.U"},
		{"", "func()", "func()"},
	} {
		if got := RelText(c.module, c.in); got != c.want {
			t.Errorf("RelText(%q, %q) = %q, want %q", c.module, c.in, got, c.want)
		}
	}
}

func TestReadinessBarClassBoundaries(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{59, "bar low"}, {60, "bar mid"}, {89, "bar mid"}, {90, "bar"}, {0, "bar low"}, {100, "bar"}} {
		if got := ReadinessBarClass(c.n); got != c.want {
			t.Errorf("ReadinessBarClass(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	b, _ := NewAnalysisReport(sampleAnalysis(), 1).HTML() // both packages lack tests: readiness 80
	if !strings.Contains(string(b), `<span class="bar mid"><span style="width: 80%"></span></span>`) {
		t.Fatalf("readiness 80 must render a mid bar 80%% wide:\n%s", b)
	}
}

func TestAnalysisBottomLine(t *testing.T) {
	const m = "m"
	order := []PortRow{
		{Wave: 0, Readiness: 100, ImportPath: "m/a"},
		{Wave: 0, Readiness: 95, ImportPath: "m/b"},
		{Wave: 0, Readiness: 70, ImportPath: "m/c"},
		{Wave: 1, Readiness: 40, ImportPath: "m/d", Risks: []string{"unsafe or cgo: memory layout is not portable at d.go:1", "no tests of its own: record its behavior before porting"}},
		{Wave: 2, Readiness: 55, ImportPath: "m/e", Risks: []string{"goroutines: 2 go statement(s), ordering is not deterministic"}},
	}
	start := []StartItem{{Func: "m/a.F1"}, {Func: "m/a.F2"}, {Func: "m/b.T.M"}, {Func: "m/b.F4"}}
	for _, c := range []struct {
		name   string
		order  []PortRow
		start  []StartItem
		delta  *AnalysisDelta
		want   []string
		absent []string
	}{
		{
			name: "ready wave 0, functions, hold back", order: order, start: start,
			want: []string{
				"Start with wave 0: 2 of its 3 packages are ready (readiness >= 90): a and b.",
				"First functions: a.F1, a.F2 and b.T.M (leaf functions with plain signatures, most called first).",
				"Hold back the lowest readiness, below 60: d (40: unsafe or cgo, no tests of its own), e (55: goroutines).",
			},
			absent: []string{"b.F4", "Since v"},
		},
		{
			name: "nothing ready, nothing to hold", order: []PortRow{{Wave: 0, Readiness: 80, ImportPath: "m/x"}, {Wave: 0, Readiness: 85, ImportPath: "m/y"}},
			want: []string{
				"Start with wave 0 (2 packages with no imports inside the module); none reaches readiness 90, the most ready is y at 85.",
				"No package scores below 60; port the lowest with care: x (80: no recorded reason), y (85: no recorded reason).",
			},
			absent: []string{"First functions"},
		},
		{
			name: "with delta", order: order[:1],
			delta: &AnalysisDelta{PrevVersion: 3, Lines: -12, RemovedPackages: []string{"m/z"}, NewRisks: []LocatedRisk{{}, {}}},
			want:  []string{"Since v3: 1 package removed, lines -12, 2 new risk sites."},
		},
		{
			name:  "all ready",
			order: []PortRow{{Wave: 0, Readiness: 100, ImportPath: "m/x"}},
			want:  []string{"Every package reaches readiness 90, so none has to be held back."},
		},
		{
			name: "no packages", want: []string{"The analysis found no packages, so there is nothing to port yet."},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := AnalysisBottomLine(m, c.order, c.start, c.delta)
			if len(got) < 1 || len(got) > 4 {
				t.Fatalf("want 1 to 4 sentences, got %d: %q", len(got), got)
			}
			all := strings.Join(got, "\n")
			for _, w := range c.want {
				if !strings.Contains(all, w) {
					t.Errorf("missing %q in\n%s", w, all)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(all, a) {
					t.Errorf("must not contain %q:\n%s", a, all)
				}
			}
		})
	}
}

func TestAnalysisHTMLBottomLineNamesWaveZeroAndStartHere(t *testing.T) {
	a := Analysis{Root: "/x", Module: "m", Packages: []Package{
		{ImportPath: "m/core", Tests: 1, Lines: 10, Functions: []FuncInfo{
			{Name: "Parse", Exported: true, PlainSignature: true, Callers: 3, Pos: "core/a.go:1"},
			{Name: "Join", Exported: true, PlainSignature: true, Callers: 1, Pos: "core/a.go:9"},
		}},
		{ImportPath: "m/app", Tests: 0, Lines: 10, Imports: []string{"m/core", "unsafe"}},
	}}
	a.StartHere = StartHere(a)
	b, err := NewAnalysisReport(a, 1).HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	banner := s[strings.Index(s, `id="bottom-line"`):strings.Index(s, `id="changes"`)]
	for _, want := range []string{"ready (readiness &gt;= 90): core.", "First functions: core.Parse and core.Join", "app (55: unsafe or cgo, no tests of its own)"} {
		if !strings.Contains(banner, want) {
			t.Fatalf("bottom line missing %q:\n%s", want, banner)
		}
	}
	if strings.Index(s, `id="summary"`) > strings.Index(s, `id="bottom-line"`) {
		t.Fatal("the KPI summary must come before the bottom line")
	}
	if strings.Index(s, `id="bottom-line"`) > strings.Index(s, `id="start-here"`) {
		t.Fatal("bottom line must come before the tables")
	}
}

func TestAnalysisHTMLFirstAnalysisNote(t *testing.T) {
	b, _ := NewAnalysisReport(sampleAnalysis(), 1).HTML()
	if !strings.Contains(string(b), "This is the first analysis of this module") {
		t.Fatal("version 1 without a delta must say it is the first analysis")
	}
	b, _ = NewAnalysisReport(sampleAnalysis(), 4).HTML()
	s := string(b)
	if strings.Contains(s, "first analysis") || !strings.Contains(s, "No data from an earlier version") {
		t.Fatal("a later version without a delta must not claim to be the first analysis")
	}
}

func TestAnalysisHTMLShowsChangesSincePrevious(t *testing.T) {
	prev := sampleAnalysis()
	prev.Packages = append(prev.Packages, Package{ImportPath: "example.com/m/gone", Lines: 7})
	cur := sampleAnalysis()
	cur.Packages[0].Functions = []FuncInfo{{Name: "A"}}
	cur.Packages[0].RiskSites = []RiskSite{{Kind: RiskGoroutine, Pos: "small/a.go:4", Detail: "go worker()"}}
	b, err := NewAnalysisReport(cur, 3).WithPrevious(prev, 2).HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`<h2 id="changes"><span class="bi-en">Changes since v2</span>`, "Removed packages (1)", `title="example.com/m/gone">gone<`,
		"New risk sites (1)", "small/a.go:4", `<div class="k"><span class="bi-en">Packages</span><span class="bi-ko-inl" lang="ko">패키지</span></div><div class="v">2</div><div class="s">-1 vs v2</div>`,
		`<div class="k"><span class="bi-en">Risk sites</span><span class="bi-ko-inl" lang="ko">위험 지점</span></div><div class="v">1</div><div class="s">&#43;1 vs v2</div>`, // html/template escapes "+"
		"Since v2: 1 package removed, lines -7, 1 new risk site.",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "first analysis") {
		t.Fatal("a report with a delta must not show the first-analysis note")
	}
}

func TestAnalysisHTMLExplainsRiskKinds(t *testing.T) {
	a := Analysis{Module: "m", Packages: []Package{{ImportPath: "m/p", Functions: []FuncInfo{{Name: "F"}},
		RiskSites: []RiskSite{{Kind: RiskUnsafe, Pos: "p/a.go:1"}, {Kind: "weird", Pos: "p/a.go:2"}}}}}
	r := NewAnalysisReport(a, 1)
	if len(r.RiskKinds) != 2 || r.RiskKinds[0].Kind != RiskUnsafe || r.RiskKinds[0].Why == "" || r.RiskKinds[1].Why != "" {
		t.Fatalf("risk kinds: %+v", r.RiskKinds)
	}
	b, err := r.HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, RiskWhy(RiskUnsafe)) || !strings.Contains(s, `weird</span> 1</li>`) {
		t.Fatal("known kinds show their explanation, unknown kinds show none")
	}
	for _, k := range []string{RiskGoroutine, RiskUnsafe, RiskReflect, RiskCgo, RiskGlobalMutable, RiskInit, "side_effects"} {
		if RiskWhy(k) == "" {
			t.Errorf("kind %q has no explanation", k)
		}
	}
}

func TestAnalysisHTMLGroupsWaves(t *testing.T) {
	a := Analysis{Module: "m", Packages: []Package{
		{ImportPath: "m/a", Tests: 1}, {ImportPath: "m/b", Tests: 1, Imports: []string{"m/a"}},
	}}
	b, _ := NewAnalysisReport(a, 1).HTML()
	s := string(b)
	w0, w1 := strings.Index(s, "<h3>Wave 0 "), strings.Index(s, "<h3>Wave 1 ")
	if w0 < 0 || w1 < w0 {
		t.Fatalf("each wave needs its own heading, in order:\n%s", s)
	}
}

func TestAnalysisHTMLEscapesScriptInPackagePath(t *testing.T) {
	a := Analysis{Root: "/x", Module: "m", Packages: []Package{
		{ImportPath: `m/<script>alert(1)</script>`, Lines: 1, Functions: []FuncInfo{{Name: "F"}},
			RiskSites: []RiskSite{{Kind: RiskInit, Pos: "a.go:1"}}},
	}}
	cur := NewAnalysisReport(a, 2).WithPrevious(Analysis{Module: "m"}, 1)
	b, err := cur.HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(strings.ToLower(s), "<script") {
		t.Fatalf("a package path must be escaped everywhere, including the delta and the bottom line:\n%s", s)
	}
	if !strings.Contains(s, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("the escaped path must still be shown")
	}
}

func TestWithPreviousIgnoresOtherModule(t *testing.T) {
	r := NewAnalysisReport(sampleAnalysis(), 2).WithPrevious(Analysis{Module: "other"}, 1)
	if r.Delta != nil {
		t.Fatal("a previous analysis of another module must not produce a delta")
	}
}

// The explanation block states the points readiness takes off; they must match readiness itself.
func TestReadinessWeightsMatchPlan(t *testing.T) {
	base := Package{Tests: 1}
	cases := map[string]Package{
		"unsafe or cgo":         {Tests: 1, Imports: []string{"unsafe"}},
		"no tests of its own":   {},
		"external side effects": {Tests: 1, Imports: []string{"net/http"}},
		"goroutines":            {Tests: 1, RiskSites: []RiskSite{{Kind: RiskGoroutine}}},
		"wide API":              {Tests: 1, Exported: make([]string, 21)},
		"reflection":            {Tests: 1, Imports: []string{"reflect"}},
		"global mutable state":  {Tests: 1, RiskSites: []RiskSite{{Kind: RiskGlobalMutable}}},
		"init functions":        {Tests: 1, RiskSites: []RiskSite{{Kind: RiskInit}}},
	}
	if readiness(base) != 100 {
		t.Fatalf("a tested package without risks must score 100, got %d", readiness(base))
	}
	if len(cases) != len(readinessWeights) {
		t.Fatalf("explained %d reasons, tested %d", len(readinessWeights), len(cases))
	}
	for _, w := range readinessWeights {
		var p Package
		found := false
		for prefix, pk := range cases {
			if strings.HasPrefix(w.Reason, prefix) {
				p, found = pk, true
			}
		}
		if !found {
			t.Fatalf("no case for reason %q", w.Reason)
		}
		if got := 100 - readiness(p); got != w.Points {
			t.Errorf("%s: readiness takes off %d, the report says %d", w.Reason, got, w.Points)
		}
	}
}

func TestCardChangeReadsNoChangeForZero(t *testing.T) {
	if Change(0) != "no change" || Change(3) != "+3" || Change(-2) != "-2" {
		t.Fatalf("Change: got %q %q %q", Change(0), Change(3), Change(-2))
	}
}

func TestAnalysisReportViewport(t *testing.T) {
	b, err := NewAnalysisReport(sampleAnalysis(), 1).HTML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `name="viewport"`) {
		t.Fatal("the analysis report must carry the viewport meta for narrow screens")
	}
}
