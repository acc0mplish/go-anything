package domain

import (
	"slices"
	"strings"
	"testing"
)

func callGraphAnalysis() Analysis {
	return Analysis{Root: "/x", Module: "m", Packages: []Package{
		{ImportPath: "m/lib", Name: "lib", Tests: 1, Functions: []FuncInfo{
			{Name: "Leaf", Pos: "lib/a.go:3", Callers: 3, Exported: true, PlainSignature: true},
			{Name: "Busy", Pos: "lib/a.go:9", Callers: 5, Exported: true, PlainSignature: true, Callees: []string{"m/other.X"}},
			{Name: "Inner", Pos: "lib/a.go:12", Callers: 1, Exported: true, PlainSignature: true, Callees: []string{"m/lib.helper"}},
			{Name: "helper", Pos: "lib/a.go:15", Callers: 1, PlainSignature: true},
			{Name: "Dyn", Pos: "lib/a.go:20", Exported: true, PlainSignature: true, DynamicCalls: 1},
			{Name: "Spawn", Pos: "lib/a.go:25", Exported: true, PlainSignature: true, GoStatements: 1},
			{Name: "Wide", Pos: "lib/a.go:30", Exported: true},
			{Name: "Cb", Pos: "lib/a.go:35", Exported: true, Refs: 1},
			{Name: "M", Receiver: "*T", Pos: "lib/a.go:40", Exported: true, PlainSignature: true, Callers: 3},
		}, RiskSites: []RiskSite{
			{Kind: RiskInit, Pos: "lib/a.go:50"},
			{Kind: RiskGoroutine, Pos: "lib/a.go:26"},
			{Kind: RiskGoroutine, Pos: "lib/b.go:4"},
			{Kind: RiskGoroutine, Pos: "lib/a.go:100"},
			{Kind: RiskGoroutine, Pos: "lib/a.go:9"},
		}},
		{ImportPath: "m/other", Name: "other", Functions: []FuncInfo{
			{Name: "X", Pos: "other/x.go:1", Callers: 1, Exported: true, PlainSignature: true},
		}, TypeErrors: []string{"other/x.go:2: boom"}},
		{ImportPath: "m/cmd", Name: "main", Imports: []string{"unsafe"}, Functions: []FuncInfo{
			{Name: "Run", Pos: "cmd/main.go:1", Exported: true, PlainSignature: true},
		}, RiskSites: []RiskSite{{Kind: RiskUnsafe, Pos: "cmd/main.go:3", Detail: "imports unsafe"}}},
	}}
}

func TestStartHereRanksPlainLeavesByCallers(t *testing.T) {
	var got []string
	for _, s := range StartHere(callGraphAnalysis()) {
		got = append(got, s.Func)
	}
	// Busy calls into m/other, Dyn and Spawn are excluded, Wide and Cb have no plain signature,
	// helper is unexported, other.X is in a package with type errors, cmd is package main.
	// Ties on callers break by name.
	want := []string{"m/lib.Leaf", "m/lib.T.M", "m/lib.Inner"}
	if !slices.Equal(got, want) {
		t.Fatalf("start_here = %v, want %v", got, want)
	}
}

func TestUnreachedSkipsMainMethodsAndReferenced(t *testing.T) {
	got := UnreachedFuncs(callGraphAnalysis())
	want := []string{"m/lib.Dyn", "m/lib.Spawn", "m/lib.Wide"}
	if !slices.Equal(got, want) {
		t.Fatalf("unreached = %v, want %v", got, want)
	}
}

func TestRankedRisksBySeverityThenNumericLine(t *testing.T) {
	r := RankedRisks(callGraphAnalysis())
	var got []string
	for _, x := range r {
		got = append(got, x.Kind+"@"+x.Pos)
	}
	want := []string{
		"unsafe@cmd/main.go:3",
		"goroutine@lib/a.go:9", "goroutine@lib/a.go:26", "goroutine@lib/a.go:100", "goroutine@lib/b.go:4",
		"init@lib/a.go:50",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ranked risks = %v, want %v", got, want)
	}
	if r[0].Package != "m/cmd" {
		t.Fatalf("each risk carries its package: %+v", r[0])
	}
}

func TestPlanRisksNamePositionsWithoutDuplicates(t *testing.T) {
	rows := PortPlan(callGraphAnalysis())
	var lib, cmd PortRow
	for _, r := range rows {
		switch r.ImportPath {
		case "m/lib":
			lib = r
		case "m/cmd":
			cmd = r
		}
	}
	joined := strings.Join(lib.Risks, " | ")
	if !strings.Contains(joined, "goroutines: 4 go statement(s), ordering is not deterministic at lib/a.go:26, lib/b.go:4, lib/a.go:100 and 1 more") {
		t.Fatalf("goroutine risk must name the first three places: %v", lib.Risks)
	}
	if !strings.Contains(joined, "init functions: 1, run before main in import order at lib/a.go:50") {
		t.Fatalf("init risk missing: %v", lib.Risks)
	}
	if lib.Readiness != 100-10-5 {
		t.Fatalf("goroutines weigh 10 and init 5, got readiness %d", lib.Readiness)
	}
	// unsafe keeps its single existing reason, now with the place; no second unsafe line.
	n := 0
	for _, r := range cmd.Risks {
		if strings.HasPrefix(r, "unsafe or cgo") {
			n++
			if !strings.HasSuffix(r, " at cmd/main.go:3") {
				t.Fatalf("unsafe reason must carry its place: %q", r)
			}
		}
	}
	if n != 1 {
		t.Fatalf("one unsafe reason expected, got %v", cmd.Risks)
	}
}

func TestPlanRisksUnchangedWithoutSites(t *testing.T) {
	p := Package{ImportPath: "x", Imports: []string{"unsafe", "reflect"}, Tests: 1}
	want := []string{"unsafe or cgo: memory layout is not portable", "reflection: behavior depends on runtime types"}
	if got := risks(p); !slices.Equal(got, want) {
		t.Fatalf("an analysis without sites must read as before: %v", got)
	}
}

func TestFuncIDDropsPointer(t *testing.T) {
	if FuncID("m/p", "*T", "M") != "m/p.T.M" || FuncID("m/p", "T", "M") != "m/p.T.M" || FuncID("m/p", "", "F") != "m/p.F" {
		t.Fatal("FuncID format")
	}
}

func TestAnalysisHTMLShowsCallGraphSections(t *testing.T) {
	a := callGraphAnalysis()
	a.CallEdges, a.DynamicCalls = 7, 2
	a.StartHere = StartHere(a)
	a.Unreached = UnreachedFuncs(a)
	a.StartHere[0].Signature = `func(<script>x</script>)`
	b, err := NewAnalysisReport(a, 1).HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"Start here: best first ports", "m/lib.Leaf", "lib/a.go:3",
		`Static call edges</div><div class="v">7<`, `Dynamic calls</div><div class="v">2<`,
		"cannot be resolved that way", "Risks by location", "cmd/main.go:3",
		"Packages that did not type-check", "other/x.go:2: boom", "m/lib.Wide",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("analysis html missing %q", want)
		}
	}
	if strings.Contains(s, "<script>x") {
		t.Fatal("signatures come from analyzed code and must be escaped")
	}
}

func TestAnalysisHTMLCapsStartHereAtTen(t *testing.T) {
	a := Analysis{Module: "m"}
	for range 15 {
		a.StartHere = append(a.StartHere, StartItem{Func: "m.F", Pos: "a.go:1"})
	}
	a.Packages = []Package{{ImportPath: "m", Functions: []FuncInfo{{Name: "F"}}}}
	r := NewAnalysisReport(a, 1)
	if len(r.StartHere) != 10 || r.StartHereTotal != 15 {
		t.Fatalf("start here shown %d of %d", len(r.StartHere), r.StartHereTotal)
	}
}

func TestAnalysisHTMLWithoutCallGraphHasNoNewSections(t *testing.T) {
	b, _ := NewAnalysisReport(sampleAnalysis(), 1).HTML()
	s := string(b)
	for _, absent := range []string{"Start here", "Dynamic calls", "Risks by location", "did not type-check"} {
		if strings.Contains(s, absent) {
			t.Fatalf("an analysis without function data must not show %q", absent)
		}
	}
}
