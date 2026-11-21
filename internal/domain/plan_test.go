package domain

import (
	"strings"
	"testing"
)

func planFixture() Analysis {
	return Analysis{Module: "example.com/m", Packages: []Package{
		{ImportPath: "example.com/m/app", Imports: []string{"example.com/m/core", "net/http"}, Lines: 300, Tests: 1},
		{ImportPath: "example.com/m/core", Imports: []string{"fmt"}, Lines: 100, Tests: 2},
		{ImportPath: "example.com/m/leaf", Imports: []string{"unsafe"}, Lines: 50, Tests: 0},
	}}
}

func TestPortPlanPutsDependenciesFirst(t *testing.T) {
	rows := PortPlan(planFixture())
	pos := map[string]int{}
	for _, r := range rows {
		pos[r.ImportPath] = r.Order
	}
	if pos["example.com/m/core"] > pos["example.com/m/app"] {
		t.Fatalf("core is used by app, so core must come first: %+v", rows)
	}
	for _, r := range rows {
		if r.ImportPath == "example.com/m/app" && r.Wave != 1 {
			t.Fatalf("app depends on core, wave must be 1, got %d", r.Wave)
		}
	}
}

func TestReadinessReasonsAreNamed(t *testing.T) {
	rows := PortPlan(planFixture())
	var leaf PortRow
	for _, r := range rows {
		if r.ImportPath == "example.com/m/leaf" {
			leaf = r
		}
	}
	joined := strings.Join(leaf.Risks, " | ")
	if !strings.Contains(joined, "unsafe or cgo") || !strings.Contains(joined, "no tests") {
		t.Fatalf("unsafe and no-tests must both be named: %v", leaf.Risks)
	}
	if leaf.Readiness != 100-25-20 {
		t.Fatalf("readiness must be 100 minus the weights, got %d", leaf.Readiness)
	}
}

func TestReadinessWeightsOfEveryRisk(t *testing.T) {
	// every risk at once: unsafe 25, side effects 10, reflection 5, no tests 20, wide API 10 = 70 lost
	p := Package{ImportPath: "x", Imports: []string{"unsafe", "os/exec", "net/http", "reflect"}, Exported: make([]string, 30)}
	if r := readiness(p); r != 30 {
		t.Fatalf("all risks together lose 70 points, want 30, got %d", r)
	}
}

func TestWithinWaveMoreReadyComesFirst(t *testing.T) {
	a := Analysis{Module: "m", Packages: []Package{
		{ImportPath: "m/risky", Imports: []string{"os/exec"}, Tests: 0},
		{ImportPath: "m/clean", Imports: []string{"fmt"}, Tests: 1},
	}}
	rows := PortPlan(a)
	if rows[0].ImportPath != "m/clean" {
		t.Fatalf("the cleaner package must be ported first within a wave: %+v", rows)
	}
}
