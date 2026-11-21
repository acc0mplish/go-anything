package frontdoorgen

// The generated module uses only the standard library. Values from the operator are inserted already
// quoted as Go literals (strconv.Quote, %#v), so a command line cannot inject code into the source.

const goModTemplate = `module {{.Module}}

go 1.22
`

// serverMain is main.go for a server. wire gen finds the http.ListenAndServe(addr, mux) call and inserts
// units.Mount(mux, ...) on the line above it. That line is the serve closure, which is declared before the
// original starts: a failing mount calls log.Fatal while no child process exists, so nothing is orphaned.
const serverMain = `// Command {{.Name}} is the Go front door of this program. go-anything frontdoor scaffold wrote it.
//
// It owns the entry point while every request still reaches the original: the original is started on a
// free port, and every route that no Go unit answers is proxied to it. Move units to Go one at a time with
// go-anything wire gen, which mounts them on mux just above the http.ListenAndServe line in run.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// originalCmd is the original program and its arguments. It runs directly, never through a shell.
var originalCmd = {{.OriginalCmd}}

const (
	// portEnv names the environment variable that tells the original which port to listen on.
	portEnv = {{.PortEnv}}
	// readyTimeout bounds how long the original may take to accept connections.
	readyTimeout = 30 * time.Second
	// stopGrace is how long the original may take to exit after SIGTERM before it is killed.
	stopGrace = 5 * time.Second
	// fallbacksPath reports, as JSON, how many requests went to the original.
	fallbacksPath = "/__go-anything/fallbacks"
)

// fallbacks counts the requests that no Go unit answered, so the original answered them.
var fallbacks atomic.Int64

func main() {
	if err := run(); err != nil {
		log.Printf("frontdoor: %v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// The original gets the environment this program was started with, plus its port.
	env := os.Environ()
	port, err := freePort()
	if err != nil {
		return err
	}
	originalURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	// Units mounted by wire gen that still serve the original reach it through this variable.
	if err := os.Setenv("GA_ORIGINAL_URL", originalURL.String()); err != nil {
		return fmt.Errorf("set GA_ORIGINAL_URL: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+fallbacksPath, serveFallbacks)
	mux.Handle("/", fallback(originalURL))

	// serve is declared before the original starts, so a unit mount that wire gen adds above this line
	// runs, and may fail, while no child process exists yet.
	serve := func() error { return http.ListenAndServe({{.Listen}}, mux) }

	orig, err := startOriginal(append(env, portEnv+"="+strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer orig.stop()
	log.Printf("frontdoor: original pid %d on %s", orig.cmd.Process.Pid, originalURL.Host)
	if err := orig.waitReady(ctx, originalURL.Host); err != nil {
		return err
	}

	served := make(chan error, 1)
	go func() { served <- serve() }()
	log.Printf("frontdoor: serving on %s", {{.Listen}})
	select {
	case <-ctx.Done():
		log.Printf("frontdoor: stopping")
		return nil
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-orig.exited:
		return fmt.Errorf("the original exited: %v", orig.waitErr)
	}
}

// fallback proxies a request to the original and counts it. The Host header the client sent is kept,
// so the original builds the same absolute URLs it would build when reached directly.
func fallback(target *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("frontdoor: the original did not answer a %s request: %v", r.Method, err)
			http.Error(w, "frontdoor: the original did not answer", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbacks.Add(1)
		proxy.ServeHTTP(w, r)
	})
}

func serveFallbacks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"fallbacks": fallbacks.Load()})
}

// freePort asks the system for a free loopback port. The port is released before the original starts,
// so another program could take it first; the original then fails to listen and the front door exits.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// original is the running original program.
type original struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error // set before exited is closed
}

func startOriginal(env []string) (*original, error) {
	cmd := exec.Command(originalCmd[0], originalCmd[1:]...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = procAttr(true)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the original %s: %w", originalCmd[0], err)
	}
	o := &original{cmd: cmd, exited: make(chan struct{})}
	go func() {
		o.waitErr = cmd.Wait()
		close(o.exited)
	}()
	return o, nil
}

// waitReady returns once the original accepts TCP connections on addr.
func (o *original) waitReady(ctx context.Context, addr string) error {
	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted while waiting for the original: %w", ctx.Err())
		case <-o.exited:
			return fmt.Errorf("the original exited before it accepted connections on %s: %v", addr, o.waitErr)
		case <-deadline.C:
			return fmt.Errorf("the original did not accept connections on %s within %s; does it listen on the port in $%s?", addr, readyTimeout, portEnv)
		case <-tick.C:
		}
	}
}

// stop asks the original to exit, kills it after stopGrace, and waits until it is gone.
func (o *original) stop() {
	select {
	case <-o.exited:
	default:
		signalOriginal(o.cmd.Process, true, syscall.SIGTERM)
		select {
		case <-o.exited:
		case <-time.After(stopGrace):
		}
	}
	// Kill what is left, including processes the original started, then wait until it is reaped.
	signalOriginal(o.cmd.Process, true, syscall.SIGKILL)
	<-o.exited
}
`

// cliMain is main.go for a command-line program. wire gen inserts a units.RunCLI check as the first
// statement of main. init runs before it and points GA_ORIGINAL_CLI at this program when it is unset,
// so a unit that still serves the original works without any setup: the unit runs this program again
// with GA_FRONTDOOR_DIRECT=1, and init then runs the original with the fixed leading arguments.
const cliMain = `// Command {{.Name}} is the Go front door of this program. go-anything frontdoor scaffold wrote it.
//
// It owns the entry point while every invocation still reaches the original: the original runs with the
// same arguments, input, output and environment, and this program exits with its exit code. Move commands
// to Go one at a time with go-anything wire gen, which routes them at the start of main.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// originalCmd is the original program and its fixed leading arguments. It runs directly, never through
// a shell; the arguments this program receives are appended.
var originalCmd = {{.OriginalCmd}}

const (
	// fallbackLogEnv names a file that gets one line per invocation the original answered.
	fallbackLogEnv = "GA_FALLBACK_LOG"
	// originalCLIEnv is where units wired by wire gen look for the original program.
	originalCLIEnv = "GA_ORIGINAL_CLI"
	// directEnv is set when this program pointed originalCLIEnv at itself.
	directEnv = "GA_FRONTDOOR_DIRECT"
)

// originalEnv is the environment this program was started with, before init adjusted it for units.
var originalEnv []string

func init() {
	if os.Getenv(directEnv) == "1" {
		// A unit that still serves the original runs this program again. That is the unit's own route
		// to the original, not a fallback, so it is not counted.
		os.Exit(runOriginal(os.Args[1:], withoutKeys(os.Environ(), directEnv, originalCLIEnv)))
	}
	originalEnv = os.Environ()
	if os.Getenv(originalCLIEnv) == "" {
		if exe, err := os.Executable(); err == nil {
			os.Setenv(originalCLIEnv, exe)
			os.Setenv(directEnv, "1")
		}
	}
}

func main() {
	countFallback()
	os.Exit(runOriginal(os.Args[1:], originalEnv))
}

// runOriginal runs the original with args and returns the exit code this program should exit with.
func runOriginal(args, env []string) int {
	argv := append(append([]string(nil), originalCmd[1:]...), args...)
	cmd := exec.Command(originalCmd[0], argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	cmd.SysProcAttr = procAttr(false)

	// A terminal sends Ctrl-C to the whole foreground group, the original included, so it is caught here
	// and left to the original. SIGTERM is usually sent to this process only, so it is passed on.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "frontdoor: start the original %s: %v\n", originalCmd[0], err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 127
		}
		return 126
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if s != os.Interrupt {
					cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		fmt.Fprintf(os.Stderr, "frontdoor: run the original %s: %v\n", originalCmd[0], err)
		return 1
	}
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	return exitLikeSignal(ee)
}

// countFallback appends one line to the file named by GA_FALLBACK_LOG. Counting must never change what
// the caller sees, so a file that cannot be written is skipped silently. Only the line is written, no
// arguments, so the log holds no user data.
func countFallback() {
	path := os.Getenv(fallbackLogEnv)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString("fallback\n")
}

func withoutKeys(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
next:
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		for _, drop := range keys {
			if k == drop {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}
`

// procLinux holds the Linux process handling: the original dies with the front door (Pdeathsig), and a
// server original runs in its own process group so stopping it also stops what it started.
const procLinux = `//go:build linux

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// Pdeathsig fires when the thread that started the child exits. Locking the main goroutine to the main
// thread, which lives as long as the process, keeps that from happening early.
func init() { runtime.LockOSThread() }

// procAttr makes the original receive SIGKILL if the front door dies, even from SIGKILL, so it is never
// orphaned. newGroup also puts it in its own process group. A command-line original stays in the
// terminal's group so it can still read from the terminal.
func procAttr(newGroup bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: newGroup, Pdeathsig: syscall.SIGKILL}
}

// signalOriginal sends sig to the original, or to its whole process group. An error means it is gone.
func signalOriginal(p *os.Process, group bool, sig syscall.Signal) {
	if group {
		_ = syscall.Kill(-p.Pid, sig)
		return
	}
	_ = p.Signal(sig)
}

// exitLikeSignal ends this program the way the original ended when a signal killed it, so a caller sees
// the same thing. Signals the Go runtime would report noisily end it with the shell code 128+n instead.
func exitLikeSignal(ee *exec.ExitError) int {
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 1
	}
	sig := ws.Signal()
	switch sig {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGKILL:
		signal.Reset(sig)
		_ = syscall.Kill(os.Getpid(), sig)
		time.Sleep(time.Second)
	}
	return 128 + int(sig)
}
`

// procOther is the portable fallback. There is no process group and no parent-death signal, so an
// original survives a front door that is killed hard; a normal stop still stops it.
const procOther = `//go:build !linux

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func procAttr(newGroup bool) *syscall.SysProcAttr { return nil }

// signalOriginal sends sig to the original; where the system cannot deliver it, the original is killed.
func signalOriginal(p *os.Process, group bool, sig syscall.Signal) {
	if sig == syscall.SIGKILL || p.Signal(sig) != nil {
		_ = p.Kill()
	}
}

// exitLikeSignal is used when a signal killed the original; without a portable way to re-raise it, the
// front door exits with 1.
func exitLikeSignal(ee *exec.ExitError) int { return 1 }
`
