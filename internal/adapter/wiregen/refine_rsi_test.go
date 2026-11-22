package wiregen

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/frontdoorgen"
	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// rsiOrigScript answers "order:<id>" and crashes on 007 with a Python traceback that names a home
// directory, as a real original run from a developer's checkout would.
const rsiOrigScript = `#!/bin/sh
if [ "$1" = orders ]; then
  if [ -n "$2" ]; then v="$2"; else read -r v; fi
  if [ "$v" = 007 ]; then
    echo "Traceback (most recent call last):" >&2
    echo '  File "/home/alice/proj/orders.py", line 4, in <module>' >&2
    echo "ValueError: order 007 cannot be parsed" >&2
    exit 1
  fi
  printf 'order:%s\n' "$v"
  exit 0
fi
echo "usage: orig orders <id>" >&2
exit 1
`

// rsiRewrite is a rewrite of rsiOrigScript. crash007 makes it exit 1 on 007 with its own message, as a
// rewrite of an expected crash would. wrongABC makes it answer "abc" differently, a bug the probes miss.
func rsiRewrite(crash007, wrongABC bool) string {
	src := `package orders

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "orders" {
		fmt.Fprintln(stderr, "usage: orig orders <id>")
		return 1
	}
	v := ""
	if len(args) > 1 && args[1] != "" {
		v = args[1]
	} else {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		v = strings.Trim(line, " \t\r\n") // sh read strips only IFS whitespace; a NBSP survives it
	}
`
	if crash007 {
		src += "\tif v == \"007\" {\n\t\tfmt.Fprintln(stderr, \"bad order id\")\n\t\treturn 1\n\t}\n"
	}
	if wrongABC {
		src += "\tif v == \"abc\" {\n\t\tv = \"ABC\"\n\t}\n"
	}
	return src + "\tfmt.Fprintf(stdout, \"order:%s\\n\", v)\n\treturn 0\n}\n"
}

// rsiSetup writes the original and a module with the orders unit and the given rewrite.
func rsiSetup(t *testing.T, rewrite string) (root, orig, casesPath string) {
	t.Helper()
	dir := t.TempDir()
	orig = filepath.Join(dir, "orig.sh")
	if err := os.WriteFile(orig, []byte(rsiOrigScript), 0o755); err != nil {
		t.Fatal(err)
	}
	root = newModule(t)
	if _, err := Gen(root, "orders", ""); err != nil {
		t.Fatal(err)
	}
	writeRewrite(t, root, rewrite)
	casesPath = filepath.Join(dir, "app.json")
	if err := store.SaveApp(casesPath, domain.AppRecording{Subject: "orig", Cases: []domain.AppCase{
		{Name: "one", Kind: "cli", Argv: []string{"orders", "1"}, Stdout: "order:1\n"},
	}}); err != nil {
		t.Fatal(err)
	}
	return root, orig, casesPath
}

func writeRewrite(t *testing.T, root, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "units", "orders", "rewrite.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lastRound(t *testing.T, root string) RoundCheck {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(root, ".go-anything", "checks", "orders", "round-*.json"))
	if len(files) == 0 {
		t.Fatal("no round record written")
	}
	slices.Sort(files)
	raw, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	var c RoundCheck
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestExpectedPolicyRecordsCrashByExitCode: under the expected policy the crash is recorded with its
// stderr masked and marked unstable, and a rewrite that exits with the same code (but its own message)
// then converges.
func TestExpectedPolicyRecordsCrashByExitCode(t *testing.T) {
	root, orig, casesPath := rsiSetup(t, rsiRewrite(false, false))
	opts := RefineOptions{Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 16, MaxNew: 20, Seed: 1, Policy: usecase.PolicyExpected, Log: io.Discard}
	if ok, err := Refine(opts); ok || err == nil {
		t.Fatalf("a rewrite that does not crash on 007 must disagree: ok=%v err=%v", ok, err)
	}
	rec, err := store.LoadApp(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	var crash *domain.AppCase
	for i, c := range rec.Cases {
		if slices.Equal(c.Argv, []string{"orders", "007"}) {
			crash = &rec.Cases[i]
		}
	}
	if crash == nil {
		t.Fatalf("the crash must be recorded under the expected policy: %+v", rec.Cases)
	}
	if crash.ExitCode != 1 || !slices.Equal(crash.Unstable, []string{"stderr"}) {
		t.Fatalf("crash case must keep exit 1 and mark stderr unstable: %+v", *crash)
	}
	if strings.Contains(crash.Stderr, "/home/alice") || !strings.Contains(crash.Stderr, `File "orders.py", line 4`) ||
		!strings.Contains(crash.Stderr, "ValueError: order 007 cannot be parsed") {
		t.Fatalf("recorded stderr must be masked and keep the exception line: %q", crash.Stderr)
	}

	writeRewrite(t, root, rsiRewrite(true, false))
	var log bytes.Buffer
	opts.Log = &log
	if ok, err := Refine(opts); !ok || err != nil {
		t.Fatalf("same exit code, different message must converge under the expected policy: ok=%v err=%v\n%s", ok, err, log.String())
	}
}

// TestRefineAcceptsRelativeRoot reproduces the relative-root bug: the probe binary was built under
// root/root and then not found.
func TestRefineAcceptsRelativeRoot(t *testing.T) {
	root, orig, casesPath := rsiSetup(t, rsiRewrite(false, false))
	t.Chdir(filepath.Dir(root))
	rel := filepath.Base(root)
	ok, err := Refine(RefineOptions{Root: rel, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: io.Discard})
	if err != nil || !ok {
		t.Fatalf("refine with a relative root must find its probe program: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
		t.Fatal("nothing may be built under root/root")
	}
}

// TestExtraInputsFailConvergenceAndReportDefects: the probes (values "", 0, 1, 2) converge, then the extra
// inputs find the "abc" bug and the original's crash on 007.
func TestExtraInputsFailConvergenceAndReportDefects(t *testing.T) {
	root, orig, casesPath := rsiSetup(t, rsiRewrite(false, true))
	var log bytes.Buffer
	ok, err := Refine(RefineOptions{Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &log, ExtraInputs: []string{"abc", "007", "5"}})
	if ok || err == nil || !strings.Contains(err.Error(), "not converged") || !strings.Contains(err.Error(), "extra input") {
		t.Fatalf("an extra-input disagreement must fail the run with a clear message: ok=%v err=%v", ok, err)
	}
	c := lastRound(t, root)
	if c.Converged || c.ExtraChecked != 3 || c.ExtraRequests != 6 || len(c.ExtraDisagreements) != 2 {
		t.Fatalf("round record: converged=%v extra_checked=%d extra_requests=%d disagreements=%+v",
			c.Converged, c.ExtraChecked, c.ExtraRequests, c.ExtraDisagreements)
	}
	for _, d := range c.ExtraDisagreements {
		if d.Value != "abc" || !strings.Contains(d.Want, "order:abc") || !strings.Contains(d.Got, "order:ABC") {
			t.Fatalf("disagreement must name the value and both answers: %+v", d)
		}
	}
	if !strings.Contains(log.String(), `extra input "abc" disagrees`) {
		t.Fatalf("the final log must list the disagreement:\n%s", log.String())
	}
	raw, err := os.ReadFile(casesPath + ".defects.json")
	if err != nil {
		t.Fatalf("the crash on an extra input must reach the defects file: %v", err)
	}
	if !strings.Contains(string(raw), "007") || strings.Contains(string(raw), "/home/alice") {
		t.Fatalf("defects file must list 007, masked:\n%s", raw)
	}
	page, err := os.ReadFile(filepath.Join(root, ".go-anything", "checks", "orders", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "Not converged: 2 extra input request(s) disagree") || !strings.Contains(string(page), "order:ABC") {
		t.Fatal("the page must show the extra-input verdict and the disagreement")
	}
}

func TestExtraInputsAgreeKeepsConvergence(t *testing.T) {
	root, orig, casesPath := rsiSetup(t, rsiRewrite(false, false))
	ok, err := Refine(RefineOptions{Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: io.Discard, ExtraInputs: []string{"abc", "x y"}})
	if !ok || err != nil {
		t.Fatalf("agreeing extra inputs must keep convergence: ok=%v err=%v", ok, err)
	}
	if c := lastRound(t, root); !c.Converged || c.ExtraChecked != 2 || len(c.ExtraDisagreements) != 0 {
		t.Fatalf("round record: %+v", c)
	}
}

// TestShapeProbesReachBothSides sends the request-shape probes through the rewrite's probe program and
// AskOriginal, and checks that the lower-case method and the raw path arrive unchanged on both.
func TestShapeProbesReachBothSides(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.RequestURI)
		mu.Unlock()
		w.Write([]byte(r.Method + " " + r.RequestURI))
	}))
	defer srv.Close()

	root := newModule(t)
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "units", "orders", "rewrite.go"), []byte(`package orders

import "net/http"

func Handle(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.Method + " " + r.RequestURI)) }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	module, err := ReadModule(root)
	if err != nil {
		t.Fatal(err)
	}
	probes := usecase.RequestShapeProbes("/orders/", []string{"1"}, 1)
	bin, cleanup, err := BuildProbe(root, "orders", "http", module)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RunProbe(bin, "http", probes)
	cleanup()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range probes {
		want := p.Method + " " + p.Path
		if got[i].ResponseBody != want {
			t.Errorf("rewrite saw %q, probe was %q", got[i].ResponseBody, want)
		}
		a, err := AskOriginal("http", "", srv.URL, p)
		if err != nil {
			t.Fatal(err)
		}
		if a.ResponseBody != want {
			t.Errorf("original saw %q, probe was %q", a.ResponseBody, want)
		}
	}
	if !slices.Contains(seen, "get /orders/1") || !slices.Contains(seen, "GET /orders//1") {
		t.Fatalf("lower-case method and doubled slash must reach the original unchanged: %q", seen)
	}
}

// TestScaffoldMarkerMatchesFrontDoor keeps scaffoldMarker in step with the scaffold templates.
func TestScaffoldMarkerMatchesFrontDoor(t *testing.T) {
	for _, cases := range [][]domain.AppCase{
		{{Name: "hello", Kind: "http", Method: "GET", Path: "/hello"}},
		{{Name: "greet", Kind: "cli", Argv: []string{"greet", "bob"}}},
	} {
		repo := t.TempDir()
		res, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{Cases: cases, OriginalCmd: "python3 app.py"})
		if err != nil {
			t.Fatal(err)
		}
		if !ScaffoldedFrontDoor(res.Dir, "main.go") {
			t.Fatalf("%s front door not recognized as scaffolded", res.Kind)
		}
	}
	root := newModule(t)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("// Command app serves.\npackage main\n\n// go-anything frontdoor scaffold wrote it.\nfunc main() {}\n"), 0o644)
	if ScaffoldedFrontDoor(root, "main.go") {
		t.Fatal("the marker counts only in the leading comment block")
	}
	if ScaffoldedFrontDoor(root, "missing.go") {
		t.Fatal("a missing file is not a front door")
	}
}
