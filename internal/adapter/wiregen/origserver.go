package wiregen

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultOriginalStartTimeout is how long a fresh original gets to answer its first HTTP request
// before the start is given up (-original-start-timeout).
const defaultOriginalStartTimeout = 60 * time.Second

// originalOutputTailBytes is how much of a failed start's output an error may carry. The original
// inherits the whole environment (see StartOriginal), so its output can contain configuration values
// and secrets: only the tail, where the actual failure usually is, ever reaches a message.
const originalOutputTailBytes = 2000

// OriginalServer is one started copy of the operator's original program. URL is the base this round's
// checks ask. Stop kills the program's whole process group and waits until it is gone; it is safe to
// call once per server (Refine wires it into the round's deferred cleanup).
type OriginalServer struct {
	URL  string
	stop func()
}

// Stop kills the original's process group — anything the command spawned goes with it — and waits for
// the exit, so the next round's start cannot meet a half-dead copy holding the port.
func (s *OriginalServer) Stop() {
	if s.stop != nil {
		s.stop()
	}
}

// serveCommandNeedsPort reports whether the -serve-original command still carries the {{port}}
// placeholder. Without it the original cannot bind the port this tool picked, so readiness polling
// watches a port nothing will ever listen on; Refine logs a warning when this is true.
func serveCommandNeedsPort(command string) bool {
	return strings.Contains(command, "{{port}}")
}

// StartOriginal starts the operator's original program from the -serve-original command and waits
// until it answers HTTP. The {{port}} placeholder in the command is replaced with a free port this
// tool picked; if the command dies right after the pick (a lost bind race), the start retries on a
// different port, at most three times. Any HTTP status counts as ready: the contract is "answers
// requests", not "answers them 200".
//
// The command runs through sh in this process's working directory, so the operator's relative paths
// resolve as if they had typed the command themselves. The original inherits the WHOLE environment on
// purpose: the rewrite is agent-generated code and gets an allowlist (allowedParentEnv, issue #20),
// but the original is the operator's own program, which may need its real credentials and settings to
// run at all. Because of that inheritance a failed start reports only the tail of its output.
//
// Unix only: on Windows the start is refused without being attempted, because the command is run
// through sh (the build still compiles there).
func StartOriginal(command string, startTimeout time.Duration) (*OriginalServer, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("not supported on Windows: -serve-original starts the original through sh")
	}
	if startTimeout <= 0 {
		startTimeout = defaultOriginalStartTimeout
	}
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		port, err := pickFreePort()
		if err != nil {
			return nil, fmt.Errorf("serve-original: pick a free port: %w", err)
		}
		srv, err := startOriginalOnce(command, port, startTimeout)
		if err == nil {
			return srv, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("serve-original: the original did not start after %d port attempts; last error: %w", maxAttempts, lastErr)
}

// pickFreePort asks the kernel for a free port and gives it back immediately. The pick-then-bind race
// cannot be removed, so the caller retries on another port when the start dies early.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// startOriginalOnce is one port attempt: substitute {{port}}, start the command through sh, and poll
// until anything answers HTTP or the process dies first. A start that cannot get ready is brought down
// before the error returns, so no half-started copy outlives the attempt.
func startOriginalOnce(command string, port int, startTimeout time.Duration) (*OriginalServer, error) {
	cmd := exec.Command("sh", "-c", strings.ReplaceAll(command, "{{port}}", strconv.Itoa(port)))
	// The whole environment is inherited on purpose — see StartOriginal. Nothing of it is stored: a
	// failure message carries the output tail only.
	cmd.Env = os.Environ()
	// Working directory: this tool's current directory (cmd.Dir stays empty), so the operator's
	// relative paths in the command resolve from where they run go-anything.
	// Own process group, so cleanup can kill the command and anything it spawned, and a grandchild that
	// inherits the output pipes cannot hold Wait open after the kill (issue #18).
	ownProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	var out originalOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("serve-original: the command did not start: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	down := func() {
		if cmd.Process != nil {
			killProcessGroup(cmd.Process.Pid)
		}
		<-exited
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(startTimeout)
	for {
		// Exit before ready is a verdict, not a wait: a dead process cannot become ready, and its output
		// says why (the tail only — see originalOutputTailBytes). The group kill still runs: a leader
		// that dies early can leave children in the group, and this tool starts the original with the
		// whole inherited environment, so a survivor would keep holding the port and the secrets in its
		// memory after the tool returns. down() is safe here — exited is already closed, so the wait
		// inside it returns immediately.
		select {
		case <-exited:
			down()
			return nil, fmt.Errorf("serve-original: the original exited during startup: %s", out.tail(originalOutputTailBytes))
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return &OriginalServer{URL: url, stop: down}, nil
		}
		if time.Now().After(deadline) {
			down()
			return nil, fmt.Errorf("serve-original: the original did not answer HTTP on %s within %s: %s", url, startTimeout, out.tail(originalOutputTailBytes))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// originalOutput collects the started command's output for failure diagnostics only. It is locked
// because the Wait goroutine writes while a failure path reads the tail.
type originalOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *originalOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

// tail returns the last n bytes of the collected output. Secrets the command may have printed into an
// earlier line stay out of the error.
func (o *originalOutput) tail(n int) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.buf.String()
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
