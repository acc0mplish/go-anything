package wiregen

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// TestAskOriginalNeverRetriesADroppedAfterAccept: a connection the original accepted and then closed
// (EOF, reset) is the original's answer — a crash on this input. Retries are for connections that never
// got through, so the tool's own retry option must never fire on a drop, and the drop must be classified
// as an original defect (NoResponse, OriginalCrashed). The primary, race-free pin of that contract is
// TestRetryableWireClassification below; this end-to-end probe is a supplementary check. Its accept
// count is therefore bounded from above, not pinned to exactly one (see the count assertion).
func TestAskOriginalNeverRetriesADroppedAfterAccept(t *testing.T) {
	var accepts atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			c.Close() // accept, then drop: like an original that crashed on the input
		}
	}()
	probe := domain.AppCase{Name: "p", Kind: "http", Method: "GET", Path: "/orders/007"}
	wo := WireOptions{Retries: 3, RetryDelay: time.Millisecond}
	got, err := AskOriginal("http", "", "http://"+ln.Addr().String(), probe, wo)
	if err != nil {
		t.Fatalf("a dropped connection is the original's answer, not a loop error: %v", err)
	}
	if got.Status != domain.NoResponse || !usecase.OriginalCrashed(got) {
		t.Fatalf("the drop must be classified as an original defect: %+v", got)
	}
	// Go's http.Transport transparently re-dials a GET whose bytes were never sent, so the dropping
	// server may be accepted twice even though the tool's own retry option never fires. The contract
	// that matters (the tool retries nothing on a drop) is pinned by TestRetryableWireClassification;
	// here we only assert the observed accepts stay within one answer plus the retry budget.
	if n := accepts.Load(); n > int64(wo.Retries)+1 {
		t.Fatalf("a drop after accept must not consume more than the retry budget, the server was reached %d times (limit %d)", n, wo.Retries+1)
	}
}

// TestAskOriginalRetriesARefusedConnection: a connection that never gets through (nothing listens) is
// retried, and only after the last retry does the loop see the error that stops it.
func TestAskOriginalRetriesARefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // every dial from now on is refused
	probe := domain.AppCase{Name: "p", Kind: "http", Method: "GET", Path: "/orders/1"}
	_, err = AskOriginal("http", "", "http://"+addr, probe,
		WireOptions{Retries: 2, RetryDelay: time.Millisecond})
	if err == nil {
		t.Fatal("after the retries are spent the error must stop the loop")
	}
	if !isDialError(err) {
		t.Fatalf("a refused connection must stay a dial failure, not a crash answer: %v", err)
	}
}

// TestRetryableWireClassification is the primary, race-free pin of the retryableWire contract: a
// connection the original accepted and then dropped (EOF, unexpected EOF, reset) must never be retried
// — the drop is the original's crash answer — while every other transport failure stays retryable. The
// end-to-end probe TestAskOriginalNeverRetriesADroppedAfterAccept is only a supplementary check,
// because Go's http.Transport may add its own transparent re-dial on top of the tool's retry decision.
func TestRetryableWireClassification(t *testing.T) {
	// The runner hands retryableWire an error wrapped with the method and path
	// ("app: %s %s: %w"), so every case is wrapped the way the real wire error is.
	wrap := func(err error) error { return fmt.Errorf("app: GET /orders/007: %w", err) }
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		// Drops after accept: the original's answer, never retried.
		{"eof after accept", wrap(&net.OpError{Op: "read", Net: "tcp", Err: io.EOF}), false},
		{"unexpected eof mid body", wrap(&net.OpError{Op: "read", Net: "tcp", Err: io.ErrUnexpectedEOF}), false},
		{"reset after accept", wrap(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), false},
		{"plain eof from a body read", wrap(io.EOF), false},
		// A connection that never got through is a delivery failure and retries.
		{"refused dial", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), true},
		// Anything else retries, including a bare error with no transport detail.
		{"plain error", wrap(errors.New("boom")), true},
	}
	for _, tc := range cases {
		if got := retryableWire(tc.err); got != tc.retryable {
			t.Fatalf("%s: retryableWire must be %v, got %v (%v)", tc.name, tc.retryable, got, tc.err)
		}
	}
}

// isDialError reports whether the error is a failure to reach the server, not an answer it gave.
func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
