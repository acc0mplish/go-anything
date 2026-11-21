// Package capture is a loopback HTTP receiver that records the outbound calls a program makes
// (webhooks). Point the program's webhook URL at Receiver.URL during a recording or replay.
//
// It binds to 127.0.0.1 only: a receiver reachable from other machines would accept real traffic.
package capture

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/go-anything/go-anything/internal/domain"
)

// maxBody caps how much of one call is kept, so a runaway program cannot exhaust memory.
const maxBody = 1 << 20

// Receiver collects calls made to it until they are drained.
type Receiver struct {
	URL string

	mu      sync.Mutex
	pending []domain.Effect
	srv     *http.Server
	ln      net.Listener
}

// Start listens on an ephemeral loopback port and returns the receiver.
func Start() (*Receiver, error) {
	return StartAt("127.0.0.1:0")
}

// StartAt listens on a given loopback address. Use a fixed port when the program under test
// must be started with the webhook URL already set, before recording begins.
// A non-loopback address is refused: a receiver reachable from other machines accepts real traffic.
func StartAt(addr string) (*Receiver, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("capture: address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("capture: %q is not a loopback address; refusing to listen on it", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("capture: listen: %w", err)
	}
	r := &Receiver{URL: "http://" + ln.Addr().String(), ln: ln}
	r.srv = &http.Server{Handler: http.HandlerFunc(r.handle)}
	go r.srv.Serve(ln)
	return r, nil
}

func (r *Receiver) handle(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.pending = append(r.pending, domain.Effect{Method: req.Method, Path: req.URL.Path, Body: string(body)})
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// Drain returns every call received since the last drain, in arrival order, and clears them.
// Call it before a case to discard stray calls, and after the case to collect that case's effects.
func (r *Receiver) Drain() []domain.Effect {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.pending
	r.pending = nil
	return out
}

// Close stops the receiver.
func (r *Receiver) Close() error {
	return r.srv.Close()
}
