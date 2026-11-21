package wiregen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// unitModule writes a module with one command-line unit whose rewrite is the given source, and returns
// its root. The rewrite also declares names the copied comparison uses unprefixed (EqualBody, Answer),
// so a missing prefix would fail to compile.
func unitModule(t *testing.T, rewrite string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs go test in a generated module")
	}
	root := t.TempDir()
	must := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/svc\n\ngo 1.27.4\n")
	must("units/greet/rewrite.go", rewrite)
	must("units/greet/bait.go", "package greet\n\n// EqualBody and Answer collide with the comparison if it is copied unprefixed.\nfunc EqualBody() {}\n\ntype Answer int\n")
	return root
}

func runUnitTests(t *testing.T, root string) (string, bool) {
	t.Helper()
	cmd := exec.Command("go", "test", "-count=1", "./units/greet/")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

const crashingRewrite = `package greet

import (
	"fmt"
	"io"
)

func Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "panic: division by zero (a Go message, not the Python traceback)")
	return %d
}
`

func TestGeneratedTestHonoursUnstableStderrLikeTheTool(t *testing.T) {
	crash := domain.AppCase{Name: "crash", Kind: "cli", Argv: []string{"greet", "0"}, ExitCode: 1,
		Stderr:   "Traceback (most recent call last):\n  File \"greet.py\", line 7\nZeroDivisionError: division by zero",
		Unstable: []string{"stderr"}}
	for _, tc := range []struct {
		name     string
		exitCode int
		pass     bool
	}{{"same exit code, different crash text", 1, true}, {"different exit code", 3, false}} {
		t.Run(tc.name, func(t *testing.T) {
			root := unitModule(t, strings.Replace(crashingRewrite, "%d", map[bool]string{true: "1", false: "3"}[tc.pass], 1))
			data, err := usecase.CLICasesTest("greet", []domain.AppCase{crash}, domain.CompareOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := WriteCases(root, "greet", data); err != nil {
				t.Fatal(err)
			}
			tool := usecase.CompareAnswersWith(crash, domain.AppCase{Kind: "cli", ExitCode: tc.exitCode, Stderr: "panic: other text"}, domain.CompareOptions{})
			out, ok := runUnitTests(t, root)
			if ok != tc.pass || (len(tool) == 0) != tc.pass {
				t.Fatalf("generated test passed=%v, tool agreed=%v, want both %v\n%s", ok, len(tool) == 0, tc.pass, out)
			}
			if !tc.pass && !strings.Contains(out, "exit_code") {
				t.Fatalf("the failure must name the field:\n%s", out)
			}
		})
	}
}

func TestGeneratedTestUsesTheJSONComparisonItWasGivenLikeTheTool(t *testing.T) {
	rewrite := "package greet\n\nimport (\n\t\"fmt\"\n\t\"io\"\n)\n\nfunc Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {\n\tfmt.Fprintln(stdout, `{\"total\":100,\"id\":1}`)\n\treturn 0\n}\n"
	want := domain.AppCase{Name: "order", Kind: "cli", Argv: []string{"greet", "1"}, Stdout: `{"id": 1, "total": 100}` + "\n"}
	for _, jsonBody := range []bool{true, false} {
		root := unitModule(t, rewrite)
		opt := domain.CompareOptions{JSONBody: jsonBody}
		data, err := usecase.CLICasesTest("greet", []domain.AppCase{want}, opt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := WriteCases(root, "greet", data); err != nil {
			t.Fatal(err)
		}
		out, ok := runUnitTests(t, root)
		tool := usecase.CompareAnswersWith(want, domain.AppCase{Kind: "cli", Stdout: `{"total":100,"id":1}` + "\n"}, opt)
		if ok != jsonBody || (len(tool) == 0) != jsonBody {
			t.Fatalf("json-body=%v: generated test passed=%v, tool agreed=%v; both must equal %v\n%s", jsonBody, ok, len(tool) == 0, jsonBody, out)
		}
	}
}

func TestGeneratedTestUsesTheNormalizersItWasGivenLikeTheTool(t *testing.T) {
	rewrite := "package greet\n\nimport (\n\t\"fmt\"\n\t\"io\"\n)\n\nfunc Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {\n\tfmt.Fprintf(stdout, \"{\\\"id\\\": 7, \\\"made\\\": \\\"%s\\\"}\\n\", \"2026-11-22T01:23:45Z\")\n\treturn 0\n}\n"
	want := domain.AppCase{Name: "order", Kind: "cli", Argv: []string{"greet"},
		Stdout: `{"id": 7, "made": "2020-01-01T00:00:00Z"}` + "\n"}
	opt := domain.CompareOptions{JSONBody: true, Normalize: []string{"timestamps", `port=[0-9]+`}}
	root := unitModule(t, rewrite)
	data, err := usecase.CLICasesTest("greet", []domain.AppCase{want}, opt)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, `"timestamps"`) || !strings.Contains(src, `"port=[0-9]+"`) {
		t.Fatalf("the generated test must carry the normalizer list verbatim:\n%s", src)
	}
	if _, err := WriteCases(root, "greet", data); err != nil {
		t.Fatal(err)
	}
	out, ok := runUnitTests(t, root)
	tool := usecase.CompareAnswersWith(want, domain.AppCase{Kind: "cli", Stdout: `{"id": 7, "made": "1999-12-31T23:59:59Z"}` + "\n"}, opt)
	if !ok || len(tool) != 0 {
		t.Fatalf("generated test passed=%v, tool agreed=%v; both must hold (a differing timestamp is masked)\n%s", ok, len(tool) == 0, out)
	}
}
