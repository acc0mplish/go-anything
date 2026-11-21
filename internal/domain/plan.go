package domain

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// PortRow is one package in the porting order, with the reasons its readiness is what it is.
type PortRow struct {
	Order      int      `json:"order"`
	Wave       int      `json:"wave"`
	Readiness  int      `json:"readiness"`
	ImportPath string   `json:"import_path"`
	Lines      int      `json:"lines"`
	Exported   int      `json:"exported"`
	Risks      []string `json:"risks,omitempty"`
}

// sideEffectImports reach outside the program (processes, network, databases, plugins). Their behavior
// depends on the world, so they are harder to port with confidence.
var sideEffectImports = map[string]bool{
	"os/exec": true, "net": true, "net/http": true, "net/rpc": true, "database/sql": true, "plugin": true,
}

// PortPlan orders the packages for porting. A package's wave is its depth in the internal import graph:
// packages with no internal dependencies come first, so each package is ported after what it uses.
// Within a wave, the more ready package comes first. Readiness starts at 100 and loses points for each
// reason it is risky, and the reasons are returned with the row.
func PortPlan(a Analysis) []PortRow {
	byPath := map[string]Package{}
	for _, p := range a.Packages {
		byPath[p.ImportPath] = p
	}
	waves := map[string]int{}
	var depth func(path string, seen map[string]bool) int
	depth = func(path string, seen map[string]bool) int {
		if w, ok := waves[path]; ok {
			return w
		}
		if seen[path] { // an import cycle is a compile error in Go; guard anyway
			return 0
		}
		seen[path] = true
		w := 0
		for _, imp := range byPath[path].Imports {
			if _, internal := byPath[imp]; internal {
				if d := depth(imp, seen) + 1; d > w {
					w = d
				}
			}
		}
		delete(seen, path)
		waves[path] = w
		return w
	}
	var rows []PortRow
	for _, p := range a.Packages {
		rows = append(rows, PortRow{
			Wave: depth(p.ImportPath, map[string]bool{}), ImportPath: p.ImportPath,
			Lines: p.Lines, Exported: len(p.Exported),
			Readiness: readiness(p),
			Risks:     risks(p),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Wave != rows[j].Wave {
			return rows[i].Wave < rows[j].Wave
		}
		if rows[i].Readiness != rows[j].Readiness {
			return rows[i].Readiness > rows[j].Readiness
		}
		return rows[i].ImportPath < rows[j].ImportPath
	})
	for i := range rows {
		rows[i].Order = i + 1
	}
	return rows
}

// risks lists each reason a package is harder to port, in a fixed order. When the analysis found
// the source places behind a reason, the first few are named with file:line.
func risks(p Package) []string {
	var out []string
	hasImport := func(name string) bool {
		for _, imp := range p.Imports {
			if imp == name {
				return true
			}
		}
		return false
	}
	if hasImport("unsafe") || hasImport("C") {
		out = append(out, "unsafe or cgo: memory layout is not portable"+sitesAt(p, RiskUnsafe, RiskCgo))
	}
	// The reasons below that read risk sites of non-Go kinds never fire for a Go analysis, whose
	// analyzer does not produce those kinds, so Go rows read exactly as before.
	if n := countSites(p, RiskNativeCode); n > 0 {
		out = append(out, fmt.Sprintf("native code: %d FFI use(s), the native side must be ported or bound", n)+sitesAt(p, RiskNativeCode))
	}
	var effects []string
	for _, imp := range p.Imports {
		if sideEffectImports[imp] {
			effects = append(effects, imp)
		}
	}
	if len(effects) > 0 {
		out = append(out, "external side effects: "+strings.Join(effects, ", "))
	} else if kinds := effectKinds(p); len(kinds) > 0 {
		out = append(out, "external side effects: "+strings.Join(kinds, ", ")+sitesAt(p, RiskSideEffects, RiskNetwork, RiskFSWrite))
	}
	if hasImport("reflect") {
		out = append(out, "reflection: behavior depends on runtime types"+sitesAt(p, RiskReflect))
	}
	if n := countSites(p, RiskDynamicCode); n > 0 {
		out = append(out, fmt.Sprintf("dynamic code: %d site(s) build or load code at run time", n)+sitesAt(p, RiskDynamicCode))
	}
	if n := countSites(p, RiskImportCycle); n > 0 {
		out = append(out, fmt.Sprintf("import cycle: %d import(s) in a cycle, which Go forbids", n)+sitesAt(p, RiskImportCycle))
	}
	if n := countSites(p, RiskGoroutine); n > 0 {
		out = append(out, fmt.Sprintf("goroutines: %d go statement(s), ordering is not deterministic", n)+sitesAt(p, RiskGoroutine))
	}
	if n := countSites(p, RiskConcurrency); n > 0 {
		out = append(out, fmt.Sprintf("concurrency: %d site(s), ordering is not deterministic", n)+sitesAt(p, RiskConcurrency))
	}
	if n := countSites(p, RiskGlobalMutable); n > 0 {
		if p.GoPackage != "" { // a non-Go original has modules, not packages
			out = append(out, fmt.Sprintf("global mutable state: %d module variable(s) changed in functions", n)+sitesAt(p, RiskGlobalMutable))
		} else {
			out = append(out, fmt.Sprintf("global mutable state: %d package var(s) assigned in functions", n)+sitesAt(p, RiskGlobalMutable))
		}
	}
	if n := countSites(p, RiskUnsafeDeserialize); n > 0 {
		out = append(out, fmt.Sprintf("unsafe deserialization: %d site(s) can run code from data", n)+sitesAt(p, RiskUnsafeDeserialize))
	}
	if n := countSites(p, RiskNondeterminism); n > 0 {
		out = append(out, fmt.Sprintf("nondeterminism: %d clock or random site(s), fix them when recording", n)+sitesAt(p, RiskNondeterminism))
	}
	if n := countSites(p, RiskInit); n > 0 {
		out = append(out, fmt.Sprintf("init functions: %d, run before main in import order", n)+sitesAt(p, RiskInit))
	}
	// A non-Go module that did not parse has unknown imports and risks, so "no risks" would mislead.
	// Go packages that fail to type-check keep their syntax data and are not marked, as before.
	if p.GoPackage != "" && len(p.TypeErrors) > 0 {
		out = append(out, "did not parse: its imports and risks are unknown")
	}
	if p.Tests == 0 {
		out = append(out, "no tests of its own: record its behavior before porting")
	}
	if len(p.Exported) > 20 {
		out = append(out, "wide API: more than 20 exported functions")
	}
	return out
}

// effectKinds names the kinds of outside-world risk sites a package has, in a fixed order.
func effectKinds(p Package) []string {
	var out []string
	for _, k := range []struct{ kind, name string }{
		{RiskSideEffects, "processes"}, {RiskNetwork, "network"}, {RiskFSWrite, "file writes"},
	} {
		if countSites(p, k.kind) > 0 {
			out = append(out, k.name)
		}
	}
	return out
}

func countSites(p Package, kind string) int {
	n := 0
	for _, s := range p.RiskSites {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

// sitesAt names up to three positions of the given kinds, as " at a.go:1, b.go:2 and 4 more".
// It is empty when the analysis recorded no positions, so older analyses read as before.
func sitesAt(p Package, kinds ...string) string {
	const shown = 3
	var pos []string
	for _, s := range p.RiskSites {
		if slices.Contains(kinds, s.Kind) {
			pos = append(pos, s.Pos)
		}
	}
	if len(pos) == 0 {
		return ""
	}
	if len(pos) <= shown {
		return " at " + strings.Join(pos, ", ")
	}
	return fmt.Sprintf(" at %s and %d more", strings.Join(pos[:shown], ", "), len(pos)-shown)
}

// readiness is 100 minus the weight of each risk, never below zero.
func readiness(p Package) int {
	score := 100
	weights := map[string]int{
		"unsafe or cgo":         25,
		"external side effects": 10,
		"reflection":            5,
		"no tests":              20,
		"wide API":              10,
		"goroutines":            10,
		"global mutable state":  5,
		"init functions":        5,
		// non-Go originals
		"native code":            25,
		"dynamic code":           10,
		"import cycle":           10,
		"concurrency":            10,
		"unsafe deserialization": 5,
		"nondeterminism":         5,
		"did not parse":          20,
	}
	for _, r := range risks(p) {
		for prefix, w := range weights {
			if strings.HasPrefix(r, prefix) {
				score -= w
			}
		}
	}
	if score < 0 {
		score = 0
	}
	return score
}

// riskSeverity orders risk kinds for the ranked list, most serious first.
// The Go kinds keep their relative order; the non-Go kinds sit next to the Go kind closest in meaning.
var riskSeverity = map[string]int{
	RiskCgo: 0, RiskNativeCode: 1, RiskUnsafe: 2, RiskDynamicCode: 3, RiskUnsafeDeserialize: 4,
	RiskGlobalMutable: 5, RiskGoroutine: 6, RiskConcurrency: 7, RiskImportCycle: 8,
	RiskReflect: 9, RiskInit: 10, RiskSideEffects: 11, RiskNetwork: 12, RiskFSWrite: 13,
	RiskNondeterminism: 14,
}

// LocatedRisk is a RiskSite with the package it belongs to.
type LocatedRisk struct {
	Package string `json:"package"`
	RiskSite
}

// RankedRisks lists every risk site of the analysis, most serious kind first, then by position.
func RankedRisks(a Analysis) []LocatedRisk {
	var out []LocatedRisk
	for _, p := range a.Packages {
		for _, s := range p.RiskSites {
			out = append(out, LocatedRisk{Package: p.ImportPath, RiskSite: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, ok := riskSeverity[out[i].Kind]
		if !ok {
			si = len(riskSeverity)
		}
		sj, ok := riskSeverity[out[j].Kind]
		if !ok {
			sj = len(riskSeverity)
		}
		if si != sj {
			return si < sj
		}
		return PosLess(out[i].Pos, out[j].Pos)
	})
	return out
}

// PosLess orders "file:line" positions by file, then by line as a number, so a.go:9 comes
// before a.go:10.
func PosLess(a, b string) bool {
	fa, la := splitPos(a)
	fb, lb := splitPos(b)
	if fa != fb {
		return fa < fb
	}
	return la < lb
}

func splitPos(p string) (string, int) {
	i := strings.LastIndexByte(p, ':')
	if i < 0 {
		return p, 0
	}
	n, err := strconv.Atoi(p[i+1:])
	if err != nil {
		return p, 0
	}
	return p[:i], n
}

// StartHere ranks the functions that are the best first ports. A candidate is an exported
// function or method outside package main, in a package that type-checked and has no cgo, whose
// signature is plain, that makes no dynamic calls and starts no goroutines, and whose in-module
// callees are all in its own package (a leaf at package level). The most called come first.
func StartHere(a Analysis) []StartItem {
	pkgOf := map[string]string{}
	for _, p := range a.Packages {
		for _, f := range p.Functions {
			pkgOf[FuncID(p.ImportPath, f.Receiver, f.Name)] = p.ImportPath
		}
	}
	var out []StartItem
	for _, p := range a.Packages {
		if p.Name == "main" || len(p.TypeErrors) > 0 || slices.Contains(p.Imports, "C") {
			continue
		}
	funcs:
		for _, f := range p.Functions {
			if !f.Exported || !f.PlainSignature || f.DynamicCalls > 0 || f.GoStatements > 0 {
				continue
			}
			for _, c := range f.Callees {
				if pkgOf[c] != p.ImportPath {
					continue funcs
				}
			}
			out = append(out, StartItem{
				Func: FuncID(p.ImportPath, f.Receiver, f.Name), Package: p.ImportPath,
				Pos: f.Pos, Callers: f.Callers, Signature: f.Signature,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Callers != out[j].Callers {
			return out[i].Callers > out[j].Callers
		}
		return out[i].Func < out[j].Func
	})
	return out
}

// UnreachedFuncs lists exported package-level functions outside package main that no function
// in the module calls and nothing references as a value. Methods are left out on purpose: they
// are often reached through an interface, which a static walk cannot see.
func UnreachedFuncs(a Analysis) []string {
	var out []string
	for _, p := range a.Packages {
		if p.Name == "main" {
			continue
		}
		for _, f := range p.Functions {
			if f.Receiver == "" && f.Exported && f.Callers == 0 && f.Refs == 0 {
				out = append(out, FuncID(p.ImportPath, "", f.Name))
			}
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}
