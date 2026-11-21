package domain

import (
	"strings"
	"testing"
)

// The explanation block for a non-Go original states the points readiness takes off; they must
// match readiness itself.
func TestAnalysisOriginalReadinessWeightsMatchPlan(t *testing.T) {
	site := func(kind string) Package {
		return Package{GoPackage: "internal/x", Tests: 1, RiskSites: []RiskSite{{Kind: kind, Pos: "x.py:1"}}}
	}
	cases := map[string]Package{
		"native code":            site(RiskNativeCode),
		"no tests of its own":    {GoPackage: "internal/x"},
		"did not parse":          {GoPackage: "internal/x", Tests: 1, TypeErrors: []string{"x.py:1: invalid syntax"}},
		"external side effects":  site(RiskSideEffects),
		"dynamic code":           site(RiskDynamicCode),
		"import cycle":           site(RiskImportCycle),
		"concurrency":            site(RiskConcurrency),
		"wide API":               {GoPackage: "internal/x", Tests: 1, Exported: make([]string, 21)},
		"global mutable state":   site(RiskGlobalMutable),
		"unsafe deserialization": site(RiskUnsafeDeserialize),
		"nondeterminism":         site(RiskNondeterminism),
	}
	if len(cases) != len(originalReadinessWeights) {
		t.Fatalf("explained %d reasons, tested %d", len(originalReadinessWeights), len(cases))
	}
	for _, w := range originalReadinessWeights {
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
	// Network and file writes join processes in the one side effects reason, counted once.
	p := Package{GoPackage: "internal/x", Tests: 1, RiskSites: []RiskSite{
		{Kind: RiskSideEffects, Pos: "a.py:1"}, {Kind: RiskNetwork, Pos: "a.py:2"}, {Kind: RiskFSWrite, Pos: "a.py:3"},
	}}
	if r := risks(p); len(r) != 1 || r[0] != "external side effects: processes, network, file writes at a.py:1, a.py:2, a.py:3" || readiness(p) != 90 {
		t.Fatalf("side effects: %v readiness %d", r, readiness(p))
	}
}

// A Go package that fails to type-check is not marked "did not parse": that reason is for
// originals, whose broken files have no data at all.
func TestAnalysisGoTypeErrorsKeepTheirReadiness(t *testing.T) {
	p := Package{Tests: 1, TypeErrors: []string{"a.go:1: undefined: x"}}
	if readiness(p) != 100 {
		t.Fatalf("Go readiness changed: %d %v", readiness(p), risks(p))
	}
}

func TestAnalysisReportForPythonOriginal(t *testing.T) {
	a := Analysis{
		Root: "/r/shop", Module: "shop", Language: "python",
		Limits: []string{"Parsed with Python's own ast module."},
		Packages: []Package{
			{ImportPath: "shop.price", Dir: "shop", Files: []string{"price.py"}, Lines: 10, Tests: 1,
				Funcs: []string{"total"}, Exported: []string{"total"}, GoPackage: "internal/shop/price",
				Functions: []FuncInfo{{Name: "total", Pos: "shop/price.py:4", Exported: true, Callers: 1, Signature: "(items)"}}},
			{ImportPath: "shop.cart", Dir: "shop", Files: []string{"cart.py"}, Lines: 20, Imports: []string{"shop.price"},
				GoPackage: "internal/shop/cart",
				Functions: []FuncInfo{{Name: "checkout", Pos: "shop/cart.py:9", Exported: true, Callees: []string{"shop.price.total"}}},
				RiskSites: []RiskSite{{Kind: RiskConcurrency, Pos: "shop/cart.py:2", Detail: "imports threading"}}},
			{ImportPath: "shop.broken", Dir: "shop", Files: []string{"broken.py"}, Lines: 5, GoPackage: "internal/shop/broken",
				TypeErrors: []string{"shop/broken.py:4: invalid syntax"}},
		},
		CallEdges: 1,
		StartHere: []StartItem{{Func: "shop.price.total", Package: "shop.price", Pos: "shop/price.py:4", Callers: 1, Signature: "(items)", Importers: 1}},
	}
	b, err := NewAnalysisReport(a, 1).HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, gone := range []string{"goroutine", "package main", "non-test Go lines", "type-check", "Dynamic calls", "_test.go", "relative to the module"} {
		if strings.Contains(s, gone) {
			t.Errorf("Go-only wording %q in a Python report", gone)
		}
	}
	for _, want := range []string{
		"non-test Python lines", "Modules, largest first", "internal/shop/price", "→ internal/shop/cart",
		"Parsed with Python&#39;s own ast module.", "Modules that did not parse", "shop/broken.py:4: invalid syntax",
		RiskWhy(RiskConcurrency), "<td class=\"num\">1</td>", // the importers of total
		"Start with wave 0", "modules are ready", "concurrency: 1 site(s)",
	} {
		if !strings.Contains(s, template2HTML(want)) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "(root)") {
		t.Error("an original's units are not relative to the module name, so nothing reads (root)")
	}
}

// template2HTML escapes the characters html/template escapes in text, for comparisons.
func template2HTML(s string) string {
	if strings.HasPrefix(s, "<") {
		return s
	}
	return strings.NewReplacer("'", "&#39;", "\"", "&#34;").Replace(s)
}

func TestAnalysisRiskWhyForOriginalKinds(t *testing.T) {
	for _, k := range []string{RiskConcurrency, RiskDynamicCode, RiskNativeCode, RiskSideEffects, RiskNetwork,
		RiskFSWrite, RiskUnsafeDeserialize, RiskNondeterminism, RiskImportCycle} {
		if RiskWhy(k) == "" {
			t.Errorf("risk kind %q has no explanation", k)
		}
		if _, ok := riskSeverity[k]; !ok {
			t.Errorf("risk kind %q has no severity", k)
		}
	}
}
