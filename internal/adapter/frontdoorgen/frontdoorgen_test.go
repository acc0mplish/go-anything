package frontdoorgen_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/adapter/frontdoorgen"
	"github.com/go-anything/go-anything/internal/adapter/wiregen"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

func TestSplitWords(t *testing.T) {
	ok := map[string][]string{
		"python app.py":                {"python", "app.py"},
		"  python3   -u  app.py ":      {"python3", "-u", "app.py"},
		`node 'my app.js'`:             {"node", "my app.js"},
		`ruby "a b" c`:                 {"ruby", "a b", "c"},
		`echo "say \"hi\" \$x"`:        {"echo", `say "hi" $x`},
		`a\ b c`:                       {"a b", "c"},
		`x 'it''s'`:                    {"x", "its"},
		`tool '' end`:                  {"tool", "", "end"},
		`tool 'a|b' "c;d" '$HOME' x~y`: {"tool", "a|b", "c;d", "$HOME", "x~y"},
	}
	for in, want := range ok {
		got, err := frontdoorgen.SplitWords(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("SplitWords(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "   ", `python 'app.py`, `python "app.py`, `tool \`, "a | b", "a > out", "a; b", "a && b",
		"echo $HOME", "echo `id`", `echo "$HOME"`, "~/bin/tool", "a\x00b"}
	for _, in := range bad {
		if got, err := frontdoorgen.SplitWords(in); err == nil || !errors.Is(err, frontdoorgen.ErrCommand) {
			t.Errorf("SplitWords(%q) = %q, %v; want an ErrCommand error", in, got, err)
		}
	}
}

func httpCases() []domain.AppCase {
	return []domain.AppCase{
		{Name: "hello", Kind: "http", Method: "GET", Path: "/hello"},
		{Name: "item", Kind: "http", Method: "GET", Path: "/items/7"},
		{Name: "echo", Kind: "http", Method: "POST", Path: "/echo", Body: `{"a":1}`},
	}
}

func cliCases() []domain.AppCase {
	return []domain.AppCase{
		{Name: "greet", Kind: "cli", Argv: []string{"greet", "bob", "two words"}},
		{Name: "upper", Kind: "cli", Argv: []string{"upper"}, Stdin: "abc\n"},
		{Name: "fail", Kind: "cli", Argv: []string{"fail"}},
	}
}

func TestMixedKindsRejectedWithCounts(t *testing.T) {
	repo := t.TempDir()
	cases := append(httpCases(), cliCases()[:2]...)
	_, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{Cases: cases, OriginalCmd: "python3 app.py"})
	if !errors.Is(err, frontdoorgen.ErrMixedKinds) || !strings.Contains(err.Error(), "3 http and 2 cli") {
		t.Fatalf("want a mixed-kinds error naming 3 http and 2 cli, got %v", err)
	}
	assertNoFrontDoor(t, repo)
}

func TestUnbalancedQuoteRejected(t *testing.T) {
	repo := t.TempDir()
	_, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{Cases: httpCases(), OriginalCmd: `python3 'app.py`})
	if !errors.Is(err, frontdoorgen.ErrCommand) || !strings.Contains(err.Error(), "unbalanced single quote") {
		t.Fatalf("want an unbalanced quote error, got %v", err)
	}
	assertNoFrontDoor(t, repo)
}

func TestBadOptionsWriteNothing(t *testing.T) {
	for name, opts := range map[string]frontdoorgen.Options{
		"port env": {PortEnv: "MY-PORT"},
		"listen":   {Listen: "127.0.0.1"},
		"port":     {Listen: "127.0.0.1:99999"},
		"module":   {Module: "a/../b"},
	} {
		repo := t.TempDir()
		opts.Cases, opts.OriginalCmd = httpCases(), "python3 app.py"
		if _, err := frontdoorgen.Scaffold(repo, opts); err == nil {
			t.Errorf("%s: want an error", name)
		}
		assertNoFrontDoor(t, repo)
	}
}

func assertNoFrontDoor(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(frontdoorgen.Dir(repo)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nothing may be written on error, but %s exists (%v)", frontdoorgen.Dir(repo), err)
	}
}

func TestRefusesToOverwrite(t *testing.T) {
	repo := t.TempDir()
	dir := frontdoorgen.Dir(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dir, "main.go")
	if err := os.WriteFile(sentinel, []byte("package main // mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{Cases: httpCases(), OriginalCmd: "python3 app.py"})
	if !errors.Is(err, frontdoorgen.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if b, _ := os.ReadFile(sentinel); string(b) != "package main // mine\n" {
		t.Fatalf("the existing file was changed: %q", b)
	}

	// A second scaffold over a fresh one refuses too.
	repo2 := t.TempDir()
	opts := frontdoorgen.Options{Cases: cliCases(), OriginalCmd: "python3 tool.py"}
	if _, err := frontdoorgen.Scaffold(repo2, opts); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(frontdoorgen.Dir(repo2), "main.go"))
	if _, err := frontdoorgen.Scaffold(repo2, opts); !errors.Is(err, frontdoorgen.ErrExists) {
		t.Fatalf("second scaffold: want ErrExists, got %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(frontdoorgen.Dir(repo2), "main.go"))
	if string(before) != string(after) {
		t.Fatal("the second scaffold changed main.go")
	}
}

// The generated main.go must offer wire gen exactly what it looks for.
func TestGeneratedMainHasWireGenAnchors(t *testing.T) {
	repo := t.TempDir()
	res, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{Cases: httpCases(), OriginalCmd: "python3 app.py", Listen: "127.0.0.1:9123"})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(res.Dir, "main.go"))
	if !strings.Contains(string(src), "serve := func() error { return http.ListenAndServe(\"127.0.0.1:9123\", mux) }") {
		t.Fatalf("main.go lacks the http.ListenAndServe(literal, mux) anchor:\n%s", src)
	}
	addr, err := wiregen.ListenAddress(res.Dir, "main.go")
	if err != nil || addr != "127.0.0.1:9123" {
		t.Fatalf("wire verify must read the listen address: %q, %v", addr, err)
	}
	if mod, err := wiregen.ReadModule(res.Dir); err != nil || mod != frontdoorgen.DefaultModule {
		t.Fatalf("module: %q, %v", mod, err)
	}
}

func needProcesses(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts processes and runs go build")
	}
	for _, tool := range []string{"python3", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

const serverScript = `import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def send(self, code, body, ctype):
        b = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def do_GET(self):
        if self.path == "/hello":
            self.send(200, "hello", "text/plain")
        elif self.path.startswith("/items/"):
            self.send(200, json.dumps({"id": self.path[len("/items/"):]}), "application/json")
        else:
            self.send(404, "not found", "text/plain")
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        self.send(201, "got " + self.rfile.read(n).decode(), "application/json")
    def log_message(self, *a):
        pass

HTTPServer(("127.0.0.1", int(os.environ["APP_PORT"])), H).serve_forever()
`

// recordServer records the cases against the original run directly, as app record would.
func recordServer(t *testing.T, script string) domain.AppRecording {
	t.Helper()
	port := freePort(t)
	cmd := exec.Command("python3", script)
	cmd.Env = append(os.Environ(), "APP_PORT="+strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the original did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	rec, err := usecase.AppRecord("app", httpCases(), app.Runner{BaseURL: "http://" + addr})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func scaffoldServer(t *testing.T) (frontdoorgen.Result, domain.AppRecording) {
	t.Helper()
	repo := t.TempDir()
	script := filepath.Join(repo, "app.py")
	if err := os.WriteFile(script, []byte(serverScript), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := recordServer(t, script)
	res, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{
		Cases:       rec.Cases,
		OriginalCmd: "python3 '" + script + "'",
		PortEnv:     "APP_PORT",
		Listen:      "127.0.0.1:" + strconv.Itoa(freePort(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res, rec
}

func check(t *testing.T, res frontdoorgen.Result, rec domain.AppRecording) frontdoorgen.Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	rep, err := frontdoorgen.Check(ctx, res, rec)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, m := range rep.Mismatches {
		t.Errorf("%s: %s want %q got %q", m.Case, m.What, m.Want, m.Got)
	}
	t.Logf("check: kind %s, %d/%d passed, %d fallback(s), listen %s, original %s, ports closed %v",
		rep.Kind, rep.Passed, rep.Total, rep.Fallbacks, rep.ListenAddr, rep.OriginalAddr, rep.PortsClosed)
	return rep
}

func TestServerScaffoldCheckPassesAtFullFallback(t *testing.T) {
	needProcesses(t)
	res, rec := scaffoldServer(t)
	rep := check(t, res, rec)
	if rep.Passed != 3 || rep.Total != 3 || rep.Fallbacks != 3 || !rep.PortsClosed {
		t.Fatalf("want 3/3 at 3 fallbacks with both ports closed, got %+v", rep)
	}
}

// After the scaffold, the real wire gen code path mounts an HTTP unit; the module builds, and the unit,
// which serves the original by default, answers its case instead of the fallback.
func TestWireGenMountsIntoServerScaffold(t *testing.T) {
	needProcesses(t)
	res, rec := scaffoldServer(t)
	if _, err := wiregen.Gen(res.Dir, "items", "/items/"); err != nil {
		t.Fatal(err)
	}
	changed, err := wiregen.WireMain(res.Dir, "main.go", res.Module, "http")
	if err != nil || !changed {
		t.Fatalf("wire gen must mount into the scaffold without edits: changed=%v err=%v", changed, err)
	}
	src, _ := os.ReadFile(filepath.Join(res.Dir, "main.go"))
	if !strings.Contains(string(src), wiregen.MountMarker+"\n\tif err := units.Mount(mux, os.Getenv(\"GA_ORIGINAL_URL\")); err != nil {\n\t\tlog.Fatal(err)\n\t}\n\tserve := func()") {
		t.Fatalf("the mount is not directly above the serve line:\n%s", src)
	}
	rep := check(t, res, rec)
	if rep.Passed != 3 || rep.Fallbacks != 2 || !rep.PortsClosed {
		t.Fatalf("want 3/3 with 2 fallbacks (the items unit answers one) and ports closed, got %+v", rep)
	}
}

const cliScript = `#!/usr/bin/env python3
import os, sys
args = sys.argv[1:]
cmd = args[0] if args else ""
if cmd == "greet":
    leaked = [k for k in ("GA_FRONTDOOR_DIRECT", "GA_ORIGINAL_CLI") if k in os.environ]
    print("hello " + "|".join(args[1:]) + " leaked=" + ",".join(leaked))
elif cmd == "upper":
    sys.stdout.write(sys.stdin.read().upper())
elif cmd == "fail":
    sys.stderr.write("boom\n")
    sys.exit(3)
else:
    sys.exit(2)
`

func scaffoldCLI(t *testing.T) (frontdoorgen.Result, domain.AppRecording) {
	t.Helper()
	repo := t.TempDir()
	script := filepath.Join(repo, "tool.py")
	if err := os.WriteFile(script, []byte(cliScript), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := usecase.AppRecord("tool", cliCases(), app.Runner{Argv0: script})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Cases[2].ExitCode != 3 || rec.Cases[1].Stdout != "ABC\n" || rec.Cases[0].Stdout != "hello bob|two words leaked=\n" {
		t.Fatalf("the original did not answer as expected: %+v", rec.Cases)
	}
	res, err := frontdoorgen.Scaffold(repo, frontdoorgen.Options{Cases: rec.Cases, OriginalCmd: "python3 '" + script + "'"})
	if err != nil {
		t.Fatal(err)
	}
	return res, rec
}

func TestCLIScaffoldCheckPassesAtFullFallback(t *testing.T) {
	needProcesses(t)
	res, rec := scaffoldCLI(t)
	rep := check(t, res, rec)
	if rep.Passed != 3 || rep.Total != 3 || rep.Fallbacks != 3 {
		t.Fatalf("want 3/3 at 3 fallbacks, got %+v", rep)
	}
}

// The real wire gen code path routes a command-line unit into the scaffold. Without any setup the unit
// still reaches the original (with its fixed leading arguments and an unchanged environment).
func TestWireGenRoutesIntoCLIScaffold(t *testing.T) {
	needProcesses(t)
	res, rec := scaffoldCLI(t)
	if _, err := wiregen.Gen(res.Dir, "greet", ""); err != nil {
		t.Fatal(err)
	}
	changed, err := wiregen.WireMain(res.Dir, "main.go", res.Module, "cli")
	if err != nil || !changed {
		t.Fatalf("wire gen must route into the scaffold without edits: changed=%v err=%v", changed, err)
	}
	src, _ := os.ReadFile(filepath.Join(res.Dir, "main.go"))
	if !strings.Contains(string(src), "func main() {\n\t"+wiregen.CLIMarker+"\n\tif code, handled := units.RunCLI(") {
		t.Fatalf("RunCLI is not the first statement of main:\n%s", src)
	}
	rep := check(t, res, rec)
	if rep.Passed != 3 || rep.Fallbacks != 2 {
		t.Fatalf("want 3/3 with 2 fallbacks (the greet unit answers one), got %+v", rep)
	}
}
