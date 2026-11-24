package wiregen

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// origStubState is the in-memory state of a stub original. It dies with the process, so a fresh
// start starts from zero — exactly the property -serve-original buys.
var origStubState = struct {
	mu sync.Mutex
	n  int
}{}

// TestOrigStubProcess is not a test: it is the body of the helper process the -serve-original tests
// start through StartOriginal. The parent runs this test binary again with GA_ORIG_STUB=1 and the
// port and pid file in the environment. On every start it appends its pid to the file, so a caller
// can prove that each round really started a fresh copy, then serves: GET /answers the live state
// counter, POST /state increments it, everything else gets a fixed 200.
func TestOrigStubProcess(t *testing.T) {
	if os.Getenv("GA_ORIG_STUB") == "" {
		t.Skip("helper process for the -serve-original tests")
	}
	if f, err := os.OpenFile(os.Getenv("GA_ORIG_STUB_PIDFILE"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%d\n", os.Getpid())
		f.Close()
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// GA_ORIG_STUB_MODE=replay mirrors the stateless chainReplayRewrite answer for answer, so a
		// refine run against it agrees every round and the loop reaches its later rounds; the default
		// mode answers everything alike, so probes disagree and cases pile up.
		if os.Getenv("GA_ORIG_STUB_MODE") == "replay" {
			id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/orders/"), "/")
			switch {
			case r.Method == "POST" && r.URL.Path == "/orders/":
				w.WriteHeader(http.StatusCreated)
				io.WriteString(w, "{\"id\":\"42\",\"title\":\"seed\"}")
			case r.Method == "GET" && id == "1":
				io.WriteString(w, "{\"id\":\"1\",\"title\":\"alpha\"}")
			case r.Method == "PUT" && id == "1":
				io.WriteString(w, "{\"id\":\"1\",\"title\":\"updated\"}")
			case r.Method == "DELETE" && id == "1":
				w.WriteHeader(http.StatusNoContent)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/state":
			origStubState.mu.Lock()
			origStubState.n++
			origStubState.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/state":
			origStubState.mu.Lock()
			n := origStubState.n
			origStubState.mu.Unlock()
			fmt.Fprintf(w, "%d", n)
		default:
			io.WriteString(w, "orig")
		}
	})
	port := os.Getenv("GA_ORIG_STUB_PORT")
	if port == "" {
		port = "0" // no {{port}} placeholder: the original binds whatever it can, not the picked port
	}
	if err := http.ListenAndServe("127.0.0.1:"+port, nil); err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		os.Exit(1)
	}
}

// origStubCommand is the -serve-original command the tests give Refine and StartOriginal: this test
// binary, re-run as the stub, answering on the port the tool picks and announcing its pid per start.
// mode selects the stub's answer behavior (see TestOrigStubProcess).
func origStubCommand(t *testing.T, pidFile, mode string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("starts real original processes")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("GA_ORIG_STUB=1 GA_ORIG_STUB_MODE=%s GA_ORIG_STUB_PORT={{port}} GA_ORIG_STUB_PIDFILE=%s %s -test.run=TestOrigStubProcess$", mode, quoteShell(pidFile), quoteShell(bin))
}

// quoteShell makes a path safe inside a single-quoted sh word.
func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pidLines reads the stub's pid file into the pids that announced themselves there, in start order.
func pidLines(t *testing.T, pidFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stub pid file must exist after the run: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// processGone reports whether a pid no longer exists (Linux /proc).
func processGone(pid string) bool {
	_, err := os.Stat("/proc/" + pid)
	return os.IsNotExist(err)
}

func TestStartOriginalReplacesPortAndAnswersHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real original processes")
	}
	dir := t.TempDir()
	cmd := origStubCommand(t, filepath.Join(dir, "pids"), "")
	srv, err := StartOriginal(cmd, 30*time.Second)
	if err != nil {
		t.Fatalf("the stub original must start: %v", err)
	}
	defer srv.Stop()
	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("the started original must answer HTTP: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("any path must answer: got status %d", resp.StatusCode)
	}
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("the URL must point at the picked port: %q (%v)", srv.URL, err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("the port must be numeric: %q", port)
	}
}

func TestStartOriginalStopFreesThePort(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real original processes")
	}
	dir := t.TempDir()
	srv, err := StartOriginal(origStubCommand(t, filepath.Join(dir, "pids"), ""), 30*time.Second)
	if err != nil {
		t.Fatalf("the stub original must start: %v", err)
	}
	srv.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if conn, err := net.DialTimeout("tcp", strings.TrimPrefix(srv.URL, "http://"), 300*time.Millisecond); err == nil {
			conn.Close()
		} else {
			return // nothing answers the port any more
		}
		if time.Now().After(deadline) {
			t.Fatalf("stop must free the port: %s still answers", srv.URL)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestStartOriginalDyingCommandNamesTheOutputTail(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real original processes")
	}
	// A head full of noise (an operator's config dump) and the real cause at the end: the error must
	// carry the tail and not the head, because the whole environment is inherited and the output can
	// carry secrets.
	cmd := "for i in $(seq 1 40); do echo head-secret-$i-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; done; echo the-actual-cause; exit 3"
	_, err := StartOriginal(cmd, 30*time.Second)
	if err == nil {
		t.Fatal("a command that dies at once must fail the start")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("the error must say the original exited during startup: %v", err)
	}
	if !strings.Contains(err.Error(), "the-actual-cause") {
		t.Fatalf("the error must carry the output tail with the cause: %v", err)
	}
	if strings.Contains(err.Error(), "head-secret-1-") {
		t.Fatalf("the error must not carry the output head: %v", err)
	}
}

func TestStartOriginalWithoutPortPlaceholderTimesOutOnThePickedPort(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real original processes")
	}
	dir := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// No {{port}} anywhere: the command runs as written (the stub binds :0), so the picked port never
	// answers and the start fails after the timeout.
	cmd := fmt.Sprintf("GA_ORIG_STUB=1 GA_ORIG_STUB_PIDFILE=%s %s -test.run=TestOrigStubProcess$", quoteShell(filepath.Join(dir, "pids")), quoteShell(bin))
	_, err = StartOriginal(cmd, 1500*time.Millisecond)
	if err == nil {
		t.Fatal("a start whose original cannot bind the picked port must time out")
	}
	if !strings.Contains(err.Error(), "did not answer HTTP") {
		t.Fatalf("the error must name the readiness timeout: %v", err)
	}
}

func TestServeCommandNeedsPort(t *testing.T) {
	if !serveCommandNeedsPort("go run original.go --port {{port}}") {
		t.Fatal("a command with the placeholder needs the port substitution")
	}
	if serveCommandNeedsPort("go run original.go") {
		t.Fatal("a command without the placeholder must be reported so it can be warned about")
	}
}

// TestRefineServeOriginalStartsFreshPerRound pins the ported contract: with -serve-original the loop
// starts a fresh copy of the original at the top of every round and stops it at the end, so the pid
// file holds one pid per round, all distinct, and none of them survives the run. The replay-mode stub
// answers like the stateless rewrite, so both rounds are clean and the run converges — a convergence
// that a run against one long-lived original would reach the same way, only with shared state.
func TestRefineServeOriginalStartsFreshPerRound(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real original processes and builds the rewrite twice")
	}
	pidFile := filepath.Join(t.TempDir(), "pids")
	orig := origStubCommand(t, pidFile, "replay")
	root := chainModule(t, chainReplayRewrite)
	casesPath := filepath.Join(t.TempDir(), "app.json")
	chainRecording(t, casesPath)
	var log strings.Builder
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		ServeOriginal: orig, OriginalStartTimeout: 30 * time.Second,
		Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &log,
	})
	if err != nil || !ok {
		t.Fatalf("a fresh deterministic original every round must let the run converge: ok=%v err=%v\n%s", ok, err, log.String())
	}
	if !strings.Contains(log.String(), "converged after 2 consecutive clean rounds") {
		t.Fatalf("both rounds must run to reach convergence:\n%s", log.String())
	}
	pids := pidLines(t, pidFile)
	if len(pids) != 2 {
		t.Fatalf("two rounds must start the original twice (one pid per round), got %d: %v\nlog:\n%s", len(pids), pids, log.String())
	}
	if pids[0] == pids[1] {
		t.Fatalf("each round must get its own fresh copy, got the same pid twice: %v", pids)
	}
	for _, pid := range pids {
		if !processGone(pid) {
			t.Fatalf("the round's original must be stopped when the round ends: pid %s survives", pid)
		}
	}
}

func TestRefineServeOriginalRejectsCommandLineUnit(t *testing.T) {
	_, err := Refine(RefineOptions{
		Root: "unused", Unit: "orders", CasesPath: "unused.json", OriginalCLI: "orig",
		ServeOriginal: "go run original.go --port {{port}}",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP units only") {
		t.Fatalf("-serve-original on a command-line unit must be refused with a clear error: %v", err)
	}
}

func TestRefineServeOriginalConflictsWithOriginalURL(t *testing.T) {
	_, err := Refine(RefineOptions{
		Root: "unused", Unit: "orders", Prefix: "/orders/", CasesPath: "unused.json",
		OriginalURL: "http://127.0.0.1:1", ServeOriginal: "go run original.go --port {{port}}",
	})
	if err == nil || !strings.Contains(err.Error(), "give one of -original-url or -serve-original") {
		t.Fatalf("giving both must be refused: %v", err)
	}
}

// TestRefineServeOriginalWarnsAboutMissingPort: the placeholder warning goes to the run log once,
// before the loop, and a command that cannot stay up still fails the run as a tool error.
func TestRefineServeOriginalWarnsAboutMissingPort(t *testing.T) {
	root := chainModule(t, chainRealRewrite)
	casesPath := filepath.Join(t.TempDir(), "app.json")
	chainRecording(t, casesPath)
	var log strings.Builder
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		ServeOriginal: "echo nothing-to-run", OriginalStartTimeout: 30 * time.Second,
		Rounds: 1, Probes: 4, Log: &log,
	})
	if ok || err == nil {
		t.Fatalf("a serve command that dies at once must fail the run: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(log.String(), "no {{port}} placeholder") {
		t.Fatalf("the log must carry the missing-placeholder warning once:\n%s", log.String())
	}
	if strings.Count(log.String(), "no {{port}} placeholder") != 1 {
		t.Fatalf("the warning is about the operator's command, not the round: it must appear once:\n%s", log.String())
	}
}
