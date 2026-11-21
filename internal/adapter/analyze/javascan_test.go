package analyze

import (
	"slices"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// javaFixture has three packages under src/main/java: util (a leaf with a test), app (which
// imports util and runs threads and processes) and io (which reads and writes files). A test
// class under src/test/java counts for util, and prose is not source.
var javaFixture = map[string]string{
	"src/main/java/com/example/util/MathUtil.java": `package com.example.util;

public class MathUtil {

    public static int add(int a, int b) {
        return a + b;
    }

    static int triple(int x) {
        return x + x + x;
    }
}
`,
	"src/main/java/com/example/app/Main.java": `package com.example.app;

import com.example.util.MathUtil;
import java.util.List;

public class Main {

    public static void main(String[] args) {
        int sum = MathUtil.add(1, 2);
        System.out.println(sum);
        synchronized (Main.class) {
            spawn();
        }
    }

    private void spawn() {
        new Thread(() -> run()).start();
        Runtime.getRuntime().exec("ls");
    }

    void run() {
    }
}
`,
	"src/main/java/com/example/io/Store.java": `package com.example.io;

import java.io.InputStream;
import java.io.RandomAccessFile;
import java.nio.file.Files;
import java.nio.file.Path;

public class Store {

    public String read(Path p) throws Exception {
        InputStream in = Files.newInputStream(p);
        RandomAccessFile raf = new RandomAccessFile("data", "r");
        return "stored";
    }
}
`,
	"src/test/java/com/example/util/MathUtilTest.java": `package com.example.util;

import org.junit.Test;

public class MathUtilTest {

    @Test
    public void adds() {
        org.junit.Assert.assertEquals(3, MathUtil.add(1, 2));
    }
}
`,
	"README.md": "# demo\n",
}

func TestAnalyzeJavaRepo(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, javaFixture)
	a, err := Source{}.Analyze(root)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.Language != LangJava || a.Module == "" || len(a.Limits) == 0 {
		t.Fatalf("language %q module %q limits %d", a.Language, a.Module, len(a.Limits))
	}
	if len(a.Packages) != 3 {
		t.Fatalf("want util, app and io, got %+v", a.Packages)
	}
	util := pkgByPath(t, a, "com.example.util")
	app := pkgByPath(t, a, "com.example.app")
	io := pkgByPath(t, a, "com.example.io")

	if util.GoPackage != "internal/com/example/util" || app.GoPackage != "internal/com/example/app" || io.GoPackage != "internal/com/example/io" {
		t.Errorf("go packages: %q %q %q", util.GoPackage, app.GoPackage, io.GoPackage)
	}
	// only the repo's own package is an import; java.util and java.io are outside it
	if len(util.Imports) != 0 || !slices.Equal(app.Imports, []string{"com.example.util"}) || len(io.Imports) != 0 {
		t.Errorf("imports: util %v app %v io %v", util.Imports, app.Imports, io.Imports)
	}
	if !slices.Equal(util.Funcs, []string{"add", "triple"}) || !slices.Equal(util.Exported, []string{"add"}) {
		t.Errorf("util funcs %v exported %v (triple is package private)", util.Funcs, util.Exported)
	}
	if !slices.Equal(app.Funcs, []string{"main", "run", "spawn"}) || !slices.Equal(app.Exported, []string{"main"}) {
		t.Errorf("app funcs %v exported %v", app.Funcs, app.Exported)
	}
	if util.Tests != 1 || app.Tests != 0 || io.Tests != 0 {
		t.Errorf("tests: util %d app %d io %d", util.Tests, app.Tests, io.Tests)
	}
	if util.Lines != 12 || app.Lines != 23 || io.Lines != 15 {
		t.Errorf("lines: util %d app %d io %d", util.Lines, app.Lines, io.Lines)
	}
	var add, cmain, spawn, read domain.FuncInfo
	for _, f := range util.Functions {
		if f.Name == "add" {
			add = f
		}
	}
	for _, f := range app.Functions {
		switch f.Name {
		case "main":
			cmain = f
		case "spawn":
			spawn = f
		}
	}
	for _, f := range io.Functions {
		if f.Name == "read" {
			read = f
		}
	}
	if add.Pos != "src/main/java/com/example/util/MathUtil.java:5" || add.Signature != "(int a, int b)" || !add.Exported || add.Callers != 1 {
		t.Errorf("add: %+v", add)
	}
	if spawn.Pos != "src/main/java/com/example/app/Main.java:16" || spawn.Exported {
		t.Errorf("spawn (private): %+v", spawn)
	}
	if read.Pos != "src/main/java/com/example/io/Store.java:10" || read.Signature != "(Path p)" || !read.Exported {
		t.Errorf("read: %+v", read)
	}
	if !slices.Equal(cmain.Callees, []string{"com.example.app.spawn", "com.example.util.add"}) {
		t.Errorf("main callees: %v", cmain.Callees)
	}
	for _, want := range []struct {
		pkg               domain.Package
		kind, pos, detail string
	}{
		{app, domain.RiskConcurrency, "src/main/java/com/example/app/Main.java:11", "synchronized block"},
		{app, domain.RiskConcurrency, "src/main/java/com/example/app/Main.java:17", "new Thread"},
		{app, domain.RiskSideEffects, "src/main/java/com/example/app/Main.java:18", "Runtime exec"},
		{io, domain.RiskFSWrite, "src/main/java/com/example/io/Store.java:11", "uses InputStream"},
		{io, domain.RiskFSWrite, "src/main/java/com/example/io/Store.java:11", "calls Files.newInputStream"},
		{io, domain.RiskFSWrite, "src/main/java/com/example/io/Store.java:12", "uses RandomAccessFile"},
	} {
		if !hasRisk(want.pkg, want.kind, want.pos) {
			t.Errorf("missing %s at %s (%s): %+v", want.kind, want.pos, want.detail, want.pkg.RiskSites)
		}
	}
	if len(util.RiskSites) != 0 {
		t.Errorf("comments, strings and import lines must not produce findings: %+v", util.RiskSites)
	}
	if len(app.RiskSites) != 3 || len(io.RiskSites) != 3 {
		t.Errorf("risk counts: app %+v io %+v", app.RiskSites, io.RiskSites)
	}

	wave := map[string]int{}
	for _, r := range domain.PortPlan(a) {
		wave[r.ImportPath] = r.Wave
	}
	if wave["com.example.util"] != 0 || wave["com.example.io"] != 0 || wave["com.example.app"] != 1 {
		t.Errorf("waves: %v", wave)
	}
	// util is imported once and its add is called by main; io is a leaf whose file use does not
	// block a first port; app imports util and has concurrency risks.
	if len(a.StartHere) != 2 || a.StartHere[0].Func != "com.example.util.add" || a.StartHere[0].Importers != 1 ||
		a.StartHere[1].Func != "com.example.io.read" {
		t.Errorf("start_here: %+v", a.StartHere)
	}
	for _, s := range a.StartHere {
		if s.Package == "com.example.app" {
			t.Errorf("app imports util and runs threads; it is not a first port: %+v", s)
		}
	}
}

func TestAnalyzeJavaFallbackTree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"hello/Greeter.java": "package hello;\n\npublic class Greeter {\n\n    public String greet(String name) {\n        return \"hi \" + name;\n    }\n}\n",
		"helper/Tool.java":   "package helper;\n\nimport hello.Greeter;\n\npublic class Tool {\n\n    public int size() {\n        Greeter g = new Greeter();\n        return 1 + g.hashCode();\n    }\n}\n",
	})
	a, err := Source{}.Analyze(root)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.Language != LangJava || len(a.Packages) != 2 {
		t.Fatalf("without src/main/java the whole tree is the source: %v %+v", a.Language, a.Packages)
	}
	hello := pkgByPath(t, a, "hello")
	helper := pkgByPath(t, a, "helper")
	if hello.GoPackage != "internal/hello" || !slices.Equal(helper.Imports, []string{"hello"}) {
		t.Errorf("hello %q helper imports %v", hello.GoPackage, helper.Imports)
	}
	if !slices.Equal(hello.Funcs, []string{"greet"}) || !slices.Equal(hello.Exported, []string{"greet"}) {
		t.Errorf("hello funcs %v exported %v", hello.Funcs, hello.Exported)
	}
	wave := map[string]int{}
	for _, r := range domain.PortPlan(a) {
		wave[r.ImportPath] = r.Wave
	}
	if wave["hello"] != 0 || wave["helper"] != 1 {
		t.Errorf("waves: %v", wave)
	}
}

func TestAnalyzeJavaMaskKeepsOffsets(t *testing.T) {
	cases := []struct {
		name string
		src  string
		gone []string
		kept []string
	}{
		{
			name: "strings, chars and comments",
			src:  "String s = \"exec()\"; // fork()\n/* Runtime exec */ int x;\nchar c = 'x';\n",
			gone: []string{"exec()", "fork()", "Runtime exec"},
			kept: []string{"int x;", "char c = '"},
		},
		{
			name: "text blocks",
			src:  "String t = \"\"\"\n    system(\"ls\")\n    \"\"\";\nint y;\n",
			gone: []string{"system"},
			kept: []string{"int y;"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := maskJava([]byte(c.src))
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
		})
	}
}

func TestAnalyzeDetectLanguageJava(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
		err   string
	}{
		{"auto picks java", map[string]string{"src/main/java/d/Demo.java": "package d;\n", "x.md": ""}, LangJava, ""},
		{"java beats python", map[string]string{"a.py": "", "b.java": "", "c.java": ""}, LangJava, ""},
		{"java and js tie", map[string]string{"a.java": "", "b.js": ""}, "", "-lang js or -lang java"},
		{"four way tie", map[string]string{"a.py": "", "b.js": "", "c.c": "", "d.java": ""}, "", "-lang python or -lang js"},
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

func TestJavaScanFlagsConcreteStreamClasses(t *testing.T) {
	// FileOutputStream carries OutputStream with no word boundary before it; a \\b-anchored pattern
	// misses every concrete java.io class.
	f := scanJava([]byte("package p;\n\nclass A {\n    void save(String path) throws Exception {\n        FileOutputStream out = new FileOutputStream(path);\n        PrintWriter w = new PrintWriter(out);\n    }\n}\n"))
	kinds := map[int]string{}
	for _, r := range f.risks {
		kinds[r.line] = r.kind
	}
	if kinds[5] != domain.RiskFSWrite || kinds[6] != domain.RiskFSWrite {
		t.Fatalf("FileOutputStream and PrintWriter must both be fs_write risks, got %v", f.risks)
	}
}
