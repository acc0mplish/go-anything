package wiregen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
	"net"
)

// liveModule writes a module whose main.go mounts the unit behind middleware that rejects any path
// containing "blocked" with 403. The unit handler alone would answer 200 for everything, so a probe
// run that reports 403 for the blocked path can only have come through the assembled application.
func liveModule(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and starts a real application")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/live\n\ngo 1.27.4\n")
	write("main.go", `package main

import (
	"net/http"
	"strings"

	"example.com/live/units/orders"
)

func main() {
	mux := http.NewServeMux()
	mux.Handle("/orders/", http.HandlerFunc(orders.Handle))
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "blocked") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
	http.ListenAndServe("127.0.0.1:18671", guarded)
}
`)
	write("units/orders/rewrite.go", `package orders

import (
	"fmt"
	"io"
	"net/http"
)

// Handle answers every order id with the id echoed back.
func Handle(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	body, _ := io.ReadAll(r.Body)
	fmt.Fprintf(w, "order %s %s", id, string(body))
}
`)
	return root
}

func TestRunHTTPProbesLiveGoesThroughMainMiddleware(t *testing.T) {
	root := liveModule(t)
	probes := []domain.AppCase{
		{Kind: "http", Method: "GET", Path: "/orders/1"},
		{Kind: "http", Method: "GET", Path: "/orders/blocked-9"},
	}
	got, err := RunHTTPProbesLive(root, "orders", "http://127.0.0.1:1", probes)
	if err != nil {
		t.Fatalf("the live probes must run against the assembled app: %v", err)
	}
	if got[0].Status != 200 || !strings.Contains(got[0].ResponseBody, "/orders/1") {
		t.Fatalf("a normal probe must reach the unit: %+v", got[0])
	}
	if got[1].Status != 403 {
		t.Fatalf("main.go's middleware must be exercised: the unit alone would answer 200, got %+v", got[1])
	}
}

func TestRunHTTPProbesLiveFailsWhenTheProgramCannotStart(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/broken\n\ngo 1.27.4\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	if _, err := RunHTTPProbesLive(root, "orders", "", []domain.AppCase{{Kind: "http", Method: "GET", Path: "/x"}}); err == nil {
		t.Fatal("a program that never listens must fail the run, not pass it")
	}
}

// TestRunHTTPProbesLiveRefusesAForeignListener pins the ownership rule (issue #17): when another
// process already accepts connections on the address, the run must fail instead of letting that
// process answer the probes in the rewrite's name.
func TestRunHTTPProbesLiveRefusesAForeignListener(t *testing.T) {
	root := liveModule(t)
	ln, err := net.Listen("tcp", "127.0.0.1:18671")
	if err != nil {
		t.Skipf("address busy: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := RunHTTPProbesLive(root, "orders", "", []domain.AppCase{{Kind: "http", Method: "GET", Path: "/orders/1"}}); err == nil {
		t.Fatal("a foreign listener on the address must fail the run")
	} else if !strings.Contains(err.Error(), "another process owns it") {
		t.Fatalf("the failure must say who owns the address: %v", err)
	}
}
