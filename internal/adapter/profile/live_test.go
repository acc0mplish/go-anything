package profile

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// fixture is a real CPU profile of a small busy loop (function main.spin), recorded with runtime/pprof.
const fixture = "testdata/cpu.pb.gz"

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// serveProfile answers /debug/pprof/profile with body after a short delay, as a real sampler would.
func serveProfile(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(PprofProfilePath, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchReturnsWholeProfile(t *testing.T) {
	fx := readFixture(t)
	srv := serveProfile(t, fx)
	got, err := FetchCPUProfile(context.Background(), srv.Client(), srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(fx) {
		t.Fatalf("profile bytes differ from the served fixture: got %d bytes, want %d", len(got), len(fx))
	}
}

func TestFetchNotFoundNamesThePprofRequirement(t *testing.T) {
	// A service that never mounted pprof answers 404 on the profile path.
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	_, err := FetchCPUProfile(context.Background(), srv.Client(), srv.URL, 1)
	if err == nil {
		t.Fatal("404 must be an error, not an empty profile")
	}
	for _, want := range []string{"HTTP 404", "net/http/pprof", "mount"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("404 message must contain %q, got: %v", want, err)
		}
	}
}

func TestFetchNonOKQuotesTheServiceReply(t *testing.T) {
	// Go returns 500 when a CPU profile is already running in the process.
	mux := http.NewServeMux()
	mux.HandleFunc(PprofProfilePath, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Could not enable CPU profiling: cpu profiling already in use", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, err := FetchCPUProfile(context.Background(), srv.Client(), srv.URL, 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("want HTTP 500 with the service's reason, got: %v", err)
	}
}

func TestFetchRefusesTruncatedProfile(t *testing.T) {
	// The declared length is longer than what is written, so the client sees the body end early.
	// A partial profile must never be returned as complete.
	fx := readFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc(PprofProfilePath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.Write(fx[:len(fx)/2])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	got, err := FetchCPUProfile(context.Background(), srv.Client(), srv.URL, 1)
	if err == nil {
		t.Fatalf("truncated profile must be an error, got %d bytes", len(got))
	}
	if got != nil {
		t.Fatal("no bytes may be returned with a truncation error")
	}
	if !strings.Contains(err.Error(), "cut short") {
		t.Fatalf("want a cut-short message, got: %v", err)
	}
}

func TestFetchRejectsNonGzipBody(t *testing.T) {
	// A catch-all route answers 200 with HTML. That is not a profile.
	srv := serveProfile(t, []byte("<html>index</html>"))
	_, err := FetchCPUProfile(context.Background(), srv.Client(), srv.URL, 1)
	if err == nil || !strings.Contains(err.Error(), "not a CPU profile") || !strings.Contains(err.Error(), "net/http/pprof") {
		t.Fatalf("want a not-a-profile error that names net/http/pprof, got: %v", err)
	}
}

func TestFetchRefusedConnectionSaysNotReachable(t *testing.T) {
	// Bind a port, then release it, so nothing listens there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	l.Close()
	_, err = FetchCPUProfile(context.Background(), http.DefaultClient, base, 1)
	if err == nil || !strings.Contains(err.Error(), "is not reachable") {
		t.Fatalf("refused connection must say the URL is not reachable, got: %v", err)
	}
}

func TestValidateBase(t *testing.T) {
	cases := []struct {
		base string
		ok   bool
	}{
		{"http://127.0.0.1:8080", true},
		{"https://svc.internal:8443/", true},
		{"ftp://host", false},
		{"127.0.0.1:8080", false},
		{"http://", false},
		{"http://127.0.0.1:8080?x=1", false},
		{"http://127.0.0.1:8080#frag", false},
	}
	for _, c := range cases {
		err := ValidateBase(c.base)
		if (err == nil) != c.ok {
			t.Fatalf("ValidateBase(%q) = %v, want ok=%v", c.base, err, c.ok)
		}
	}
}

func TestValidateBaseNeverEchoesCredentials(t *testing.T) {
	err := ValidateBase("http://user:s3cr3t-token@127.0.0.1:8080")
	if err == nil {
		t.Fatal("credentials in the URL must be refused")
	}
	if strings.Contains(err.Error(), "s3cr3t-token") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("the error must not echo credentials: %v", err)
	}
}

func TestTopReadsProfileWithoutBinary(t *testing.T) {
	// The profile carries its function names, so pprof needs no binary to rank it.
	top, err := Top(fixture, "")
	if err != nil {
		t.Fatal(err)
	}
	hot, err := domain.ParseTop(strings.NewReader(string(top)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hot {
		if h.Name == "main.spin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("main.spin should rank in the fixture, got %+v", hot)
	}
	if !HasSamples(top) {
		t.Fatal("the busy-loop fixture has samples")
	}
}

func TestHasSamplesFalseForIdleProfile(t *testing.T) {
	// The shape pprof prints for a profile with zero samples: a header and no rows.
	idle := []byte("File: svc\nType: cpu\nDuration: 1s, Total samples = 0 \nShowing nodes accounting for 0, 0% of 0 total\n      flat  flat%   sum%        cum   cum%\n")
	if HasSamples(idle) {
		t.Fatal("zero samples must not count as samples")
	}
	if !HasSamples([]byte("Showing nodes accounting for 1.02s, 100% of 1.02s total\n")) {
		t.Fatal("a non-zero total must count as samples")
	}
}
