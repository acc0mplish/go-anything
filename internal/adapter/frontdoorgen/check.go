package frontdoorgen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// FallbackLogEnv names the file a CLI front door appends one line to per fallback.
const FallbackLogEnv = "GA_FALLBACK_LOG"

const (
	buildTimeout = 5 * time.Minute
	// startTimeout covers the original's own readiness timeout (30s in main.go) plus the front door start.
	startTimeout = 45 * time.Second
	stopTimeout  = 15 * time.Second
	closeTimeout = 5 * time.Second
)

// Report is what Check observed.
type Report struct {
	Kind       string
	Total      int
	Passed     int
	Fallbacks  int64
	Mismatches []usecase.AppMismatch
	// Server only.
	ListenAddr   string // where the replay reached the front door
	OriginalAddr string // where the front door started the original
	PortsClosed  bool   // both addresses refused connections after the stop
}

// AllPassed reports whether every recorded case matched.
func (r Report) AllPassed() bool { return r.Total > 0 && r.Passed == r.Total }

// Check builds the front door, runs it the way a user would, and replays the recorded cases through it
// with the same code as app replay. A server front door is stopped with SIGTERM afterwards, and both
// its port and the original's port must then refuse connections. Relative paths in the original
// command resolve against the current directory, as they do for app replay.
func Check(ctx context.Context, res Result, rec domain.AppRecording) (Report, error) {
	kind, err := DetectKind(rec.Cases)
	if err != nil {
		return Report{}, err
	}
	if kind != res.Kind {
		return Report{}, fmt.Errorf("frontdoorgen: the front door is %s but the recording holds %s cases", res.Kind, kind)
	}
	tmp, err := os.MkdirTemp("", "go-anything-frontdoor-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "frontdoor")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := build(ctx, res.Dir, bin); err != nil {
		return Report{}, err
	}
	if kind == KindCLI {
		return checkCLI(rec, bin, filepath.Join(tmp, "fallbacks.log"))
	}
	return checkServer(ctx, rec, bin, res.Listen)
}

func build(ctx context.Context, dir, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = dir
	// A go.work above the repo would not list this new module and would make the build fail.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("frontdoorgen: go build in %s: %w\n%s", dir, err, out.String())
	}
	return nil
}

// envMu serializes checks that set GA_FALLBACK_LOG: app.Runner passes this process's environment on.
var envMu sync.Mutex

func checkCLI(rec domain.AppRecording, bin, logPath string) (Report, error) {
	envMu.Lock()
	defer envMu.Unlock()
	prev, had := os.LookupEnv(FallbackLogEnv)
	if err := os.Setenv(FallbackLogEnv, logPath); err != nil {
		return Report{}, err
	}
	defer func() {
		if had {
			os.Setenv(FallbackLogEnv, prev)
		} else {
			os.Unsetenv(FallbackLogEnv)
		}
	}()
	verdict, err := usecase.AppReplay(rec, app.Runner{Argv0: bin})
	if err != nil {
		return Report{}, err
	}
	n, err := countLines(logPath)
	if err != nil {
		return Report{}, err
	}
	return Report{Kind: KindCLI, Total: verdict.Total, Passed: verdict.Matched, Mismatches: verdict.Mismatches, Fallbacks: n}, nil
}

func countLines(path string) (int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("frontdoorgen: read fallback log: %w", err)
	}
	defer f.Close()
	var n int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}

// originalLine is what the server front door logs once it started the original.
var originalLine = regexp.MustCompile(`original pid (\d+) on (\S+)`)

func checkServer(ctx context.Context, rec domain.AppRecording, bin, listen string) (rep Report, err error) {
	rep.Kind = KindServer
	addr, err := dialAddr(listen)
	if err != nil {
		return rep, err
	}
	rep.ListenAddr = addr
	// The address must be free first; otherwise the replay would reach whatever already listens there.
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return rep, fmt.Errorf("frontdoorgen: listen address %s is not free (%v); scaffold with another -listen or stop what uses it", listen, err)
	}
	l.Close()

	logs := &lockedBuffer{}
	cmd := exec.Command(bin)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return rep, fmt.Errorf("frontdoorgen: start the front door: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	stopped := false
	origPID := 0
	defer func() {
		// Whatever happened, leave no process behind.
		if !stopped {
			cmd.Process.Kill()
			<-exited
		}
		if origPID > 0 && rep.OriginalAddr != "" && isOpen(rep.OriginalAddr) {
			if p, ferr := os.FindProcess(origPID); ferr == nil {
				p.Kill()
			}
		}
	}()

	if err := waitOpen(ctx, addr, exited, startTimeout); err != nil {
		return rep, fmt.Errorf("frontdoorgen: the front door did not start: %w\noutput:\n%s", err, logs.String())
	}
	if m := originalLine.FindStringSubmatch(logs.String()); m != nil {
		origPID, _ = strconv.Atoi(m[1])
		rep.OriginalAddr = m[2]
	}

	verdict, err := usecase.AppReplay(rec, app.Runner{BaseURL: "http://" + addr})
	if err != nil {
		return rep, err
	}
	rep.Total, rep.Passed, rep.Mismatches = verdict.Total, verdict.Matched, verdict.Mismatches
	if rep.Fallbacks, err = readFallbacks(addr); err != nil {
		return rep, err
	}

	// Stop it the way a supervisor would, and let it stop the original.
	if serr := cmd.Process.Signal(syscall.SIGTERM); serr != nil {
		cmd.Process.Kill()
	}
	select {
	case <-exited:
	case <-time.After(stopTimeout):
		cmd.Process.Kill()
		<-exited
		stopped = true
		return rep, fmt.Errorf("frontdoorgen: the front door did not stop within %s of SIGTERM and was killed", stopTimeout)
	}
	stopped = true
	listenClosed := waitClosed(addr, closeTimeout)
	origClosed := rep.OriginalAddr != "" && waitClosed(rep.OriginalAddr, closeTimeout)
	rep.PortsClosed = listenClosed && origClosed
	switch {
	case rep.OriginalAddr == "":
		return rep, fmt.Errorf("frontdoorgen: the front door did not log the original's address, so its port could not be checked\noutput:\n%s", logs.String())
	case !origClosed:
		return rep, fmt.Errorf("frontdoorgen: the original still listens on %s after the front door stopped; it was killed now (pid %d)", rep.OriginalAddr, origPID)
	case !listenClosed:
		return rep, fmt.Errorf("frontdoorgen: %s still accepts connections after the front door stopped", addr)
	}
	return rep, nil
}

// dialAddr turns a listen address into one a client can dial.
func dialAddr(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("frontdoorgen: listen address %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func readFallbacks(addr string) (int64, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + addr + "/__go-anything/fallbacks")
	if err != nil {
		return 0, fmt.Errorf("frontdoorgen: read the fallback count: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("frontdoorgen: fallback count answered %s", resp.Status)
	}
	var body struct {
		Fallbacks *int64 `json:"fallbacks"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil || body.Fallbacks == nil {
		return 0, fmt.Errorf("frontdoorgen: fallback count is not {\"fallbacks\": n}: %v", err)
	}
	return *body.Fallbacks, nil
}

func isOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func waitOpen(ctx context.Context, addr string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.After(timeout)
	for !isOpen(addr) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return errors.New("it exited")
		case <-deadline:
			return fmt.Errorf("nothing accepted connections on %s within %s", addr, timeout)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

func waitClosed(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isOpen(addr) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !isOpen(addr)
}

// lockedBuffer collects process output that is written and read from different goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
