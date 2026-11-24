package app

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// countingSleeper records the waits a Runner asked for, so a test checks pacing without measuring time.
type countingSleeper struct {
	waits []time.Duration
}

func (s *countingSleeper) sleep(d time.Duration) { s.waits = append(s.waits, d) }

// flakyListener accepts and immediately closes the first n connections, then answers like a server:
// each accept is one connection the client made, dropped at the transport level.
type flakyListener struct {
	ln       net.Listener
	dropLeft atomic.Int64
	made     atomic.Int64
}

func newFlakyListener(t *testing.T, drop int) *flakyListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &flakyListener{ln: ln}
	f.dropLeft.Store(int64(drop))
	go f.serve()
	return f
}

func (f *flakyListener) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.made.Add(1)
		if f.dropLeft.Add(-1) >= 0 {
			c.Close() // drop before reading: the client sees a transport failure
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 4096)
			for {
				if _, err := c.Read(buf); err != nil {
					return
				}
				// A minimal HTTP/1.1 answer closes after one response; the client sends one request.
				fmt.Fprint(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
				return
			}
		}(c)
	}
}

func (f *flakyListener) url() string { return "http://" + f.ln.Addr().String() }

func (f *flakyListener) count() int64 { return f.made.Load() }

// TestRunnerZeroPacingSendsOnceIsTheOldBehavior: a Runner that sets none of the new fields sends the
// request once, waits for nothing, and answers as before.
func TestRunnerZeroPacingSendsOnceIsTheOldBehavior(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		fmt.Fprint(w, "hi")
	}))
	defer srv.Close()
	sleeper := &countingSleeper{}
	r := Runner{BaseURL: srv.URL}
	r.sleep = sleeper.sleep
	got, err := r.Do(domain.AppCase{Kind: "http", Method: "GET", Path: "/hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseBody != "hi" || got.Status != 200 {
		t.Fatalf("a zero Runner must answer as before: %+v", got)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("a zero Runner must send exactly one request, sent %d", got)
	}
	if len(sleeper.waits) != 0 {
		t.Fatalf("a zero Runner must never wait, waited %v", sleeper.waits)
	}
}

// TestRunnerRetriesDroppedConnections: a server that kills the first two connections is answered on the
// third attempt with Retries 2.
func TestRunnerRetriesDroppedConnections(t *testing.T) {
	f := newFlakyListener(t, 2)
	r := Runner{BaseURL: f.url(), Retries: 2, RetryDelay: time.Millisecond}
	got, err := r.Do(domain.AppCase{Kind: "http", Method: "GET", Path: "/x"})
	if err != nil {
		t.Fatalf("the third attempt must succeed after two dropped connections: %v", err)
	}
	if got.ResponseBody != "ok" {
		t.Fatalf("the answer must come from the working server: %+v", got)
	}
	if got := f.count(); got != 3 {
		t.Fatalf("the server must have been reached 3 times, was %d", got)
	}
}

// TestRunnerZeroOptionsDoesNotRetry: the same dropping server with a zero Runner is one attempt and an
// error, which is the behavior before the option existed.
func TestRunnerZeroOptionsDoesNotRetry(t *testing.T) {
	f := newFlakyListener(t, 2)
	r := Runner{BaseURL: f.url()}
	if _, err := r.Do(domain.AppCase{Kind: "http", Method: "GET", Path: "/x"}); err == nil {
		t.Fatal("a zero Runner must fail on the first dropped connection")
	}
	if got := f.count(); got != 1 {
		t.Fatalf("a zero Runner must send exactly one request, sent %d", got)
	}
}

// TestRunnerRetriesRefusedConnectionThenErrors: a dead address is retried and the last error comes back.
// The injected sleeper proves how many retries ran without measuring time.
func TestRunnerRetriesRefusedConnectionThenErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens: every dial is refused
	sleeper := &countingSleeper{}
	r := Runner{BaseURL: "http://" + addr, Retries: 2, RetryDelay: 20 * time.Millisecond}
	r.sleep = sleeper.sleep
	if _, err := r.Do(domain.AppCase{Kind: "http", Method: "GET", Path: "/x"}); err == nil {
		t.Fatal("a refused connection must end in an error after the retries")
	}
	if len(sleeper.waits) != 2 {
		t.Fatalf("two retries must wait twice between attempts, waited %v", sleeper.waits)
	}
	for _, w := range sleeper.waits {
		if w != 20*time.Millisecond {
			t.Fatalf("the wait between retries must be RetryDelay, waited %v", sleeper.waits)
		}
	}
}

// TestRunnerDelayWaitsBeforeTheRequest: the wait is asked for once, before the first attempt, and a
// status answer is never retried whatever the retry count says.
func TestRunnerDelayAndStatusNeverRetried(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	sleeper := &countingSleeper{}
	r := Runner{BaseURL: srv.URL, Delay: 7 * time.Millisecond, Retries: 3, RetryDelay: time.Millisecond}
	r.sleep = sleeper.sleep
	got, err := r.Do(domain.AppCase{Kind: "http", Method: "GET", Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != 500 {
		t.Fatalf("a 500 is the answer, not a transport failure: %+v", got)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("an HTTP status must never be retried, sent %d", got)
	}
	if len(sleeper.waits) != 1 || sleeper.waits[0] != 7*time.Millisecond {
		t.Fatalf("only the Delay must be waited, once, before the request: %v", sleeper.waits)
	}
}

// TestRunnerRetryableRefuses: a Runner with a Retryable that refuses every error does not retry.
func TestRunnerRetryableRefuses(t *testing.T) {
	f := newFlakyListener(t, 2)
	r := Runner{BaseURL: f.url(), Retries: 5, RetryDelay: time.Millisecond,
		Retryable: func(error) bool { return false }}
	if _, err := r.Do(domain.AppCase{Kind: "http", Method: "GET", Path: "/x"}); err == nil {
		t.Fatal("a refusing Retryable must let the first failure through")
	}
	if got := f.count(); got != 1 {
		t.Fatalf("a refusing Retryable must stop after one attempt, was %d", got)
	}
}

// failingTransport fails the first n round trips with a transport error, then delegates, so a test can
// force a retry on a server that is perfectly healthy.
type failingTransport struct {
	rt    http.RoundTripper
	fails *int
}

func (f *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if *f.fails > 0 {
		*f.fails--
		return nil, fmt.Errorf("connection reset by peer")
	}
	return f.rt.RoundTrip(req)
}

// TestRunnerRetryRebuildsTheBody: a POST retried after a transport failure must carry its body again on
// the attempt that succeeds (a sent request's body reader is consumed).
func TestRunnerRetryRebuildsTheBody(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	fails := 1
	r := Runner{BaseURL: srv.URL, Retries: 1, RetryDelay: time.Millisecond,
		Client: &http.Client{Transport: &failingTransport{rt: http.DefaultTransport, fails: &fails}}}
	got, err := r.Do(domain.AppCase{Kind: "http", Method: "POST", Path: "/x", Body: `{"a":1}`})
	if err != nil {
		t.Fatalf("the retried POST must succeed: %v", err)
	}
	if got.ResponseBody != "ok" {
		t.Fatalf("unexpected answer: %+v", got)
	}
	if seen != `{"a":1}` {
		t.Fatalf("the retried POST must carry the body again, the server saw %q", seen)
	}
}
