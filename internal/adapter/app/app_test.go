package app

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hi"))
	})
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		w.WriteHeader(http.StatusCreated)
		w.Write(buf[:n])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPCaseCapturesStatusAndBody(t *testing.T) {
	srv := fakeServer(t)
	r := Runner{BaseURL: srv.URL}
	got, err := r.Do(domain.AppCase{Name: "h", Kind: "http", Method: "GET", Path: "/hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != 200 || got.ResponseBody != "hi" {
		t.Fatalf("got %+v", got)
	}
}

func TestHTTPCasePassesBodyAndStatus(t *testing.T) {
	srv := fakeServer(t)
	r := Runner{BaseURL: srv.URL}
	got, err := r.Do(domain.AppCase{Name: "e", Kind: "http", Method: "POST", Path: "/echo", Body: "data"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != http.StatusCreated || got.ResponseBody != "data" {
		t.Fatalf("got %+v", got)
	}
}

func TestHTTPUnknownRouteIs404NotError(t *testing.T) {
	srv := fakeServer(t)
	got, err := (Runner{BaseURL: srv.URL}).Do(domain.AppCase{Name: "x", Kind: "http", Method: "GET", Path: "/nope"})
	if err != nil || got.Status != 404 {
		t.Fatalf("a 404 is an answer, not a failure: %+v err=%v", got, err)
	}
}

func TestHTTPRequiresBaseURL(t *testing.T) {
	if _, err := (Runner{}).Do(domain.AppCase{Name: "x", Kind: "http", Method: "GET", Path: "/"}); err == nil {
		t.Fatal("missing base URL must be an error")
	}
}

func TestHTTPUnreachableIsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // now nothing listens there
	_, err := (Runner{BaseURL: url, Client: &http.Client{Timeout: 2 * time.Second}}).Do(domain.AppCase{Name: "x", Kind: "http", Method: "GET", Path: "/"})
	if err == nil {
		t.Fatal("an unreachable server must be an error, not a fake answer")
	}
}

func TestCLICaptureExitCodeAndStreams(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	r := Runner{Argv0: sh}
	got, err := r.Do(domain.AppCase{Name: "c", Kind: "cli", Argv: []string{"-c", "echo out; echo err >&2; exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 3 || got.Stdout != "out\n" || got.Stderr != "err\n" {
		t.Fatalf("got %+v", got)
	}
}

func TestCLIStdinReachesProgram(t *testing.T) {
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat")
	}
	got, err := (Runner{Argv0: cat}).Do(domain.AppCase{Name: "s", Kind: "cli", Argv: []string{}, Stdin: "piped"})
	if err != nil || got.Stdout != "piped" {
		t.Fatalf("stdin not delivered: %+v err=%v", got, err)
	}
}

func TestCLITimeoutIsError(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	_, err = (Runner{Argv0: sleep, Timeout: 100 * time.Millisecond}).Do(domain.AppCase{Name: "t", Kind: "cli", Argv: []string{"5"}})
	if err == nil {
		t.Fatal("a timed-out run must be an error")
	}
}

func TestCLIMissingProgramIsError(t *testing.T) {
	_, err := (Runner{Argv0: "/definitely/not/a/program"}).Do(domain.AppCase{Name: "m", Kind: "cli", Argv: []string{}})
	if err == nil {
		t.Fatal("a missing program must be an error, not a fake exit code")
	}
}
