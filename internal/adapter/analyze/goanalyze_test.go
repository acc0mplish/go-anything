package analyze

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

const mod = "example.com/t"

// fixture is a module with known call relations:
//
//	main.main -> a.A -> b.B -(interface)-> impl.Get -> b.C -> b.F, b.Id[int]
//	a.A -> a.helper; b.D -> b.F, b.Id (D is exported and unreached)
//	b.E starts a goroutine and assigns a package variable; b has an init; r.go imports reflect and unsafe
//	bad does not type-check; b_windows.go is excluded by its build constraint off Windows
var fixture = map[string]string{
	"go.mod": "module " + mod + "\n\ngo 1.22\n",
	"cmd/app/main.go": `package main

import "example.com/t/a"

func main() { a.A() }

func Run() {}
`,
	"a/a.go": `package a

import "example.com/t/b"

func A() int { return b.B() + helper() }

func helper() int { return 1 }
`,
	"b/b.go": `package b

import "io"

type Getter interface{ Get() int }

type impl struct{}

func (impl) Get() int { return C() }

func C() int { return F() + Id[int](2) }

func D() int { return F() + Id(1) }

func F() int { return 2 }

func Id[T any](x T) T { return x }

var g Getter = impl{}

func B() int { return g.Get() }

var counter int

func E() {
	go func() {}()
	counter++
}

func G(r io.Reader) int { return 0 }

func init() {}
`,
	"b/r.go": `package b

import (
	"reflect"
	"unsafe"
)

func Size(x any) uintptr { _ = reflect.TypeOf(x); return unsafe.Sizeof(0) }
`,
	"b/b_windows.go": `//go:build windows

package b

func D() int { return 5 }
`,
	"bad/bad.go": `package bad

func Bad() int { return "x" }

func Good() {}
`,
}

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range fixture {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// at returns "file:line" of the first line in the fixture file that contains s.
func at(t *testing.T, file, s string) string {
	t.Helper()
	for i, line := range strings.Split(fixture[file], "\n") {
		if strings.Contains(line, s) {
			return file + ":" + strconv.Itoa(i+1)
		}
	}
	t.Fatalf("fixture %s has no %q", file, s)
	return ""
}

func analyzeFixture(t *testing.T) domain.Analysis {
	t.Helper()
	a, err := GoSource{}.Analyze(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func pkg(t *testing.T, a domain.Analysis, path string) domain.Package {
	t.Helper()
	for _, p := range a.Packages {
		if p.ImportPath == path {
			return p
		}
	}
	t.Fatalf("no package %s", path)
	return domain.Package{}
}

func fn(t *testing.T, p domain.Package, receiver, name string) domain.FuncInfo {
	t.Helper()
	for _, f := range p.Functions {
		if f.Receiver == receiver && f.Name == name {
			return f
		}
	}
	t.Fatalf("no function %s.%s in %s", receiver, name, p.ImportPath)
	return domain.FuncInfo{}
}

func TestCallGraphEdgesAndDynamicCalls(t *testing.T) {
	a := analyzeFixture(t)
	b := pkg(t, a, mod+"/b")
	pa := pkg(t, a, mod+"/a")

	if got, want := fn(t, pa, "", "A").Callees, []string{mod + "/a.helper", mod + "/b.B"}; !slices.Equal(got, want) {
		t.Fatalf("A callees = %v, want %v", got, want)
	}
	// b.B calls through the Getter interface: not an edge, one dynamic call.
	if f := fn(t, b, "", "B"); len(f.Callees) != 0 || f.DynamicCalls != 1 {
		t.Fatalf("B must have no static callee and one dynamic call: %+v", f)
	}
	if got := fn(t, b, "impl", "Get").Callees; !slices.Equal(got, []string{mod + "/b.C"}) {
		t.Fatalf("impl.Get callees = %v", got)
	}
	// Explicit and inferred instantiation both resolve to the generic function.
	if got := fn(t, b, "", "C").Callees; !slices.Equal(got, []string{mod + "/b.F", mod + "/b.Id"}) {
		t.Fatalf("C callees = %v", got)
	}
	if f := fn(t, b, "", "Id"); f.Callers != 2 || f.PlainSignature {
		t.Fatalf("Id has 2 callers and type parameters: %+v", f)
	}
	if f := fn(t, b, "", "F"); f.Callers != 2 || f.Signature != "func() int" {
		t.Fatalf("F: %+v", f)
	}
	if f := fn(t, b, "", "G"); f.Signature != "func(r io.Reader) int" || f.PlainSignature {
		t.Fatalf("G signature is relative to b and io.Reader is not plain: %+v", f)
	}
	// main->A, A->B, A->helper, Get->C, C->F, C->Id, D->F, D->Id
	if a.CallEdges != 8 {
		t.Fatalf("call edges = %d, want 8", a.CallEdges)
	}
	if a.DynamicCalls != 1 {
		t.Fatalf("dynamic calls = %d, want 1 (go func(){}() calls a literal, not a value)", a.DynamicCalls)
	}
	if f := fn(t, b, "", "E"); f.GoStatements != 1 || f.DynamicCalls != 0 {
		t.Fatalf("E: %+v", f)
	}
}

func TestUnreachedAndStartHere(t *testing.T) {
	a := analyzeFixture(t)
	// main.Run is unreached but in package main; bad.* are in a package that failed to check
	// but still listed, since nothing calls them.
	want := []string{mod + "/b.D", mod + "/b.E", mod + "/b.G", mod + "/b.Size", mod + "/bad.Bad", mod + "/bad.Good"}
	if !slices.Equal(a.Unreached, want) {
		t.Fatalf("unreached = %v, want %v", a.Unreached, want)
	}
	var got []string
	for _, s := range a.StartHere {
		got = append(got, s.Func)
	}
	// F (2 callers), C (1), D (0). Not B (dynamic), E (goroutine), G (io.Reader), Id (generic),
	// impl.Get (unexported receiver), A (calls into b), Size (any), bad (type errors), main.
	if wantSH := []string{mod + "/b.F", mod + "/b.C", mod + "/b.D"}; !slices.Equal(got, wantSH) {
		t.Fatalf("start_here = %v, want %v", got, wantSH)
	}
	if a.StartHere[0].Pos != at(t, "b/b.go", "func F()") || a.StartHere[0].Callers != 2 {
		t.Fatalf("start_here[0] = %+v", a.StartHere[0])
	}
}

func TestRiskSitesHaveFileAndLine(t *testing.T) {
	a := analyzeFixture(t)
	b := pkg(t, a, mod+"/b")
	want := map[string]string{
		domain.RiskGoroutine:     at(t, "b/b.go", "go func"),
		domain.RiskGlobalMutable: at(t, "b/b.go", "var counter"),
		domain.RiskInit:          at(t, "b/b.go", "func init"),
		domain.RiskReflect:       at(t, "b/r.go", `"reflect"`),
		domain.RiskUnsafe:        at(t, "b/r.go", `"unsafe"`),
	}
	got := map[string]string{}
	for _, s := range b.RiskSites {
		if _, dup := got[s.Kind]; dup {
			t.Fatalf("one site per kind expected in the fixture, got %+v", b.RiskSites)
		}
		got[s.Kind] = s.Pos
	}
	for kind, pos := range want {
		if got[kind] != pos {
			t.Fatalf("%s at %q, want %q (all: %+v)", kind, got[kind], pos, b.RiskSites)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected risk sites: %+v", b.RiskSites)
	}
	for _, s := range b.RiskSites {
		if s.Kind == domain.RiskGlobalMutable && !strings.Contains(s.Detail, "E at "+at(t, "b/b.go", "counter++")) {
			t.Fatalf("global var detail must name the assigning function and place: %q", s.Detail)
		}
	}
	// The plan names the places next to the reasons.
	for _, row := range domain.PortPlan(a) {
		if row.ImportPath == mod+"/b" && !strings.Contains(strings.Join(row.Risks, "|"), "goroutines: 1 go statement(s), ordering is not deterministic at "+want[domain.RiskGoroutine]) {
			t.Fatalf("plan risks must carry the go statement position: %v", row.Risks)
		}
	}
}

func TestTypeErrorKeepsSyntaxData(t *testing.T) {
	a := analyzeFixture(t)
	bad := pkg(t, a, mod+"/bad")
	if !slices.Equal(bad.Funcs, []string{"Bad", "Good"}) || !slices.Equal(bad.Exported, []string{"Bad", "Good"}) {
		t.Fatalf("syntax level data must survive a type error: %+v", bad)
	}
	if len(bad.TypeErrors) != 1 || !strings.HasPrefix(bad.TypeErrors[0], at(t, "bad/bad.go", "func Bad")+": ") {
		t.Fatalf("type errors = %v", bad.TypeErrors)
	}
	if len(bad.Functions) != 2 {
		t.Fatalf("functions of a failed package are still listed: %+v", bad.Functions)
	}
	for _, p := range a.Packages {
		if p.ImportPath != mod+"/bad" && len(p.TypeErrors) > 0 {
			t.Fatalf("only bad may have type errors, %s has %v", p.ImportPath, p.TypeErrors)
		}
	}
}

func TestTypesAndPackageNames(t *testing.T) {
	a := analyzeFixture(t)
	b := pkg(t, a, mod+"/b")
	if b.Name != "b" || pkg(t, a, mod+"/cmd/app").Name != "main" {
		t.Fatalf("package names wrong")
	}
	kinds := map[string]domain.TypeInfo{}
	for _, ty := range b.Types {
		kinds[ty.Name] = ty
	}
	if g := kinds["Getter"]; g.Kind != "interface" || !slices.Equal(g.Methods, []string{"Get"}) || g.Pos != at(t, "b/b.go", "type Getter") {
		t.Fatalf("Getter: %+v", g)
	}
	if i := kinds["impl"]; i.Kind != "struct" || !slices.Equal(i.Methods, []string{"Get"}) {
		t.Fatalf("impl: %+v", i)
	}
}

func TestBuildConstraintsExcludeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture's excluded file targets windows")
	}
	a := analyzeFixture(t)
	b := pkg(t, a, mod+"/b")
	if !slices.Equal(b.BuildIgnored, []string{"b_windows.go"}) {
		t.Fatalf("build ignored = %v", b.BuildIgnored)
	}
	if !slices.Contains(b.Files, "b_windows.go") {
		t.Fatal("syntax level file list still covers every file")
	}
	n := 0
	for _, f := range b.Functions {
		if f.Name == "D" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("D must be listed once, from the file that builds here; got %d", n)
	}
}

// Without the go command the dependency list cannot be made; the "source" importer must still
// type-check the standard library imports.
func TestFallsBackWithoutGoList(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	a := analyzeFixture(t)
	for _, p := range a.Packages {
		if p.ImportPath != mod+"/bad" && len(p.TypeErrors) > 0 {
			t.Fatalf("%s: %v", p.ImportPath, p.TypeErrors)
		}
	}
	if a.CallEdges != 8 {
		t.Fatalf("call edges = %d, want 8", a.CallEdges)
	}
}

func TestValidImportPathRejectsFlagsAndTraversal(t *testing.T) {
	for _, p := range []string{"fmt", "github.com/jackc/pgx/v5", "gopkg.in/yaml.v3", "vendor/golang.org/x/net/idna"} {
		if !validImportPath(p) {
			t.Errorf("%q must be accepted", p)
		}
	}
	for _, p := range []string{"", "-toolexec=sh", "/abs", "./rel", "a/../b", "a//b", "C", "a b", "a;b"} {
		if validImportPath(p) {
			t.Errorf("%q must be rejected", p)
		}
	}
}
