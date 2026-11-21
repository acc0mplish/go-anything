package domain

import (
	"reflect"
	"testing"
)

func TestDiffAnalyses(t *testing.T) {
	prev := Analysis{Module: "m", CallEdges: 10, DynamicCalls: 4, StartHere: []StartItem{{}, {}}, Packages: []Package{
		{ImportPath: "m/keep", Lines: 100, Functions: []FuncInfo{{}, {}},
			RiskSites: []RiskSite{{Kind: RiskGoroutine, Pos: "keep/a.go:10"}}},
		{ImportPath: "m/gone", Lines: 50, Functions: []FuncInfo{{}},
			RiskSites: []RiskSite{{Kind: RiskInit, Pos: "gone/a.go:1"}}},
	}}
	cur := Analysis{Module: "m", CallEdges: 12, DynamicCalls: 3, StartHere: []StartItem{{}}, Packages: []Package{
		{ImportPath: "m/keep", Lines: 120, Functions: []FuncInfo{{}, {}, {}}, RiskSites: []RiskSite{
			{Kind: RiskGoroutine, Pos: "keep/a.go:10"}, // unchanged
			{Kind: RiskReflect, Pos: "keep/a.go:10"},   // same place, new kind
			{Kind: RiskGoroutine, Pos: "keep/a.go:30"}, // same kind, new place
		}},
		{ImportPath: "m/new", Lines: 5, RiskSites: []RiskSite{{Kind: RiskUnsafe, Pos: "new/b.go:2"}}},
		{ImportPath: "m/also", Lines: 1},
	}}
	d := DiffAnalyses(prev, cur)
	if d.PrevVersion != 0 || d.Packages != 1 || d.Lines != -24 || d.Functions != 0 ||
		d.CallEdges != 2 || d.DynamicCalls != -1 || d.Risks != 2 || d.StartHere != -1 {
		t.Fatalf("counts wrong: %+v", d)
	}
	if !reflect.DeepEqual(d.AddedPackages, []string{"m/also", "m/new"}) || !reflect.DeepEqual(d.RemovedPackages, []string{"m/gone"}) {
		t.Fatalf("added %v removed %v", d.AddedPackages, d.RemovedPackages)
	}
	var got []string
	for _, r := range d.NewRisks {
		got = append(got, r.Kind+"@"+r.Pos+"@"+r.Package)
	}
	want := []string{"unsafe@new/b.go:2@m/new", "goroutine@keep/a.go:30@m/keep", "reflect@keep/a.go:10@m/keep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("new risks %v, want %v (most serious first)", got, want)
	}
}

func TestDiffAnalysesSameIsEmpty(t *testing.T) {
	a := sampleAnalysis()
	a.Packages[0].RiskSites = []RiskSite{{Kind: RiskInit, Pos: "small/a.go:1"}}
	d := DiffAnalyses(a, a)
	if !reflect.DeepEqual(d, AnalysisDelta{}) {
		t.Fatalf("identical analyses must give an empty delta, got %+v", d)
	}
}

func TestDiffAnalysesDoesNotChangeInputs(t *testing.T) {
	prev, cur := sampleAnalysis(), sampleAnalysis()
	cur.Packages = cur.Packages[:1]
	before := len(prev.Packages)
	_ = DiffAnalyses(prev, cur)
	if len(prev.Packages) != before || prev.Packages[0].ImportPath != "example.com/m/small" {
		t.Fatal("DiffAnalyses must not change its inputs")
	}
}
