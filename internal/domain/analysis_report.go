package domain

import (
	"bytes"
	"fmt"
	"html/template"
	"sort"
	"strings"
)

// AnalysisReport is the human-readable view of an analysis: what the code contains and where to
// start porting. It is not a pass/fail verdict; that belongs to Report.
type AnalysisReport struct {
	Root    string
	Module  string
	Version int
	// Language is the analyzed language: "" or "go" for Go, else python or js.
	Language string
	// Limits are what the analyzer of a non-Go original cannot see.
	Limits   []string
	Packages []AnalysisPackage
	// PortOrder is the order to port packages in, with each readiness and its reasons.
	PortOrder []PortRow
	// Totals across all packages, for the summary.
	Lines    int
	Exported int

	// Function level view; empty for an analysis made without type information.
	Functions    int
	CallEdges    int
	DynamicCalls int
	// StartHere is the top of Analysis.StartHere, at most startHereShown rows.
	StartHere      []StartItem
	StartHereTotal int
	// Risks are the most serious risk sites, at most risksShown rows.
	Risks      []LocatedRisk
	RisksTotal int
	// RiskKinds counts every risk site by kind, most serious kind first, with why it matters.
	RiskKinds  []RiskKindCount
	Unreached  []string
	TypeErrors []PackageErrors

	// GeneratedAt is an optional time stamp set by the caller; the domain does not read clocks.
	GeneratedAt string
	// Delta is the change against the previous version; nil when there is none to compare with.
	Delta *AnalysisDelta

	// analysis is the source of the report, kept so WithPrevious can compare against it.
	analysis Analysis
}

// PackageErrors is one package that failed to type-check, with its errors.
type PackageErrors struct {
	ImportPath string
	Errors     []string
}

// RiskKindCount is how many risk sites of one kind the analysis found.
type RiskKindCount struct {
	Kind  string
	Count int
	// Why is one line on why the kind matters for a port; empty for a kind without an entry.
	Why string
	// WhyKorean is the Korean mirror of Why; empty exactly when Why is.
	WhyKorean string
}

// WaveGroup is one wave of the porting order.
type WaveGroup struct {
	Wave  int
	Ready int // rows with readiness >= readyAt
	Rows  []PortRow
}

const (
	startHereShown = 10
	risksShown     = 30
	// readyAt is the readiness from which a package counts as ready to port first.
	readyAt = 90
	// holdBelow is the readiness under which a package is named to hold back.
	holdBelow = 60
)

// riskWhy says in one line why each risk kind matters for a port.
var riskWhy = map[string]string{
	RiskGoroutine:     "Goroutines run in an order the scheduler picks, so the port must keep the synchronization, not only the results.",
	RiskUnsafe:        "unsafe relies on Go memory layout and pointer rules that the target does not share.",
	RiskReflect:       "Reflection decides behavior from runtime types, so reading the code statically misses what it does.",
	RiskCgo:           "cgo links C code and C memory, which must be ported or bound separately.",
	RiskGlobalMutable: "Package variables changed at run time carry hidden state between calls, so results depend on call history.",
	RiskInit:          "init functions run before main in import order; the port must run their effects in the same order.",
	"side_effects":    "Processes, network and databases tie behavior to the outside world, so it has to be recorded to be compared.",
	// Kinds found in non-Go originals.
	RiskNativeCode:        "Native code reached through an FFI (ctypes, cffi, a .node addon) must be ported or bound with cgo, which the rest of the port avoids.",
	RiskDynamicCode:       "Code built or loaded at run time (eval, exec, dynamic import) cannot be read statically and has no direct Go equivalent.",
	RiskImportCycle:       "Go forbids import cycles, so the modules in a cycle must become one Go package or be split differently.",
	RiskUnsafeDeserialize: "Deserialization that can run code (pickle) has no Go equivalent; the data format itself has to be redesigned or decoded by hand.",
	RiskConcurrency:       "Threads, processes or an event loop run in an order the runtime picks, so the port must keep the synchronization, not only the results.",
	RiskNetwork:           "Sockets and HTTP tie behavior to other machines, so it has to be recorded or faked to be compared.",
	RiskFSWrite:           "File writes change the disk, so a recording must capture the files written, not only stdout.",
	RiskNondeterminism:    "Clock and random values differ on every run; fix or record them, or the original and the port can never be compared.",
}

// RiskWhy returns the one-line explanation for a risk kind, or "" for an unknown kind.
func RiskWhy(kind string) string { return riskWhy[kind] }

// riskWhyKorean is the Korean mirror of riskWhy, key for key, so every explained kind carries both
// languages on the report. A test checks the coverage of both maps against each other.
var riskWhyKorean = map[string]string{
	RiskGoroutine:     "고루틴은 스케줄러가 고른 순서로 실행되므로, 포팅은 결과만이 아니라 동기화까지 유지해야 합니다.",
	RiskUnsafe:        "unsafe는 대상 언어가 공유하지 않는 Go 메모리 배치와 포인터 규칙에 의존합니다.",
	RiskReflect:       "리플렉션은 실행 시점 타입으로 동작을 결정하므로, 코드를 정적으로 읽으면 실제 동작을 놓칩니다.",
	RiskCgo:           "cgo는 C 코드와 C 메모리를 연결하므로, 따로 포팅하거나 바인딩해야 합니다.",
	RiskGlobalMutable: "실행 중 바뀌는 패키지 변수는 호출 사이에 숨은 상태를 남기므로 결과가 호출 이력에 의존합니다.",
	RiskInit:          "init 함수는 main보다 먼저 임포트 순서대로 실행되므로, 포팅도 같은 순서로 그 효과를 내야 합니다.",
	"side_effects":    "프로세스, 네트워크, 데이터베이스는 동작을 외부 세계에 묶으므로, 비교하려면 기록해야 합니다.",
	// Kinds found in non-Go originals.
	RiskNativeCode:        "FFI(ctypes, cffi, .node 애드온)로 접근하는 네이티브 코드는 따로 포팅하거나 cgo로 바인딩해야 하며, 이는 포팅의 나머지가 피하려던 것입니다.",
	RiskDynamicCode:       "실행 시점에 만들어지거나 불러오는 코드(eval, exec, dynamic import)는 정적으로 읽을 수 없고 Go에 직접 대응하는 것이 없습니다.",
	RiskImportCycle:       "Go는 임포트 순환을 금지하므로, 순환에 있는 모듈은 하나의 Go 패키지가 되거나 다르게 나뉘어야 합니다.",
	RiskUnsafeDeserialize: "코드를 실행할 수 있는 역직렬화(pickle)는 Go에 대응물이 없으므로, 데이터 형식 자체를 다시 설계하거나 손으로 디코딩해야 합니다.",
	RiskConcurrency:       "스레드, 프로세스, 이벤트 루프는 런타임이 고른 순서로 실행되므로, 포팅은 결과만이 아니라 동기화까지 유지해야 합니다.",
	RiskNetwork:           "소켓과 HTTP는 동작을 다른 기계에 묶으므로, 비교하려면 기록하거나 흉내 내야 합니다.",
	RiskFSWrite:           "파일 쓰기는 디스크를 바꾸므로, 녹화는 stdout뿐 아니라 쓴 파일까지 담아야 합니다.",
	RiskNondeterminism:    "시계와 무작위 값은 실행마다 달라지므로, 고치거나 기록해야 원본과 포팅을 비교할 수 있습니다.",
}

// originalRiskWhy replaces the Go wording of a kind for a non-Go original.
var originalRiskWhy = map[string]string{
	RiskGlobalMutable: "Module variables changed at run time carry hidden state between calls, so results depend on call history.",
	RiskSideEffects:   "Starting other processes ties behavior to the outside world, so it has to be recorded to be compared.",
}

// originalRiskWhyKorean mirrors originalRiskWhy for a non-Go original.
var originalRiskWhyKorean = map[string]string{
	RiskGlobalMutable: "실행 중 바뀌는 모듈 변수는 호출 사이에 숨은 상태를 남기므로 결과가 호출 이력에 의존합니다.",
	RiskSideEffects:   "다른 프로세스를 시작하는 것은 동작을 외부 세계에 묶으므로, 비교하려면 기록해야 합니다.",
}

// riskWhyFor is RiskWhy in the wording of the analyzed language.
func riskWhyFor(kind string, isGo bool) string {
	if w, ok := originalRiskWhy[kind]; ok && !isGo {
		return w
	}
	return RiskWhy(kind)
}

// riskWhyKoreanFor is the Korean mirror in the wording of the analyzed language.
func riskWhyKoreanFor(kind string, isGo bool) string {
	if w, ok := originalRiskWhyKorean[kind]; ok && !isGo {
		return w
	}
	return riskWhyKorean[kind]
}

// ReadinessWeight is the points readiness takes off for one reason.
type ReadinessWeight struct {
	Reason string
	Points int
}

// readinessWeights mirror the points readiness in plan.go takes off per reason. They are only used
// to explain the score; a test checks them against readiness itself.
var readinessWeights = []ReadinessWeight{
	{"unsafe or cgo", 25},
	{"no tests of its own", 20},
	{"external side effects (os/exec, net, net/http, net/rpc, database/sql, plugin)", 10},
	{"goroutines", 10},
	{"wide API (more than 20 exported functions)", 10},
	{"reflection", 5},
	{"global mutable state", 5},
	{"init functions", 5},
}

// originalReadinessWeights explain readiness for a non-Go original, whose reasons come from risk
// sites rather than Go imports. A test checks them against readiness.
var originalReadinessWeights = []ReadinessWeight{
	{"native code (ctypes, cffi, .node addons)", 25},
	{"no tests of its own", 20},
	{"did not parse (imports and risks unknown)", 20},
	{"external side effects (processes, network, file writes)", 10},
	{"dynamic code (eval, exec, dynamic import)", 10},
	{"import cycle (Go forbids it)", 10},
	{"concurrency (threads, processes, event loop)", 10},
	{"wide API (more than 20 exported functions)", 10},
	{"global mutable state", 5},
	{"unsafe deserialization (pickle)", 5},
	{"nondeterminism (clock, random)", 5},
}

// AnalysisPackage is one package row.
type AnalysisPackage struct {
	ImportPath string
	Dir        string
	Files      int
	Lines      int
	Exported   int
	Internal   bool
	Funcs      int
	// File is the source file of a single-file unit (a module of a non-Go original), else "".
	File string
}

// NewAnalysisReport builds the view from an analysis, with packages ordered by size, largest first.
func NewAnalysisReport(a Analysis, version int) AnalysisReport {
	r := AnalysisReport{Root: a.Root, Module: a.Module, Version: version, Language: a.Language, Limits: a.Limits, PortOrder: PortPlan(a), analysis: a}
	for _, p := range a.Packages {
		row := AnalysisPackage{
			ImportPath: p.ImportPath, Dir: p.Dir, Files: len(p.Files), Lines: p.Lines,
			Exported: len(p.Exported), Internal: p.Internal, Funcs: len(p.Funcs),
		}
		if a.Language != "" && a.Language != "go" && len(p.Files) == 1 {
			row.File = p.Files[0]
			if p.Dir != "" && p.Dir != "." {
				row.File = p.Dir + "/" + p.Files[0]
			}
		}
		r.Packages = append(r.Packages, row)
		r.Lines += p.Lines
		r.Exported += len(p.Exported)
		r.Functions += len(p.Functions)
		if len(p.TypeErrors) > 0 {
			r.TypeErrors = append(r.TypeErrors, PackageErrors{ImportPath: p.ImportPath, Errors: p.TypeErrors})
		}
	}
	r.CallEdges, r.DynamicCalls, r.Unreached = a.CallEdges, a.DynamicCalls, a.Unreached
	r.StartHereTotal = len(a.StartHere)
	r.StartHere = a.StartHere[:min(len(a.StartHere), startHereShown)]
	risks := RankedRisks(a)
	r.RisksTotal = len(risks)
	r.Risks = risks[:min(len(risks), risksShown)]
	for _, k := range risks { // ranked, so kinds arrive most serious first
		if n := len(r.RiskKinds); n > 0 && r.RiskKinds[n-1].Kind == k.Kind {
			r.RiskKinds[n-1].Count++
			continue
		}
		r.RiskKinds = append(r.RiskKinds, RiskKindCount{Kind: k.Kind, Count: 1, Why: riskWhyFor(k.Kind, r.IsGo()), WhyKorean: riskWhyKoreanFor(k.Kind, r.IsGo())})
	}
	sort.SliceStable(r.Packages, func(i, j int) bool {
		if r.Packages[i].Lines != r.Packages[j].Lines {
			return r.Packages[i].Lines > r.Packages[j].Lines
		}
		return r.Packages[i].ImportPath < r.Packages[j].ImportPath
	})
	return r
}

// WithPrevious returns the report with the change against an earlier analysis of the same module.
// It leaves the report without a delta when the modules differ, since a comparison across modules
// would be meaningless. It needs a report made by NewAnalysisReport.
func (r AnalysisReport) WithPrevious(prev Analysis, prevVersion int) AnalysisReport {
	if prev.Module != "" && r.analysis.Module != "" && prev.Module != r.analysis.Module {
		return r
	}
	d := DiffAnalyses(prev, r.analysis)
	d.PrevVersion = prevVersion
	r.Delta = &d
	return r
}

// BottomLine is the few sentences at the top of the page that say what to do first.
func (r AnalysisReport) BottomLine() []string {
	return bottomLine(r.RelModule(), r.PortOrder, r.StartHere, r.Delta, r.words())
}

// IsGo reports whether the analyzed source is Go. An analysis without a language is Go, since
// only the Go analyzer existed before the field.
func (r AnalysisReport) IsGo() bool { return r.Language == "" || r.Language == "go" }

// RelModule is the prefix paths are shown relative to. A non-Go original names its units in its
// own notation, which the module name does not prefix, so nothing is cut.
func (r AnalysisReport) RelModule() string {
	if r.IsGo() {
		return r.Module
	}
	return ""
}

// HasImportCycles reports whether any unit imports inside a cycle, which only happens in a
// non-Go original; it qualifies the "imports only earlier waves" promise.
func (r AnalysisReport) HasImportCycles() bool {
	for _, k := range r.RiskKinds {
		if k.Kind == RiskImportCycle {
			return true
		}
	}
	return false
}

// LanguageName is the analyzed language as people write it.
func (r AnalysisReport) LanguageName() string {
	switch r.Language {
	case "", "go":
		return "Go"
	case "python":
		return "Python"
	case "js":
		return "JavaScript/TypeScript"
	}
	return r.Language
}

// Unit and Units name one analyzed unit: a Go package, or a module (file) of another language.
func (r AnalysisReport) Unit() string { return r.words().one }

// Units is the plural of Unit.
func (r AnalysisReport) Units() string { return r.words().many }

// GoPackageOf is the suggested Go package for a unit of a non-Go original, or "".
func (r AnalysisReport) GoPackageOf(importPath string) string {
	for _, p := range r.analysis.Packages {
		if p.ImportPath == importPath {
			return p.GoPackage
		}
	}
	return ""
}

// unitWords are the words the bottom line uses for the analyzed units.
type unitWords struct {
	one, many string
	// scope is where an import counts as internal.
	scope string
	// firstNote says how the first functions were chosen.
	firstNote string
}

var goWords = unitWords{"package", "packages", "the module", "leaf functions with plain signatures, most called first"}

var originalWords = unitWords{"module", "modules", "the repo", "exported functions of leaf modules without concurrency, dynamic or native code, most imported module first"}

func (r AnalysisReport) words() unitWords {
	if r.IsGo() {
		return goWords
	}
	return originalWords
}

// Waves groups the porting order by wave, keeping the order inside each wave.
func (r AnalysisReport) Waves() []WaveGroup {
	var out []WaveGroup
	for _, row := range r.PortOrder {
		if n := len(out); n == 0 || out[n-1].Wave != row.Wave {
			out = append(out, WaveGroup{Wave: row.Wave})
		}
		g := &out[len(out)-1]
		g.Rows = append(g.Rows, row)
		if row.Readiness >= readyAt {
			g.Ready++
		}
	}
	return out
}

// DynamicShare is the dynamic calls as a percent of static edges plus dynamic calls, or "" when
// both are zero. A small nonzero share reads "<1%" rather than "0%".
func (r AnalysisReport) DynamicShare() string {
	total := r.CallEdges + r.DynamicCalls
	if total <= 0 || r.DynamicCalls < 0 {
		return ""
	}
	pct := (r.DynamicCalls*100 + total/2) / total
	if pct == 0 && r.DynamicCalls > 0 {
		return "<1%"
	}
	return fmt.Sprintf("%d%%", pct)
}

// NewRisksShown is the head of Delta.NewRisks the page lists.
func (r AnalysisReport) NewRisksShown() []LocatedRisk {
	if r.Delta == nil {
		return nil
	}
	return r.Delta.NewRisks[:min(len(r.Delta.NewRisks), risksShown)]
}

// ReadinessWeights lists the points readiness takes off per reason, for the explanation block.
func (r AnalysisReport) ReadinessWeights() []ReadinessWeight {
	if r.IsGo() {
		return readinessWeights
	}
	return originalReadinessWeights
}

// AnalysisBottomLine writes two to four plain sentences from the analysis: which wave-0 packages to
// port first, the first functions, which packages to hold back and why, and what changed since the
// previous version. It states only what the data shows.
func AnalysisBottomLine(module string, order []PortRow, start []StartItem, d *AnalysisDelta) []string {
	return bottomLine(module, order, start, d, goWords)
}

func bottomLine(module string, order []PortRow, start []StartItem, d *AnalysisDelta, w unitWords) []string {
	if len(order) == 0 {
		return []string{fmt.Sprintf("The analysis found no %s, so there is nothing to port yet.", w.many)}
	}
	var out []string
	var wave0, ready []string
	best := -1
	for i, row := range order {
		if row.Wave != 0 {
			continue
		}
		wave0 = append(wave0, row.ImportPath)
		if row.Readiness >= readyAt {
			ready = append(ready, RelPath(module, row.ImportPath))
		}
		if best < 0 || row.Readiness > order[best].Readiness {
			best = i
		}
	}
	if len(ready) > 0 {
		out = append(out, fmt.Sprintf("Start with wave 0: %d of its %d %s are ready (readiness >= %d): %s.",
			len(ready), len(wave0), w.many, readyAt, nameList(ready, 4)))
	} else {
		out = append(out, fmt.Sprintf("Start with wave 0 (%d %s with no imports inside %s); none reaches readiness %d, the most ready is %s at %d.",
			len(wave0), plural(len(wave0), w.one, w.many), w.scope, readyAt, RelPath(module, order[best].ImportPath), order[best].Readiness))
	}
	if len(start) > 0 {
		var names []string
		for _, s := range start[:min(len(start), 3)] {
			names = append(names, RelPath(module, s.Func))
		}
		out = append(out, "First functions: "+nameList(names, 3)+" ("+w.firstNote+").")
	}
	out = append(out, holdSentence(module, order, w))
	if d != nil {
		out = append(out, deltaSentence(*d, w))
	}
	return out
}

// holdSentence names the packages with the lowest readiness and their reasons. Packages below
// holdBelow are held back; when there are none, the lowest below readyAt are named to port with care.
func holdSentence(module string, order []PortRow, w unitWords) string {
	var low, care []PortRow
	for _, row := range order {
		switch {
		case row.Readiness < holdBelow:
			low = append(low, row)
		case row.Readiness < readyAt:
			care = append(care, row)
		}
	}
	name := func(rows []PortRow) string {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Readiness < rows[j].Readiness })
		var parts []string
		for _, row := range rows[:min(len(rows), 2)] {
			parts = append(parts, fmt.Sprintf("%s (%d: %s)", RelPath(module, row.ImportPath), row.Readiness, riskLabels(row.Risks, 3)))
		}
		s := strings.Join(parts, ", ")
		if len(rows) > 2 {
			s += fmt.Sprintf(" and %d more", len(rows)-2)
		}
		return s
	}
	switch {
	case len(low) > 0:
		return fmt.Sprintf("Hold back the lowest readiness, below %d: %s.", holdBelow, name(low))
	case len(care) > 0:
		return fmt.Sprintf("No %s scores below %d; port the lowest with care: %s.", w.one, holdBelow, name(care))
	}
	return fmt.Sprintf("Every %s reaches readiness %d, so none has to be held back.", w.one, readyAt)
}

// deltaSentence sums up a delta in one sentence.
func deltaSentence(d AnalysisDelta, w unitWords) string {
	var parts []string
	if n := len(d.AddedPackages); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s added", n, plural(n, w.one, w.many)))
	}
	if n := len(d.RemovedPackages); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s removed", n, plural(n, w.one, w.many)))
	}
	if d.Lines != 0 {
		parts = append(parts, "lines "+Signed(d.Lines))
	}
	if n := len(d.NewRisks); n > 0 {
		parts = append(parts, fmt.Sprintf("%d new risk %s", n, plural(n, "site", "sites")))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Since v%d: no %s added or removed, the same line count and no new risk sites.", d.PrevVersion, w.many)
	}
	return fmt.Sprintf("Since v%d: %s.", d.PrevVersion, strings.Join(parts, ", "))
}

// riskLabels keeps the name of each risk reason, the text before its first colon.
func riskLabels(risks []string, limit int) string {
	if len(risks) == 0 {
		return "no recorded reason"
	}
	var out []string
	for _, r := range risks[:min(len(risks), limit)] {
		if i := strings.IndexByte(r, ':'); i >= 0 {
			r = r[:i]
		}
		out = append(out, r)
	}
	return strings.Join(out, ", ")
}

// nameList joins up to limit names as "a, b and c", adding "and N more" past the limit.
func nameList(names []string, limit int) string {
	if len(names) > limit {
		return strings.Join(names[:limit], ", ") + fmt.Sprintf(" and %d more", len(names)-limit)
	}
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// RelPath writes an import path or a FuncID relative to the module: "m/a/b" becomes "a/b",
// "m/a.F" becomes "a.F", the module itself becomes "(root)" and "m.F" becomes "(root).F". A path
// outside the module is returned as is.
func RelPath(module, s string) string {
	if module == "" {
		return s
	}
	switch {
	case s == module:
		return "(root)"
	case strings.HasPrefix(s, module+"/"):
		return s[len(module)+1:]
	case strings.HasPrefix(s, module+"."):
		return "(root)" + s[len(module):]
	}
	return s
}

// RelText drops the module prefix wherever it starts a path inside a longer text, such as a
// signature that names a type of another module package. The prefix must start a word, so module
// "m" leaves "example.com/m/x" alone.
func RelText(module, s string) string {
	if module == "" {
		return s
	}
	prefix := module + "/"
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		if i > 0 && isPathByte(s[i-1]) {
			b.WriteString(s[:i+1])
			s = s[i+1:]
			continue
		}
		b.WriteString(s[:i])
		s = s[i+len(prefix):]
	}
}

func isPathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._-~/", c) >= 0
}

// ReadinessBarClass is the CSS class of a readiness bar: plain when ready, mid from holdBelow up
// to readyAt, low below holdBelow.
func ReadinessBarClass(readiness int) string {
	switch {
	case readiness >= readyAt:
		return "bar"
	case readiness >= holdBelow:
		return "bar mid"
	}
	return "bar low"
}

// Signed writes a change with its sign: "+3", "-2" or "0".
func Signed(n int) string {
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprintf("%d", n)
}

// Change is a card's delta against the previous version; zero reads as "no change", not as a value of 0.
func Change(n int) string {
	if n == 0 {
		return "no change"
	}
	return Signed(n)
}

// dirNote is the package folder when it differs from the relative import path, else "".
func dirNote(module, importPath, dir string) string {
	rel := RelPath(module, importPath)
	if dir == rel || (rel == "(root)" && (dir == "." || dir == "")) {
		return ""
	}
	return dir
}

func clampPercent(n int) int { return max(0, min(100, n)) }

// HTML renders the analysis as one self-contained page. Values are escaped, since package names
// and paths come from the analyzed code.
func (r AnalysisReport) HTML() ([]byte, error) {
	var buf bytes.Buffer
	if err := analysisTemplate.Execute(&buf, r); err != nil {
		return nil, fmt.Errorf("analysis report: html: %w", err)
	}
	return buf.Bytes(), nil
}

var analysisFuncs = template.FuncMap{
	"rel":      RelPath,
	"barClass": ReadinessBarClass,
	"pct":      clampPercent,
	"signed":   Signed,
	"change":   Change,
	"dirNote":  dirNote,
	"relText":  RelText,
	"bi":       biHTML,
	"biIn":     biInlineHTML,
}

var analysisTemplate = template.Must(template.New("analysis").Funcs(analysisFuncs).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Analysis v{{.Version}}: {{.Module}}</title>
<style>` + ReportCSS + `
  td.path, td.val { word-break: normal; overflow-wrap: anywhere; }
  td.bar-cell { white-space: nowrap; }
  td.nowrap { white-space: nowrap; }
  table.order { table-layout: fixed; }
  table.order col.c-order { width: 4.5rem; } table.order col.c-ready { width: 150px; }
  table.order col.c-pkg { width: 290px; } table.order col.c-lines { width: 5rem; }
  td.bar-cell .bar { display: inline-block; width: 90px; vertical-align: middle; margin-left: .4rem; }
  h3 { font-size: 1rem; margin: 1.25rem 0 .25rem; }
  ul.kinds { padding-left: 1.1rem; } ul.kinds li { margin: .2rem 0; }
  ul.paths { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .82rem; }
</style>
</head>
<body>
{{- $m := .RelModule}}
{{- $koUnit := "모듈"}}{{if .IsGo}}{{$koUnit = "패키지"}}{{end}}
<h1>Analysis v{{.Version}}: {{.Module}}</h1>
{{- if .IsGo}}
<p class="meta">Root <code>{{.Root}}</code> · version {{.Version}}{{if .GeneratedAt}} · generated {{.GeneratedAt}}{{end}} · paths below are relative to the module; hover for the full path</p>
{{- else}}
<p class="meta">Root <code>{{.Root}}</code> · version {{.Version}}{{if .GeneratedAt}} · generated {{.GeneratedAt}}{{end}} · original in {{.LanguageName}} · units are its modules, named in its own notation; the Go package column is where each one lands in the Go rewrite</p>
{{- end}}
<nav class="toc">
  <a href="#summary">{{biIn "Summary" "요약"}}</a>
  <a href="#bottom-line">{{biIn "Bottom line" "결론"}}</a>
  <a href="#changes">{{biIn "Changes" "변경 사항"}}</a>
  {{- if .StartHere}}
  <a href="#start-here">{{biIn "Start here" "여기서 시작"}}</a>
  {{- end}}
  <a href="#order">{{biIn "Porting 순서" "포팅 순서"}}</a>
  {{- if .Risks}}
  <a href="#risks">{{biIn "Risks" "위험"}}</a>
  {{- end}}
  <a href="#packages">{{if .IsGo}}{{biIn "Packages" "패키지"}}{{else}}{{biIn "Modules" "모듈"}}{{end}}</a>
  <a href="#how">{{biIn "How to read this" "읽는 법"}}</a>
</nav>

<h2 id="summary">{{biIn "Summary" "요약"}}</h2>
<div class="cards">
  {{/* The Packages/Modules title line stays bare for compactness; the Korean line mirrors it at the card foot. */}}
  <div class="card"><div class="k">{{if .IsGo}}{{biIn "Packages" "패키지"}}{{else}}{{biIn "Modules" "모듈"}}{{end}}</div><div class="v">{{len .Packages}}</div><div class="s">{{if .Delta}}{{change .Delta.Packages}} vs v{{.Delta.PrevVersion}}{{else}}{{.Exported}} exported funcs{{end}}</div></div>
  <div class="card"><div class="k">{{biIn "Lines" "라인 수"}}</div><div class="v">{{.Lines}}</div><div class="s">{{if .Delta}}{{change .Delta.Lines}} vs v{{.Delta.PrevVersion}}{{else}}non-test {{.LanguageName}} lines{{end}}</div></div>
  {{- if .Functions}}
  <div class="card"><div class="k">{{biIn "Functions" "함수"}}</div><div class="v">{{.Functions}}</div><div class="s">{{if .Delta}}{{change .Delta.Functions}} vs v{{.Delta.PrevVersion}}{{else}}{{if .IsGo}}functions and methods{{else}}top-level functions{{end}}{{end}}</div></div>
  {{/* The Static call edges and Dynamic calls title lines stay bare for compactness; the Korean line mirrors them at the card foot. */}}
  <div class="card"><div class="k">{{biIn "Static call edges" "정적 호출 간선"}}</div><div class="v">{{.CallEdges}}</div><div class="s">{{if .Delta}}{{change .Delta.CallEdges}} vs v{{.Delta.PrevVersion}}{{else}}{{if .IsGo}}inside the module{{else}}inside the repo, direct calls only{{end}}{{end}}</div>
  {{- if .IsGo}}
  <div class="card{{if .DynamicCalls}} warn{{end}}"><div class="k">{{biIn "Dynamic calls" "동적 호출"}}</div><div class="v">{{.DynamicCalls}}</div><div class="s">{{with .DynamicShare}}{{.}} unresolved{{end}}{{if .Delta}}{{if .DynamicShare}} · {{end}}{{change .Delta.DynamicCalls}} vs v{{.Delta.PrevVersion}}{{end}}</div>
  {{- end}}
  {{/* The Risk sites title line stays bare for compactness; the Korean line mirrors it at the card foot. */}}
  <div class="card"><div class="k">{{biIn "Risk sites" "위험 지점"}}</div><div class="v">{{.RisksTotal}}</div><div class="s">{{if .Delta}}{{change .Delta.Risks}} vs v{{.Delta.PrevVersion}}{{else}}{{len .RiskKinds}} kinds{{end}}</div>
  <div class="card ok"><div class="k">{{biIn "Start here" "여기서 시작"}}</div><div class="v">{{.StartHereTotal}}</div><div class="s">{{if .Delta}}{{change .Delta.StartHere}} vs v{{.Delta.PrevVersion}}{{else}}first-port candidates{{end}}</div></div>
  {{- end}}
</div>

<div class="banner info" id="bottom-line">
  <strong class="verdict">{{biIn "Bottom line" "결론"}}</strong>
  {{- range .BottomLine}}
  <p>{{.}}</p>
  {{- end}}
</div>

{{- if .Limits}}
<div class="banner warn" id="limits">
  <strong>{{bi (printf "What this analysis of a %s original cannot see" .LanguageName) (printf "이 %s 원본 분석이 볼 수 없는 것" .LanguageName)}}</strong>
  <ul>
    {{- range .Limits}}
    <li>{{.}}</li>
    {{- end}}
  </ul>
</div>
{{- end}}

{{- if .Delta}}
<h2 id="changes">{{biIn (printf "Changes since v%d" .Delta.PrevVersion) (printf "v%d 이후 변경 사항" .Delta.PrevVersion)}}</h2>
{{- if not (or .Delta.AddedPackages .Delta.RemovedPackages .Delta.NewRisks)}}
<p>{{bi (printf "No %s added or removed and no new risk sites since v%d." .Units .Delta.PrevVersion) (printf "v%d 이후 추가되거나 제거된 %s이(가) 없고 새로운 위험 지점도 없습니다." .Delta.PrevVersion $koUnit)}}</p>
{{- end}}
{{- if .Delta.AddedPackages}}
<h3>{{biIn (printf "Added %s (%d)" .Units (len .Delta.AddedPackages)) (printf "추가된 %s (%d)" $koUnit (len .Delta.AddedPackages))}}</h3>
<ul class="paths">
  {{- range .Delta.AddedPackages}}
  <li title="{{.}}">{{rel $m .}}</li>
  {{- end}}
</ul>
{{- end}}
{{- if .Delta.RemovedPackages}}
<h3>{{biIn (printf "Removed %s (%d)" .Units (len .Delta.RemovedPackages)) (printf "제거된 %s (%d)" $koUnit (len .Delta.RemovedPackages))}}</h3>
<ul class="paths">
  {{- range .Delta.RemovedPackages}}
  <li title="{{.}}">{{rel $m .}}</li>
  {{- end}}
</ul>
{{- end}}
{{- if .Delta.NewRisks}}
<h3>{{biIn (printf "New risk sites (%d)" (len .Delta.NewRisks)) (printf "새로운 위험 지점 (%d)" (len .Delta.NewRisks))}}</h3>
<p class="note">A site counts as new when its kind and file:line were not in v{{.Delta.PrevVersion}}, so a site that only moved lines shows here too. Showing {{len .NewRisksShown}} of {{len .Delta.NewRisks}}.</p>
<table>
  <tr><th>{{bi "Kind" "종류"}}</th><th>{{bi "Where" "위치"}}</th><th>{{if .IsGo}}{{bi "Package" "패키지"}}{{else}}{{bi "Module" "모듈"}}{{end}}</th><th>{{bi "Detail" "상세"}}</th></tr>
  {{- range .NewRisksShown}}
  <tr>
    <td><span class="badge warn">{{.Kind}}</span></td>
    <td class="path">{{.Pos}}</td>
    <td class="path" title="{{.Package}}">{{rel $m .Package}}</td>
    <td>{{.Detail}}</td>
  </tr>
  {{- end}}
</table>
{{- end}}
{{- else}}
<h2 id="changes">{{biIn "Changes" "변경 사항"}}</h2>
{{- if le .Version 1}}
<p class="note">This is the first analysis of this {{if .IsGo}}module{{else}}project{{end}}, so there is nothing to compare with yet. The next run will show what changed.</p>
{{- else}}
<p class="note">No data from an earlier version was available, so changes since the previous version cannot be shown.</p>
{{- end}}
{{- end}}

{{- if .StartHere}}
<h2 id="start-here">{{biIn "Start here: best first ports" "여기서 시작: 가장 좋은 첫 포팅"}}</h2>
{{- if .IsGo}}
<p class="note">Exported functions outside package main with a plain signature, no dynamic calls,
no goroutines, no cgo, and no calls into other module packages. Most called first.
Showing {{len .StartHere}} of {{.StartHereTotal}}.</p>
<table>
  <tr><th>{{bi "Function" "함수"}}</th><th>{{bi "Where" "위치"}}</th><th class="num">{{bi "Callers" "호출자"}}</th><th>{{bi "Signature" "시그니처"}}</th></tr>
  {{- range .StartHere}}
  <tr>
    <td class="path nowrap" title="{{.Func}}">{{rel $m .Func}}</td>
    <td class="path nowrap">{{.Pos}}</td>
    <td class="num">{{.Callers}}</td>
    <td class="path" title="{{.Signature}}">{{relText $m .Signature}}</td>
  </tr>
  {{- end}}
</table>
{{- else}}
<p class="note">Exported functions of leaf modules: modules that import nothing inside the repo, parse cleanly and
have no concurrency, dynamic code or native code. The module imported by the most repo modules comes first; inside it,
the most called function. Showing {{len .StartHere}} of {{.StartHereTotal}}.</p>
<table>
  <tr><th>{{bi "Function" "함수"}}</th><th>{{bi "Where" "위치"}}</th><th>{{bi "Go package" "Go 패키지"}}</th><th class="num">{{bi "Importers" "가져오는 모듈"}}</th><th class="num">{{bi "Callers" "호출자"}}</th><th>{{bi "Parameters" "매개변수"}}</th></tr>
  {{- range .StartHere}}
  <tr>
    <td class="path" title="{{.Func}}">{{.Func}}</td>
    <td class="path">{{.Pos}}</td>
    <td class="path">{{$.GoPackageOf .Package}}</td>
    <td class="num">{{.Importers}}</td>
    <td class="num">{{.Callers}}</td>
    <td class="path">{{.Signature}}</td>
  </tr>
  {{- end}}
</table>
{{- end}}
{{- end}}

<h2 id="order">{{bi "Porting order" "포팅 순서"}}</h2>
<p class="note">Port wave by wave: a {{.Unit}} comes after every {{.Unit}} it imports. Inside a wave the most ready comes first. Green bar: readiness 90 or more. Amber: 60 to 89. Red: below 60.</p>
{{- range .Waves}}
<h3>Wave {{.Wave}} · {{len .Rows}} {{if eq (len .Rows) 1}}{{$.Unit}}{{else}}{{$.Units}}{{end}}, {{.Ready}} ready{{if eq .Wave 0}} · no imports inside {{if $.IsGo}}the module{{else}}the repo{{end}}{{else}} · imports only earlier waves{{if $.HasImportCycles}}, except imports marked import cycle{{end}}{{end}}</h3>
<table class="order">
  <colgroup><col class="c-order"><col class="c-ready"><col class="c-pkg"><col class="c-lines"><col></colgroup>
  <tr><th class="num">{{bi "Order" "순서"}}</th><th>{{bi "Readiness" "준비도"}}</th><th>{{if $.IsGo}}{{bi "Package" "패키지"}}{{else}}{{bi "Module → Go package" "모듈 → Go 패키지"}}{{end}}</th><th class="num">{{bi "Lines" "라인"}}</th><th>{{bi "Risks" "위험"}}</th></tr>
  {{- range .Rows}}
  <tr>
    <td class="num">{{.Order}}</td>
    <td class="num bar-cell">{{.Readiness}}<span class="{{barClass .Readiness}}"><span style="width: {{pct .Readiness}}%"></span></span></td>
    <td class="path" title="{{.ImportPath}}">{{rel $m .ImportPath}}{{with $.GoPackageOf .ImportPath}}<div class="note">→ {{.}}</div>{{end}}</td>
    <td class="num">{{.Lines}}</td>
    <td>{{range .Risks}}<div>{{.}}</div>{{else}}none{{end}}</td>
  </tr>
  {{- end}}
</table>
{{- end}}

{{- if .Risks}}
<h2 id="risks">{{bi "Risks by location" "위치별 위험"}}</h2>
<ul class="kinds">
  {{- range .RiskKinds}}
  <li><span class="badge warn">{{.Kind}}</span> {{.Count}}{{if .Why}}: {{bi .Why .WhyKorean}}{{end}}</li>
  {{- end}}
</ul>
<p class="note">Most serious kind first. Showing {{len .Risks}} of {{.RisksTotal}}.</p>
<table>
  <tr><th>{{bi "Kind" "종류"}}</th><th>{{bi "Where" "위치"}}</th><th>{{if .IsGo}}{{bi "Package" "패키지"}}{{else}}{{bi "Module" "모듈"}}{{end}}</th><th>{{bi "Detail" "상세"}}</th></tr>
  {{- range .Risks}}
  <tr>
    <td>{{.Kind}}</td>
    <td class="path">{{.Pos}}</td>
    <td class="path" title="{{.Package}}">{{rel $m .Package}}</td>
    <td>{{.Detail}}</td>
  </tr>
  {{- end}}
</table>
{{- end}}

<h2 id="packages">{{if .IsGo}}{{bi "Packages, largest first" "패키지, 크기 순"}}{{else}}{{bi "Modules, largest first" "모듈, 크기 순"}}{{end}}</h2>
<table>
  <tr><th>{{if .IsGo}}{{bi "Package" "패키지"}}{{else}}{{bi "Module" "모듈"}}{{end}}</th>{{if not .IsGo}}<th>{{bi "Go package" "Go 패키지"}}</th>{{end}}<th class="num">{{bi "Files" "파일"}}</th><th class="num">{{bi "Lines" "라인"}}</th><th class="num">{{bi "Exported" "내보냄"}}</th><th class="num">{{bi "Functions" "함수"}}</th></tr>
  {{- range .Packages}}
  <tr>
    <td class="path nowrap" title="{{.ImportPath}}">{{rel $m .ImportPath}}{{if .Internal}} <span class="badge info">internal</span>{{end}}{{if .File}} <span class="note">{{.File}}</span>{{else}}{{with dirNote $m .ImportPath .Dir}} <span class="note">dir {{.}}</span>{{end}}{{end}}</td>
    {{- if not $.IsGo}}
    <td class="path nowrap">{{$.GoPackageOf .ImportPath}}</td>
    {{- end}}
    <td class="num">{{.Files}}</td>
    <td class="num">{{.Lines}}</td>
    <td class="num">{{.Exported}}</td>
    <td class="num">{{.Funcs}}</td>
  </tr>
  {{- end}}
</table>

{{- if .Unreached}}
<details>
<summary>{{bi (printf "Exported functions nothing in the module reaches (%d)" (len .Unreached)) (printf "모듈 안 어디서도 도달하지 않는 내보낸 함수 (%d개)" (len .Unreached))}}</summary>
<p class="note">No static call and no use as a value inside the module, outside package main.
They may be called by tests, by other modules, or only through reflection.</p>
<ul class="paths">
  {{- range .Unreached}}
  <li title="{{.}}">{{rel $m .}}</li>
  {{- end}}
</ul>
</details>
{{- end}}

{{- if .TypeErrors}}
<details>
{{- if .IsGo}}
<summary>{{bi (printf "Packages that did not type-check (%d)" (len .TypeErrors)) (printf "타입 검사를 통과하지 못한 패키지 (%d개)" (len .TypeErrors))}}</summary>
<p class="note">Their package rows are complete; their call data may be missing edges.</p>
{{- else}}
<summary>{{bi (printf "Modules that did not parse (%d)" (len .TypeErrors)) (printf "구문 분석에 실패한 모듈 (%d개)" (len .TypeErrors))}}</summary>
<p class="note">Their line counts are right, but their functions, imports and risks are missing. The analysis went on without them.</p>
{{- end}}
<table>
  <tr><th>{{if .IsGo}}{{bi "Package" "패키지"}}{{else}}{{bi "Module" "모듈"}}{{end}}</th><th>{{bi "Errors" "오류"}}</th></tr>
  {{- range .TypeErrors}}
  <tr>
    <td class="path" title="{{.ImportPath}}">{{rel $m .ImportPath}}</td>
    <td class="path">{{range .Errors}}<div>{{.}}</div>{{end}}</td>
  </tr>
  {{- end}}
</table>
</details>
{{- end}}

<details id="how">
<summary>{{bi "How to read this" "읽는 법"}}</summary>
<p><strong>{{bi "Readiness" "준비도"}}</strong> {{bi (printf "starts at 100 and loses fixed points for each reason found in the %s." .Unit) (printf "100에서 시작하여 %s에서 발견된 이유마다 정해진 점수를 깎습니다." $koUnit)}} {{bi "Each reason counts once, however many places cause it. It is a heuristic to order the work, not a measure of effort." "하나의 이유는 몇 군데서 나오든 한 번만 계산됩니다. 작업 순서를 정하는 휴리스틱이지 노력의 척도가 아닙니다."}}</p>
<ul>
  {{- range .ReadinessWeights}}
  <li>{{.Reason}}: -{{.Points}}</li>
  {{- end}}
</ul>
{{- if .IsGo}}
<p><strong>{{bi "Wave" "웨이브"}}</strong> {{bi "is the depth in the import graph inside the module. Wave 0 imports no other package of the module. A package in wave N imports at least one package of wave N-1 and none of a later wave, so porting wave by wave always has its dependencies done." "는 모듈 안 임포트 그래프에서의 깊이입니다. 웨이브 0은 모듈의 다른 패키지를 임포트하지 않습니다. 웨이브 N의 패키지는 최소한 웨이브 N-1의 패키지 하나를 임포트하고 더 늦은 웨이브는 임포트하지 않으므로, 웨이브별 포팅은 의존성이 항상 먼저 끝나 있습니다."}}</p>
{{- else}}
<p><strong>{{bi "Wave" "웨이브"}}</strong> {{bi "is the depth in the import graph inside the repo. Wave 0 imports no other module of the repo. A module in wave N imports at least one module of wave N-1. Modules in an import cycle are marked import_cycle; the cycle has to be broken or merged into one Go package, and their wave is only approximate." "는 저장소 안 임포트 그래프에서의 깊이입니다. 웨이브 0은 저장소의 다른 모듈을 임포트하지 않습니다. 웨이브 N의 모듈은 최소한 웨이브 N-1의 모듈 하나를 임포트합니다. 임포트 순환에 있는 모듈은 import_cycle으로 표시되며, 순환은 끊거나 하나의 Go 패키지로 합쳐야 하고 그 웨이브는 근사치입니다."}}</p>
<p><strong>{{bi "Go package" "Go 패키지"}}</strong> {{bi "is a suggestion for where the module lands in the rewrite: its path in lower case, with characters Go does not allow in a package name dropped and Go keywords suffixed with pkg. Several small modules may well belong in one Go package instead." "는 재작성에서 그 모듈이 놓일 위치의 제안입니다. 경로를 소문자로 쓰고, Go가 패키지 이름에 허용하지 않는 문자는 빼며, Go 키워드에는 pkg를 분입니다. 작은 모듈 여러 개는 하나의 Go 패키지로 묶는 것이 나을 수도 있습니다."}}</p>
{{- end}}
{{- if and .Functions .IsGo}}
<p><strong>{{bi "Dynamic calls." "동적 호출."}}</strong> {{bi "Call edges come from a static walk of function bodies. Calls through an interface method or a function value cannot be resolved that way; they are counted as dynamic calls and are not edges. The share on the card is dynamic calls over static edges plus dynamic calls; edges count distinct caller and callee pairs while dynamic calls count call sites, so it is a rough indicator." "호출 간선은 함수 본문을 정적으로 훑어 나옵니다. 인터페이스 메서드나 함수 값을 통한 호출은 그렇게 해결할 수 없어 동적 호출로 세이며 간선이 아닙니다. 카드의 비율은 동적 호출을 정적 간선과 동적 호출의 합으로 나눈 것으로, 간선은 서로 다른 호출자-피호출자 쌍을 세고 동적 호출은 호출 지점을 세므로 대략적인 지표입니다."}}</p>
{{- end}}
{{- if .IsGo}}
<p><strong>{{bi "Tests are not walked." "테스트는 훑지 않습니다."}}</strong> {{bi "Only non-test files are read, so calls made from tests do not count as callers, and a package's own _test.go files only decide the \"no tests of its own\" reason." "테스트가 아닌 파일만 읽으므로, 테스트에서 하는 호출은 호출자로 세지 않고, 패키지 자신의 _test.go 파일은 \"자체 테스트 없음\" 이유만 결정합니다."}}</p>
{{- else}}
<p><strong>{{bi "Tests are not walked." "테스트는 훑지 않습니다."}}</strong> {{bi "Test files (test_*.py, *_test.py, *.test.ts, *.spec.js and the like) are not modules here. A test file counts for the module whose name it carries, and only decides the \"no tests of its own\" reason." "테스트 파일(test_*.py, *_test.py, *.test.ts, *.spec.js 등)은 여기서 모듈이 아닙니다. 테스트 파일은 이름이 붙은 모듈에 속하며 \"자체 테스트 없음\" 이유만 결정합니다."}}</p>
{{- end}}
</details>

<footer>{{bi "Generated by go-anything analyze. Each run writes a new version; earlier versions are kept." "go-anything analyze가 생성했습니다. 실행마다 새 버전을 남기며, 이전 버전은 보관됩니다."}}</footer>
</body>
</html>
`))
