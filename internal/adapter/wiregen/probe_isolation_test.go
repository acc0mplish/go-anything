package wiregen

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// fakeHarness is a stand-in for the generated probe binary: it answers each probe it is handed. A
// probe naming "boom" makes it exit the child the way a unit that calls os.Exit would, and "hang"
// makes it sleep far past any timeout. One child is started per probe, so the exits and hangs below
// can only be survived if the isolation works.
const fakeHarness = `#!/usr/bin/env python3
import json, sys, time
probes = json.load(sys.stdin)
out = []
for p in probes:
    a = p.get("argv") or [""]
    if "boom" in a:
        sys.stderr.write("unit exited the child\n")
        sys.exit(7)
    if "hang" in a:
        time.sleep(30)
    out.append({"exit_code": 0, "stdout": "ok:" + a[-1], "stderr": ""})
json.dump(out, sys.stdout)
`

func writeFakeHarness(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "harness.py")
	if err := os.WriteFile(p, []byte(fakeHarness), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunProbeCLIIolationKeepsAUnitExitInTheChild(t *testing.T) {
	bin := writeFakeHarness(t)
	probes := []domain.AppCase{
		{Kind: "cli", Argv: []string{"orders", "fine"}},
		{Kind: "cli", Argv: []string{"orders", "boom"}},
	}
	got, err := RunProbe(bin, "cli", probes)
	if err != nil {
		t.Fatalf("a unit that exits its child must not fail the probe run: %v", err)
	}
	if got[0].Stdout != "ok:fine" || got[0].ExitCode != 0 {
		t.Fatalf("the normal probe must still be answered: %+v", got[0])
	}
	if got[1].ExitCode != 7 || !strings.Contains(got[1].Stderr, "unit exited the child") {
		t.Fatalf("the exit must be recorded as that probe's answer: %+v", got[1])
	}
}

func TestRunProbeCLITimeoutRecordsExit124(t *testing.T) {
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	defer func() { probeTimeout = old }()
	bin := writeFakeHarness(t)
	got, err := RunProbe(bin, "cli", []domain.AppCase{{Kind: "cli", Argv: []string{"orders", "hang"}}})
	if err != nil {
		t.Fatalf("a hanging unit must time out, not stall the run: %v", err)
	}
	if got[0].ExitCode != 124 || !strings.Contains(got[0].Stderr, "timed out") {
		t.Fatalf("a hung probe must carry exit 124 and say so: %+v", got[0])
	}
}

// TestAskOriginalSendsBodyAndHeaders pins the wire symmetry (issue #16): the original side receives
// the same bytes the rewrite side would. A probe carrying a body and request headers must deliver
// them, and a JSON body without a declared type gets the same default Content-Type app.Runner adds.
func TestAskOriginalSendsBodyAndHeaders(t *testing.T) {
	var seen struct {
		method, path, body, ctype, token string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.method, seen.path, seen.body = r.Method, r.URL.Path, string(b)
		seen.ctype, seen.token = r.Header.Get("Content-Type"), r.Header.Get("X-Probe-Token")
		w.WriteHeader(204)
	}))
	defer srv.Close()
	probe := domain.AppCase{Kind: "http", Method: "POST", Path: "/orders/7", Body: `{"q": 1}`,
		RequestHeaders: map[string]string{"X-Probe-Token": "shape-variant"}}
	got, err := AskOriginal("http", "", srv.URL, probe, WireOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if seen.method != "POST" || seen.path != "/orders/7" || seen.body != `{"q": 1}` {
		t.Fatalf("the original must receive the probe's method, path and body: %+v", seen)
	}
	if seen.token != "shape-variant" {
		t.Fatalf("the original must receive the probe's request headers: %+v", seen)
	}
	if seen.ctype != "application/json" {
		t.Fatalf("an unlabeled JSON body gets the same default Content-Type the rewrite side gets: %+v", seen)
	}
	if got.Status != 204 {
		t.Fatalf("the answer must come back: %+v", got)
	}
}

// fakeExitHarness answers with the three shapes issue #19 named: a clean exit with no output, a
// nonzero exit after writing output, and a clean exit whose output is not the answer JSON.
const fakeExitHarness = `#!/usr/bin/env python3
import json, sys
probes = json.load(sys.stdin)
a = (probes[0].get("argv") or [""])[-1]
if a == "clean0":
    sys.exit(0)
if a == "half5":
    sys.stdout.write("partial answer")
    sys.stderr.write("then it died")
    sys.exit(5)
sys.stdout.write("not json at all")
`

func TestRunProbeCLIRecordsEveryExitShape(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "exit-harness.py")
	if err := os.WriteFile(bin, []byte(fakeExitHarness), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		arg    string
		code   int
		stdout string
		stderr string
	}{
		{"clean0", 0, "", ""},
		{"half5", 5, "partial answer", "then it died"},
		{"raw", 0, "not json at all", ""},
	} {
		got, err := RunProbe(bin, "cli", []domain.AppCase{{Kind: "cli", Argv: []string{"orders", tc.arg}}})
		if err != nil {
			t.Fatalf("%s: the exit shape is the probe's answer, not a run failure: %v", tc.arg, err)
		}
		a := got[0]
		if a.ExitCode != tc.code || a.Stdout != tc.stdout || a.Stderr != tc.stderr {
			t.Fatalf("%s: want exit %d stdout %q stderr %q, got %+v", tc.arg, tc.code, tc.stdout, tc.stderr, a)
		}
	}
}
