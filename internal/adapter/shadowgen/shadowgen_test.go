package shadowgen

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const origSrc = `package calc

import (
	"errors"
	"time"
)

// Config is shared by the original and the rewrite.
type Config struct{ Scale int }

func (c Config) Double() int { return 2 * c.Scale }

const Version = "1"

var Calls int

func Add(a, b int) int { return a + b }

func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("division by zero")
	}
	return a / b, nil
}

func Boom(n int) int { return n }

func Scaled(c Config, xs ...int) []int {
	out := make([]int, len(xs))
	for i, x := range xs {
		out[i] = x * c.Scale
	}
	return out
}

func Wait(d time.Duration) time.Duration { return d }

func Apply(f func(int) int, x int) int { return f(x) }

func Label(n int) string { return "n" }

func Fill(buf []byte) {
	for i := range buf {
		buf[i] = 1
	}
}
`

const rewriteSrc = `package calc

import (
	"time"

	"example.com/demo/calc"
)

type divError struct{}

func (divError) Error() string { return "division by zero" }

// Add disagrees with the original on exactly one input.
func Add(a, b int) int {
	if a == 2 && b == 2 {
		return 5
	}
	return a + b
}

// Div returns a different error type with the same text; that is not a mismatch.
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, divError{}
	}
	return a / b, nil
}

func Boom(n int) int {
	if n == 13 {
		panic("rewrite exploded")
	}
	return n
}

func Scaled(c calc.Config, xs ...int) []int {
	out := make([]int, len(xs))
	for i, x := range xs {
		out[i] = x * c.Scale
	}
	return out
}

func Wait(d time.Duration) time.Duration { return d }

func Apply(f func(int) int, x int) int { return f(x) }

func Label(n int64) string { return "n" }

func Fill(buf []byte) {
	for i := range buf {
		buf[i] = 1
	}
}
`

// The caller program imports the shadow package instead of the original.
const checkSrc = `package main

import (
	"fmt"
	"os"

	calc "example.com/demo/shadow/calc"
)

func main() {
	switch os.Args[1] {
	case "add":
		fmt.Printf("add13=%d add22=%d\n", calc.Add(1, 3), calc.Add(2, 2))
	case "equal":
		q, err := calc.Div(6, 3)
		_, zerr := calc.Div(1, 0)
		fmt.Printf("add13=%d div=%d,%v zero=%v scaled=%v wait=%v apply=%d\n", calc.Add(1, 3), q, err, zerr,
			calc.Scaled(calc.Config{Scale: 3}, 1, 2), calc.Wait(7), calc.Apply(func(x int) int { return x + 1 }, 1))
	case "boom":
		fmt.Printf("boom=%d\n", calc.Boom(13))
	case "types":
		fmt.Printf("double=%d version=%s\n", calc.Config{Scale: 4}.Double(), calc.Version)
	}
}
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func demoModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.22\n")
	writeFile(t, filepath.Join(root, "calc", "calc.go"), origSrc)
	writeFile(t, filepath.Join(root, "rewrite", "calc", "calc.go"), rewriteSrc)
	return root
}

func generateDemo(t *testing.T) (string, Report) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	root := demoModule(t)
	rep, err := Generate(Options{ModuleRoot: root, Original: "example.com/demo/calc", Rewrite: "example.com/demo/rewrite/calc"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "cmd", "check", "main.go"), checkSrc)
	return root, rep
}

// runCheck runs the caller program with go run and returns stdout, stderr and the mismatch log lines.
func runCheck(t *testing.T, root, mode, what string, extraEnv ...string) (string, string, []map[string]any) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "mismatch.jsonl")
	cmd := exec.Command("go", "run", "./cmd/check", what)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GA_SHADOW_CALC="+mode, "GA_SHADOW_LOG="+logPath, "GOWORK=off")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run (mode %s, %s): %v\n%s", mode, what, err, stderr.String())
	}
	var lines []map[string]any
	if f, err := os.Open(logPath); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("log line is not JSON: %q: %v", sc.Text(), err)
			}
			lines = append(lines, m)
		}
	}
	return stdout.String(), stderr.String(), lines
}

func TestShadowGenModes(t *testing.T) {
	root, rep := generateDemo(t)
	t.Logf("generated %v", rep.Files)

	out, _, logs := runCheck(t, root, "old", "add")
	if out != "add13=4 add22=4\n" || len(logs) != 0 {
		t.Fatalf("old mode must return the original: %q, logs %v", out, logs)
	}
	out, _, logs = runCheck(t, root, "new", "add")
	if out != "add13=4 add22=5\n" || len(logs) != 0 {
		t.Fatalf("new mode must return the rewrite: %q, logs %v", out, logs)
	}
	out, _, logs = runCheck(t, root, "shadow", "add")
	if out != "add13=4 add22=4\n" {
		t.Fatalf("shadow mode must return the original: %q", out)
	}
	if len(logs) != 1 {
		t.Fatalf("shadow mode must log exactly one mismatch, got %d: %v", len(logs), logs)
	}
	m := logs[0]
	t.Logf("mismatch line: %v", m)
	if m["func"] != "Add" || m["args_repr"] != "2, 2" || m["time"] == "" {
		t.Fatalf("unexpected mismatch record: %v", m)
	}
	if o, r := m["original"].([]any), m["rewrite"].([]any); len(o) != 1 || o[0] != "4" || len(r) != 1 || r[0] != "5" {
		t.Fatalf("mismatch record must carry both results: %v", m)
	}
}

func TestShadowGenEqualInputsLogNothing(t *testing.T) {
	root, _ := generateDemo(t)
	out, _, logs := runCheck(t, root, "shadow", "equal")
	want := "add13=4 div=2,<nil> zero=division by zero scaled=[3 6] wait=7ns apply=2\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	if len(logs) != 0 {
		t.Fatalf("equal results (errors with equal text) must log nothing, got %v", logs)
	}
}

func TestShadowGenRewritePanicRecovered(t *testing.T) {
	root, _ := generateDemo(t)
	out, _, logs := runCheck(t, root, "shadow", "boom")
	if out != "boom=13\n" {
		t.Fatalf("the caller must get the original's result: %q", out)
	}
	if len(logs) != 1 || logs[0]["func"] != "Boom" {
		t.Fatalf("the rewrite panic must be one mismatch line: %v", logs)
	}
	if r := logs[0]["rewrite"].([]any); len(r) != 1 || r[0] != "panic: rewrite exploded" {
		t.Fatalf("rewrite side must record the panic: %v", logs[0])
	}
}

func TestShadowGenRedactOmitsValues(t *testing.T) {
	root, _ := generateDemo(t)
	_, _, logs := runCheck(t, root, "shadow", "add", "GA_SHADOW_REDACT=1")
	if len(logs) != 1 || logs[0]["func"] != "Add" || logs[0]["args_repr"] != "[redacted]" {
		t.Fatalf("redacted record must keep only func and time: %v", logs)
	}
	if o := logs[0]["original"].([]any); len(o) != 1 || o[0] != "[redacted]" {
		t.Fatalf("results must be redacted: %v", logs[0])
	}
}

func TestShadowGenUnknownModeWarnsOnce(t *testing.T) {
	root, _ := generateDemo(t)
	out, stderr, logs := runCheck(t, root, "sideways", "add")
	if out != "add13=4 add22=4\n" || len(logs) != 0 {
		t.Fatalf("an unknown mode must behave as old: %q %v", out, logs)
	}
	if n := strings.Count(stderr, "is not old, shadow or new"); n != 1 {
		t.Fatalf("want exactly one warning, got %d in %q", n, stderr)
	}
}

func TestShadowGenReexportsTypes(t *testing.T) {
	root, _ := generateDemo(t)
	out, _, _ := runCheck(t, root, "old", "types")
	if out != "double=8 version=1\n" {
		t.Fatalf("types and consts must be re-exported: %q", out)
	}
}

func TestShadowGenReport(t *testing.T) {
	_, rep := generateDemo(t)
	want := map[string]string{
		"Label":         "signature mismatch",
		"Apply":         "parameter f holds a func",
		"Config.Double": "method",
		"Calls":         "package variable",
	}
	got := map[string]Skip{}
	for _, s := range rep.NotShadowed {
		got[s.Name] = s
	}
	for name, frag := range want {
		if !strings.Contains(got[name].Reason, frag) {
			t.Errorf("%s: want reason containing %q, got %+v", name, frag, got[name])
		}
	}
	if strings.Join(rep.Shadowed, ",") != "Add,Boom,Div,Fill,Scaled,Wait" {
		t.Errorf("shadowed = %v", rep.Shadowed)
	}
	risks := map[string]string{}
	for _, r := range rep.Risks {
		risks[r.Name] = r.Reason
	}
	if !strings.Contains(risks["Fill"], "parameter buf is a slice") || !strings.Contains(risks["Scaled"], "slice") {
		t.Errorf("risks = %v", rep.Risks)
	}
	if _, ok := risks["Add"]; ok {
		t.Errorf("Add has only value parameters and is no risk")
	}
	if rep.EnvVar != "GA_SHADOW_CALC" || len(rep.Files) != 2 {
		t.Errorf("report = %+v", rep)
	}
	head, err := os.ReadFile(rep.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"Code generated by go-anything shadow gen. DO NOT EDIT.", "//   - Apply: parameter f holds a func", "Methods are not shadowed"} {
		if !bytes.Contains(head, []byte(frag)) {
			t.Errorf("generated header lacks %q", frag)
		}
	}
}

func TestShadowGenGeneratedTestRuns(t *testing.T) {
	// A go test inside the temp module, with the race detector when cgo is available, so concurrent
	// shadow calls exercise the log mutex.
	root, _ := generateDemo(t)
	writeFile(t, filepath.Join(root, "shadow", "calc", "concurrent_test.go"), `package calc

import (
	"bufio"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentMismatches(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("GA_SHADOW_CALC", "shadow")
	t.Setenv("GA_SHADOW_LOG", log)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = Add(2, 2); _ = Add(1, 1) }()
	}
	wg.Wait()
	f, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	for sc := bufio.NewScanner(f); sc.Scan(); n++ {
	}
	if n != 50 {
		t.Fatalf("want 50 lines, got %d", n)
	}
}
`)
	args := []string{"test", "-count=1", "./shadow/calc/"}
	if out, err := exec.Command("go", "env", "CGO_ENABLED").Output(); err == nil && strings.TrimSpace(string(out)) == "1" {
		args = []string{"test", "-race", "-count=1", "./shadow/calc/"}
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	t.Logf("go %s: %s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestShadowGenRefusesNonEmptyDir(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root := demoModule(t)
	writeFile(t, filepath.Join(root, "shadow", "calc", "keep.txt"), "mine")
	_, err := Generate(Options{ModuleRoot: root, Original: "example.com/demo/calc", Rewrite: "example.com/demo/rewrite/calc"})
	if err == nil || !strings.Contains(err.Error(), "already has content") {
		t.Fatalf("want refusal, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "shadow", "calc", "keep.txt")); string(b) != "mine" {
		t.Fatal("existing content was touched")
	}
}

func TestShadowGenBuildFailureRemovesFiles(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root := demoModule(t)
	writeFile(t, filepath.Join(root, "broken", "broken.go"), "package broken\n\nvar x int = \"not an int\"\n")
	out := filepath.Join(root, "shadow", "calc")
	_, err := Generate(Options{ModuleRoot: root, Original: "example.com/demo/calc", Rewrite: "example.com/demo/rewrite/calc"})
	if err == nil || !strings.Contains(err.Error(), "go build ./...") {
		t.Fatalf("want build error, got %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("generated dir must be removed, stat: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "shadow")); !os.IsNotExist(statErr) {
		t.Fatalf("created parent must be removed, stat: %v", statErr)
	}
}

func TestShadowGenDryRunWritesNothing(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root := demoModule(t)
	rep, err := Generate(Options{ModuleRoot: root, Original: "example.com/demo/calc", Rewrite: "example.com/demo/rewrite/calc", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 0 || len(rep.Shadowed) == 0 {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(root, "shadow")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote something")
	}
}
