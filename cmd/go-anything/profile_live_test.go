package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// writeCases saves an app recording with the given HTTP cases and returns its path.
func writeCases(t *testing.T, cases ...domain.AppCase) string {
	t.Helper()
	data, err := json.Marshal(domain.AppRecording{Subject: "svc", Cases: cases})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// homeCase is the recorded request every live test replays. The server must answer it with 200 "ok".
var homeCase = domain.AppCase{Name: "home", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: "ok"}

// profileServer serves "/" and, when profile is non-nil, /debug/pprof/profile with that body.
func profileServer(t *testing.T, profile []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r) // only the routes the service really has answer; the rest are 404
			return
		}
		w.Write([]byte("ok"))
	})
	if profile != nil {
		mux.HandleFunc("/debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) {
			// A real sampler takes the requested number of seconds; a short wait keeps the test fast.
			time.Sleep(300 * time.Millisecond)
			w.Write(profile)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLiveProfileRanksServedProfile(t *testing.T) {
	fx, err := os.ReadFile("testdata/cpu.pb.gz")
	if err != nil {
		t.Fatal(err)
	}
	srv := profileServer(t, fx)
	var out bytes.Buffer
	err = cmdProfileLive(liveOptions{URL: srv.URL, Cases: writeCases(t, homeCase), Seconds: 1}, &out)
	if err != nil {
		t.Fatalf("live profile failed: %v", err)
	}
	for _, want := range []string{"profiled " + srv.URL, "main.spin", "hot functions in live service"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output must contain %q, got:\n%s", want, out.String())
		}
	}
}

func TestLiveProfileWithoutPprofFailsExplicitly(t *testing.T) {
	// The service serves its routes but never mounted net/http/pprof, so the profile path is a 404.
	srv := profileServer(t, nil)
	var out bytes.Buffer
	err := cmdProfileLive(liveOptions{URL: srv.URL, Cases: writeCases(t, homeCase), Seconds: 1}, &out)
	if err == nil {
		t.Fatal("a service without pprof must fail, not print a profile")
	}
	for _, want := range []string{"HTTP 404", "net/http/pprof"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("404 error must contain %q, got: %v", want, err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("nothing may be printed on failure, got %q", out.String())
	}
}

func TestLiveProfileIdleServiceIsAnError(t *testing.T) {
	// A profile with zero samples must not be reported as "nothing is hot".
	empty, err := os.ReadFile("testdata/empty.pb.gz")
	if err != nil {
		t.Fatal(err)
	}
	srv := profileServer(t, empty)
	var out bytes.Buffer
	err = cmdProfileLive(liveOptions{URL: srv.URL, Cases: writeCases(t, homeCase), Seconds: 1}, &out)
	if err == nil || !strings.Contains(err.Error(), "no CPU samples") {
		t.Fatalf("zero samples must be an explicit error, got: %v", err)
	}
	if strings.Contains(out.String(), "nothing worth porting") {
		t.Fatal("an idle profile must not claim nothing is worth porting")
	}
}

func TestLiveProfileConnectionRefusedSaysNotReachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	l.Close()
	var out bytes.Buffer
	err = cmdProfileLive(liveOptions{URL: base, Cases: writeCases(t, homeCase), Seconds: 1}, &out)
	if err == nil || !strings.Contains(err.Error(), "is not reachable") {
		t.Fatalf("connection refused must say the URL is not reachable, got: %v", err)
	}
}

func TestLiveProfileRejectsCLICases(t *testing.T) {
	cli := domain.AppCase{Name: "run", Kind: "cli", Argv: []string{"--version"}, ExitCode: 0}
	var out bytes.Buffer
	err := cmdProfileLive(liveOptions{URL: "http://127.0.0.1:1", Cases: writeCases(t, cli), Seconds: 1}, &out)
	if err == nil || !strings.Contains(err.Error(), "HTTP cases only") {
		t.Fatalf("a cli case must be refused in live mode, got: %v", err)
	}
}

func TestLiveProfileRequiresCasesAndValidSeconds(t *testing.T) {
	var out bytes.Buffer
	if err := cmdProfileLive(liveOptions{URL: "http://127.0.0.1:1", Seconds: 1}, &out); err == nil || !strings.Contains(err.Error(), "-cases") {
		t.Fatalf("missing -cases must be explicit, got: %v", err)
	}
	if err := cmdProfileLive(liveOptions{URL: "http://127.0.0.1:1", Cases: writeCases(t, homeCase), Seconds: 0}, &out); err == nil || !strings.Contains(err.Error(), "-seconds") {
		t.Fatalf("seconds 0 must be refused, got: %v", err)
	}
}

func TestLiveProfileRefusesCredentialsInURL(t *testing.T) {
	var out bytes.Buffer
	err := cmdProfileLive(liveOptions{URL: "http://user:tok3n@127.0.0.1:1", Cases: writeCases(t, homeCase), Seconds: 1}, &out)
	if err == nil || strings.Contains(err.Error(), "tok3n") {
		t.Fatalf("credentials in -url must be refused without echo, got: %v", err)
	}
}

func TestReplayTallyCountsDifferencesWithoutFailing(t *testing.T) {
	var tally replayTally
	tally.add(usecase.AppVerdict{Total: 1, Matched: 1})
	tally.add(usecase.AppVerdict{Total: 1, Matched: 0, Mismatches: []usecase.AppMismatch{{Case: "home", What: "status"}}})
	if tally.requests != 2 || tally.differed != 1 {
		t.Fatalf("want 2 requests and 1 differing, got %+v", tally)
	}
	if len(tally.samples) != 1 || !strings.Contains(tally.samples[0], "home: status differs") {
		t.Fatalf("the difference must be named, got %v", tally.samples)
	}
}

// burnWork is the busy loop the real-service test drives. It shows up by name in the profile.
func burnWork(d time.Duration) int {
	end := time.Now().Add(d)
	n := 0
	for time.Now().Before(end) {
		for i := 0; i < 100000; i++ {
			n += i * i
		}
	}
	return n
}

// TestLiveProfileAgainstRealPprofService starts a real Go server that mounts net/http/pprof, replays a busy
// endpoint against it for one second, and expects the busy function to rank in the profile.
func TestLiveProfileAgainstRealPprofService(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real pprof service and samples for one second")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		burnWork(40 * time.Millisecond)
		w.Write([]byte("done"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cases := writeCases(t, domain.AppCase{Name: "work", Kind: "http", Method: "GET", Path: "/work", Status: 200, ResponseBody: "done"})
	var out bytes.Buffer
	start := time.Now()
	if err := cmdProfileLive(liveOptions{URL: srv.URL, Cases: cases, Seconds: 1}, &out); err != nil {
		t.Fatalf("live profile against a real pprof service failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 60*time.Second {
		t.Fatalf("live profile took %s, want under 60s", elapsed)
	}
	if !strings.Contains(out.String(), "burnWork") {
		t.Fatalf("the busy function must rank in the live profile, got:\n%s", out.String())
	}
}
