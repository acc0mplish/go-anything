package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// pythonOriginal is a tiny original service: it answers with Python's json.dumps spacing.
const pythonOriginal = `import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"id": 1, "total": 100, "items": [{"price": 1.5}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
`

// startPythonOriginal starts the Python original on a free local port and returns its base URL.
func startPythonOriginal(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed; the end-to-end check against a Python original cannot run")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	script := filepath.Join(t.TempDir(), "original.py")
	if err := os.WriteFile(script, []byte(pythonOriginal), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, py, script, strconv.Itoa(port))
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(base + "/order")
		if err == nil {
			resp.Body.Close()
			return base
		}
		if time.Now().After(deadline) {
			t.Fatalf("python original did not start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// goRewrite answers the same values as the Python original, with Go's compact JSON spacing.
func goRewrite(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAppReplayJSONBodyAgainstPythonOriginal(t *testing.T) {
	base := startPythonOriginal(t)
	dir := t.TempDir()
	cases := filepath.Join(dir, "cases.json")
	if err := os.WriteFile(cases, []byte(`[{"name":"order","kind":"http","method":"GET","path":"/order"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	recPath := filepath.Join(dir, "app.json")
	var out bytes.Buffer
	if err := run([]string{"app", "record", "-cases", cases, "-o", recPath, "-base", base}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("record: %v\n%s", err, out.String())
	}
	data, _ := os.ReadFile(recPath)
	if !strings.Contains(string(data), `{\"id\": 1, \"total\": 100`) {
		t.Fatalf("recording does not hold Python's spacing:\n%s", data)
	}

	rewrite := goRewrite(t, `{"id":1,"total":100,"items":[{"price":1.5}]}`)

	out.Reset()
	err := run([]string{"app", "replay", recPath, "-base", rewrite.URL}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(out.String(), "order: body differs") {
		t.Fatalf("exact replay must fail on JSON spacing: err=%v\n%s", err, out.String())
	}

	out.Reset()
	report := filepath.Join(dir, "report.md")
	if err := run([]string{"app", "replay", recPath, "-base", rewrite.URL, "-json-body", "-report", report}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("-json-body replay must pass: %v\n%s", err, out.String())
	}
	md, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "Compare: JSON semantic") || !strings.Contains(string(md), "exact decimal value") {
		t.Fatalf("report does not say which comparison was used:\n%s", md)
	}

	// A real difference still fails, and the output names the JSON path instead of both bodies.
	wrong := goRewrite(t, `{"id":1,"total":100,"items":[{"price":1.50001}]}`)
	out.Reset()
	err = run([]string{"app", "replay", recPath, "-base", wrong.URL, "-json-body"}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(out.String(), "$.items[0].price: want 1.5 got 1.50001") {
		t.Fatalf("a changed value must fail with its path: err=%v\n%s", err, out.String())
	}
}

func TestAppReplayCaseFilter(t *testing.T) {
	srv := goRewrite(t, `{"a":1}`)
	recPath := writeCases(t,
		domain.AppCase{Name: "good", Kind: "http", Method: "GET", Path: "/g", Status: 200, ResponseBody: `{"a":1}`},
		domain.AppCase{Name: "bad", Kind: "http", Method: "GET", Path: "/b", Status: 200, ResponseBody: `{"a":2}`},
	)
	var out bytes.Buffer
	if err := run([]string{"app", "replay", recPath, "-base", srv.URL, "-case", "good"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("-case good must replay only that case and pass: %v\n%s", err, out.String())
	}
	if !strings.HasPrefix(out.String(), "1/1 app cases match") {
		t.Fatalf("-case must replay exactly one case:\n%s", out.String())
	}
	out.Reset()
	if err := run([]string{"app", "replay", recPath, "-base", srv.URL, "-case", "bad"}, strings.NewReader(""), &out, io.Discard); err == nil {
		t.Fatalf("-case bad must fail:\n%s", out.String())
	}
	err := run([]string{"app", "replay", recPath, "-base", srv.URL, "-case", "nope"}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `"good", "bad"`) {
		t.Fatalf("unknown -case must name the recorded cases: %v", err)
	}
}

func TestCompareFlagsRejectBadTolerance(t *testing.T) {
	srv := goRewrite(t, `{"a":1}`)
	recPath := writeCases(t, domain.AppCase{Name: "c", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: `{"a":1}`})
	for _, v := range []string{"-1", "NaN", "Inf"} {
		err := run([]string{"app", "replay", recPath, "-base", srv.URL, "-float-tolerance", v}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "float tolerance must be a finite number >= 0") {
			t.Fatalf("app replay -float-tolerance %s: err = %v", v, err)
		}
		err = run([]string{"verify", "rec.json", "-float-tolerance", v, "--", "true"}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "float tolerance must be a finite number >= 0") {
			t.Fatalf("verify -float-tolerance %s: err = %v", v, err)
		}
		err = run([]string{"wire", "verify", ".", "unit", "-cases", recPath, "-float-tolerance", v}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "float tolerance must be a finite number >= 0") {
			t.Fatalf("wire verify -float-tolerance %s: err = %v", v, err)
		}
	}
}

func TestCompareFlagsRejectBadNormalizer(t *testing.T) {
	srv := goRewrite(t, `{"a":1}`)
	recPath := writeCases(t, domain.AppCase{Name: "c", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: `{"a":1}`})
	for spec, want := range map[string]string{
		"epoch": "unknown normalizer",
		"bad=[": "normalizer bad",
	} {
		err := run([]string{"app", "replay", recPath, "-base", srv.URL, "-normalize", spec}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("app replay -normalize %s: err = %v", spec, err)
		}
	}
}
