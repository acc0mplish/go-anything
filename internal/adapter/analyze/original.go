package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// unitData is one module of a non-Go original while it is being analyzed.
type unitData struct {
	pkg *domain.Package
	// segments is the module path split into its parts, for the Go package suggestion.
	segments []string
	// importPos is where each in-repo import first appears: imported import path -> "file:line".
	importPos map[string]string
}

// addImport records an in-repo import of target at pos, ignoring an import of the unit itself.
func (u *unitData) addImport(target, pos string) {
	if target == "" || target == u.pkg.ImportPath {
		return
	}
	if _, ok := u.importPos[target]; ok {
		return
	}
	u.importPos[target] = pos
	u.pkg.Imports = append(u.pkg.Imports, target)
}

func (u *unitData) risk(kind, pos, detail string) {
	u.pkg.RiskSites = append(u.pkg.RiskSites, domain.RiskSite{Kind: kind, Pos: pos, Detail: detail})
}

// startBlocked are the risk kinds that keep a module out of start_here: the code cannot be
// read or reproduced from the source alone.
var startBlocked = map[string]bool{
	domain.RiskConcurrency: true, domain.RiskDynamicCode: true, domain.RiskNativeCode: true,
	domain.RiskGoroutine: true, domain.RiskCgo: true, domain.RiskUnsafe: true,
}

// startPerUnit is how many exported functions of one leaf module start_here lists.
const startPerUnit = 3

// finishOriginal turns the units of a non-Go original into the analysis: import cycles become
// risk sites, callers are counted from the callees, lists are sorted and start_here is ranked.
func finishOriginal(out *domain.Analysis, units []*unitData) {
	sort.Slice(units, func(i, j int) bool { return units[i].pkg.ImportPath < units[j].pkg.ImportPath })
	assignGoPackages(units, func(u *unitData) []string { return u.segments })
	markCycles(units)

	callers := map[string]map[string]bool{}
	for _, u := range units {
		for _, f := range u.pkg.Functions {
			self := domain.FuncID(u.pkg.ImportPath, "", f.Name)
			for _, c := range f.Callees {
				if callers[c] == nil {
					callers[c] = map[string]bool{}
				}
				callers[c][self] = true
			}
		}
	}
	for _, u := range units {
		p := u.pkg
		for i := range p.Functions {
			f := &p.Functions[i]
			f.Callers = len(callers[domain.FuncID(p.ImportPath, "", f.Name)])
			out.CallEdges += len(f.Callees)
		}
		sort.Strings(p.Files)
		sort.Strings(p.Imports)
		sort.Strings(p.Funcs)
		sort.Strings(p.Exported)
		sort.SliceStable(p.RiskSites, func(i, j int) bool { return domain.PosLess(p.RiskSites[i].Pos, p.RiskSites[j].Pos) })
		out.Packages = append(out.Packages, *p)
	}
	out.StartHere = originalStartHere(out.Packages)
}

// originalStartHere ranks the first ports of a non-Go original: the exported functions of leaf
// modules (no in-repo imports) that parsed and have no concurrency, dynamic or native code. The
// module that most repo modules import comes first, then the most called function.
func originalStartHere(pkgs []domain.Package) []domain.StartItem {
	importers := map[string]int{}
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			importers[imp]++
		}
	}
	var out []domain.StartItem
next:
	for _, p := range pkgs {
		if len(p.Imports) > 0 || len(p.TypeErrors) > 0 || strings.HasPrefix(p.GoPackage, "cmd/") {
			continue
		}
		for _, s := range p.RiskSites {
			if startBlocked[s.Kind] {
				continue next
			}
		}
		var fs []domain.FuncInfo
		for _, f := range p.Functions {
			if f.Exported {
				fs = append(fs, f)
			}
		}
		sort.SliceStable(fs, func(i, j int) bool {
			if fs[i].Callers != fs[j].Callers {
				return fs[i].Callers > fs[j].Callers
			}
			return domain.PosLess(fs[i].Pos, fs[j].Pos)
		})
		for _, f := range fs[:min(len(fs), startPerUnit)] {
			out = append(out, domain.StartItem{
				Func: domain.FuncID(p.ImportPath, "", f.Name), Package: p.ImportPath, Pos: f.Pos,
				Callers: f.Callers, Signature: f.Signature, Importers: importers[p.ImportPath],
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Importers != out[j].Importers {
			return out[i].Importers > out[j].Importers
		}
		if out[i].Callers != out[j].Callers {
			return out[i].Callers > out[j].Callers
		}
		return out[i].Func < out[j].Func
	})
	return out
}

// markCycles finds the import cycles (strongly connected components of more than one unit) and
// records each import inside a cycle as a risk site at the import's position. Go forbids import
// cycles, so these modules cannot become one Go package each as they are.
func markCycles(units []*unitData) {
	index := map[string]int{}
	for i, u := range units {
		index[u.pkg.ImportPath] = i
	}
	// Tarjan's algorithm, iterative so a long import chain cannot exhaust the stack.
	n := len(units)
	order, low := make([]int, n), make([]int, n)
	onStack := make([]bool, n)
	for i := range order {
		order[i] = -1
	}
	var stack []int
	counter := 0
	comp := make([]int, n)
	var comps [][]int
	type frame struct{ v, next int }
	for start := range units {
		if order[start] >= 0 {
			continue
		}
		call := []frame{{start, 0}}
		order[start], low[start] = counter, counter
		counter++
		stack = append(stack, start)
		onStack[start] = true
		for len(call) > 0 {
			top := &call[len(call)-1]
			imports := units[top.v].pkg.Imports
			if top.next < len(imports) {
				w, ok := index[imports[top.next]]
				top.next++
				if !ok {
					continue
				}
				if order[w] < 0 {
					order[w], low[w] = counter, counter
					counter++
					stack = append(stack, w)
					onStack[w] = true
					call = append(call, frame{w, 0})
				} else if onStack[w] {
					low[top.v] = min(low[top.v], order[w])
				}
				continue
			}
			v := top.v
			call = call[:len(call)-1]
			if len(call) > 0 {
				parent := call[len(call)-1].v
				low[parent] = min(low[parent], low[v])
			}
			if low[v] == order[v] {
				var c []int
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					comp[w] = len(comps)
					c = append(c, w)
					if w == v {
						break
					}
				}
				comps = append(comps, c)
			}
		}
	}
	for _, c := range comps {
		if len(c) < 2 {
			continue
		}
		var names []string
		for _, i := range c {
			names = append(names, units[i].pkg.ImportPath)
		}
		sort.Strings(names)
		members := strings.Join(names[:min(len(names), 4)], ", ")
		if len(names) > 4 {
			members += fmt.Sprintf(" and %d more", len(names)-4)
		}
		for _, i := range c {
			u := units[i]
			first := true // the first site of a unit names the cycle, the others only the import
			for _, imp := range u.pkg.Imports {
				if w, ok := index[imp]; ok && comp[w] == comp[i] {
					detail := fmt.Sprintf("imports %s, same cycle", imp)
					if first {
						detail = fmt.Sprintf("imports %s; cycle of %d modules: %s", imp, len(names), members)
						first = false
					}
					u.risk(domain.RiskImportCycle, u.importPos[imp], detail)
				}
			}
		}
	}
}

// lastSegment is the part of path after the last sep, or path when sep does not occur.
func lastSegment(path string, sep string) string {
	if i := strings.LastIndex(path, sep); i >= 0 {
		return path[i+len(sep):]
	}
	return path
}
