package analyze

import (
	"slices"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// cFixture has three modules: util (a .c with its .h extending it), main (which includes util.h
// and calls system), net (which includes worker.h, a header only it uses, and spawns threads).
// A test file counts for util, and build output and prose are not source.
var cFixture = map[string]string{
	"src/util.h": `#ifndef UTIL_H
#define UTIL_H

int add(int a, int b);

static inline int twice(int x) {
    return x * 2;
}

#endif
`,
	"src/util.c": `#include <stdio.h>
#include "util.h"

int add(int a, int b) {
    return a + b;
}

int scale(int n, int by) {
    return add(n, by) * twice(by);
}
`,
	"src/main.c": `#include <stdio.h>
#include <stdlib.h>
#include "util.h"

int main(void) {
    char *msg = "system(\"ls\")"; /* fork() */
    if (add(1, 2) > 0) {
        system("ls");
    }
    return scale(3, 4) + twice(9);
}
`,
	"src/net.c": `#include <pthread.h>
#include "worker.h"

static void *run(void *arg) {
    return arg;
}

void spawn(void) {
    pthread_t t;
    pthread_create(&t, NULL, run, NULL);
    fork();
}
`,
	"src/worker.h": `#ifndef WORKER_H
#define WORKER_H

void spawn(void);

#endif
`,
	"tests/test_util.c": `#include "../src/util.h"

int main(void) {
    return add(1, 1) - 2;
}
`,
	"build/out.c":           "int hidden(void) { return 1; }\n",
	"README.md":             "# demo\n",
	"stale.h.bak":           "not scanned\n",
	"node_modules/x/deps.c": "int hidden(void) { return 2; }\n",
}

func TestAnalyzeCRepo(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, cFixture)
	a, err := Source{}.Analyze(root)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.Language != LangC || a.Module == "" || len(a.Limits) == 0 {
		t.Fatalf("language %q module %q limits %d", a.Language, a.Module, len(a.Limits))
	}
	if len(a.Packages) != 3 {
		t.Fatalf("want util, main and net, got %+v", a.Packages)
	}
	util := pkgByPath(t, a, "src/util")
	main := pkgByPath(t, a, "src/main")
	net := pkgByPath(t, a, "src/net")

	if util.GoPackage != "internal/util" || main.GoPackage != "internal/mainpkg" || net.GoPackage != "internal/net" {
		t.Errorf("go packages: %q %q %q", util.GoPackage, main.GoPackage, net.GoPackage)
	}
	// the header with util.c's stem extends that module; worker.h extends the one module that
	// includes it; angle includes are system and never imports.
	if !slices.Equal(util.Files, []string{"util.c", "util.h"}) || !slices.Equal(net.Files, []string{"net.c", "worker.h"}) {
		t.Errorf("files: util %v net %v", util.Files, net.Files)
	}
	if util.Lines != 20 || main.Lines != 11 || net.Lines != 18 {
		t.Errorf("lines: util %d main %d net %d", util.Lines, main.Lines, net.Lines)
	}
	if len(util.Imports) != 0 || !slices.Equal(main.Imports, []string{"src/util"}) || len(net.Imports) != 0 {
		t.Errorf("imports: util %v main %v net %v", util.Imports, main.Imports, net.Imports)
	}
	if !slices.Equal(util.Funcs, []string{"add", "scale", "twice"}) || !slices.Equal(util.Exported, []string{"add", "scale"}) {
		t.Errorf("util funcs %v exported %v", util.Funcs, util.Exported)
	}
	if !slices.Equal(net.Funcs, []string{"run", "spawn"}) || !slices.Equal(net.Exported, []string{"spawn"}) {
		t.Errorf("net funcs %v exported %v (run is static)", net.Funcs, net.Exported)
	}
	if util.Tests != 1 || main.Tests != 0 || net.Tests != 0 {
		t.Errorf("tests: util %d main %d net %d", util.Tests, main.Tests, net.Tests)
	}
	var add, scale, twice, cmain, spawn domain.FuncInfo
	for _, f := range util.Functions {
		switch f.Name {
		case "add":
			add = f
		case "scale":
			scale = f
		case "twice":
			twice = f
		}
	}
	for _, f := range main.Functions {
		if f.Name == "main" {
			cmain = f
		}
	}
	for _, f := range net.Functions {
		if f.Name == "spawn" {
			spawn = f
		}
	}
	if add.Pos != "src/util.c:4" || add.Signature != "(int a, int b)" || !add.Exported || add.Callers != 2 {
		t.Errorf("add: %+v", add)
	}
	if twice.Pos != "src/util.h:6" || twice.Exported || twice.Signature != "(int x)" {
		t.Errorf("twice (static, defined in the header): %+v", twice)
	}
	if !slices.Equal(scale.Callees, []string{"src/util.add", "src/util.twice"}) {
		t.Errorf("scale callees: %v", scale.Callees)
	}
	if cmain.Pos != "src/main.c:5" || !slices.Equal(cmain.Callees, []string{"src/util.add", "src/util.scale", "src/util.twice"}) {
		t.Errorf("main: %+v", cmain)
	}
	if spawn.Pos != "src/net.c:8" || len(spawn.Callees) != 0 {
		t.Errorf("spawn: %+v (run is passed as a function pointer, so the call graph cannot see it)", spawn)
	}
	for _, want := range []struct {
		pkg               domain.Package
		kind, pos, detail string
	}{
		{main, domain.RiskSideEffects, "src/main.c:8", "calls system"},
		{net, domain.RiskConcurrency, "src/net.c:10", "calls pthread_create"},
		{net, domain.RiskConcurrency, "src/net.c:11", "calls fork"},
	} {
		if !hasRisk(want.pkg, want.kind, want.pos) {
			t.Errorf("missing %s at %s (%s)", want.kind, want.pos, want.detail)
		}
	}
	if len(util.RiskSites) != 0 {
		t.Errorf("comments, strings and directives must not produce findings: %+v", util.RiskSites)
	}
	if len(main.RiskSites) != 1 || len(net.RiskSites) != 2 {
		t.Errorf("risk counts: main %+v net %+v", main.RiskSites, net.RiskSites)
	}

	wave := map[string]int{}
	for _, r := range domain.PortPlan(a) {
		wave[r.ImportPath] = r.Wave
	}
	if wave["src/util"] != 0 || wave["src/net"] != 0 || wave["src/main"] != 1 {
		t.Errorf("waves: %v", wave)
	}
	// util is the only leaf without concurrency: its exported functions are the first ports,
	// the most called one first. net is a leaf but has thread risks; main imports util.
	if len(a.StartHere) != 2 || a.StartHere[0].Func != "src/util.add" || a.StartHere[0].Importers != 1 ||
		a.StartHere[1].Func != "src/util.scale" {
		t.Errorf("start_here: %+v", a.StartHere)
	}
	for _, s := range a.StartHere {
		if s.Package == "src/net" || s.Package == "src/main" {
			t.Errorf("net has thread risks and main imports util; neither is a first port: %+v", s)
		}
	}
}

func TestAnalyzeCSharedHeader(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"lib/api.h": "#ifndef API_H\n#define API_H\n\nint load(void);\n\n#endif\n",
		"lib/a.c":   "#include \"api.h\"\n\nint load(void) {\n    return 1;\n}\n",
		"lib/b.c":   "#include \"api.h\"\n\nint other(void) {\n    return load() + 1;\n}\n",
	})
	a, err := CSource{}.Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Packages) != 3 {
		t.Fatalf("a header included by two modules is a module of its own: %+v", a.Packages)
	}
	api := pkgByPath(t, a, "lib/api")
	am := pkgByPath(t, a, "lib/a")
	b := pkgByPath(t, a, "lib/b")
	if !slices.Equal(api.Files, []string{"api.h"}) || api.Lines != 6 || len(api.Funcs) != 0 {
		t.Errorf("api: %+v", api)
	}
	if !slices.Equal(am.Imports, []string{"lib/api"}) || !slices.Equal(b.Imports, []string{"lib/api"}) {
		t.Errorf("imports: a %v b %v", am.Imports, b.Imports)
	}
	wave := map[string]int{}
	for _, r := range domain.PortPlan(a) {
		wave[r.ImportPath] = r.Wave
	}
	if wave["lib/api"] != 0 || wave["lib/a"] != 1 || wave["lib/b"] != 1 {
		t.Errorf("waves: %v", wave)
	}
	if len(a.StartHere) != 0 {
		t.Errorf("no module has an exported leaf function: %+v", a.StartHere)
	}
}

func TestAnalyzeCMaskKeepsOffsets(t *testing.T) {
	cases := []struct {
		name string
		src  string
		gone []string
		kept []string
		inc  []cInclude
	}{
		{
			name: "strings, comments and directives",
			src:  "#include \"util.h\"\nchar *s = \"fork()\"; // fork()\n/* system(1) */ int x;\n#define M(x) system(x)\n",
			gone: []string{"fork()", "system(", "#include", "util.h", "#define"},
			kept: []string{"int x;"},
			inc:  []cInclude{{target: "util.h", line: 1}},
		},
		{
			name: "system includes are never collected",
			src:  "#include <stdio.h>\nint y;\n",
			gone: []string{"stdio"},
			kept: []string{"int y;"},
		},
		{
			name: "a continued directive is one blank",
			src:  "#define M(x) \\\n    fork(x)\nint z;\n",
			gone: []string{"fork", "#define"},
			kept: []string{"int z;"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, inc := maskC([]byte(c.src))
			if len(m) != len(c.src) || strings.Count(string(m), "\n") != strings.Count(c.src, "\n") {
				t.Fatal("masking must keep every offset and newline")
			}
			for _, gone := range c.gone {
				if strings.Contains(string(m), gone) {
					t.Errorf("%q must be blanked: %q", gone, m)
				}
			}
			for _, kept := range c.kept {
				if !strings.Contains(string(m), kept) {
					t.Errorf("%q must stay: %q", kept, m)
				}
			}
			if !slices.Equal(inc, c.inc) {
				t.Errorf("includes: %+v want %+v", inc, c.inc)
			}
		})
	}
}

func TestAnalyzeDetectLanguageC(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
		err   string
	}{
		{"auto picks c", map[string]string{"a.c": "", "b.h": "", "x.md": ""}, LangC, ""},
		{"c beats js", map[string]string{"a.c": "", "b.c": "", "z.js": ""}, LangC, ""},
		{"c beats python", map[string]string{"a.py": "", "b.c": "", "c.c": ""}, LangC, ""},
		{"c and java tie", map[string]string{"a.c": "", "b.java": ""}, "", "-lang c or -lang java"},
		{"go.mod still wins", map[string]string{"go.mod": "module m\n", "a.c": "", "b.java": ""}, LangGo, ""},
		{"empty", map[string]string{"README.md": ""}, "", "supported: go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, c.files)
			got, err := DetectLanguage(root)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("want error with %q, got %q, %v", c.err, got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestAnalyzeCLangOverride(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.c": "int main(void) { return 0; }\n", "c2.c": "int other(void) { return 1; }\n", "b.java": "class B {}\n"})
	if a, err := (Source{}).Analyze(root); err != nil || a.Language != LangC {
		t.Fatalf("auto picks c: %v %v", a.Language, err)
	}
	if a, err := (Source{Lang: LangJava}).Analyze(root); err != nil || a.Language != LangJava {
		t.Fatalf("-lang java must analyze the Java files: %v %v", a.Language, err)
	}
}
