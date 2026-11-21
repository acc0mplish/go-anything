// Package subject adapts external programs to usecase.Subject.
package subject

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Exec runs a command per input: the input is written to stdin, stdout is the output.
// Each run has a timeout so a hung program cannot stall the gate forever.
type Exec struct {
	Argv    []string
	Timeout time.Duration
	// Env, when set, replaces the process environment for each run (e.g. GOCOVERDIR).
	Env []string
}

// ErrNoCommand is returned when Exec has no program to run.
var ErrNoCommand = errors.New("subject: no command given")

// Run implements usecase.Subject.
func (e Exec) Run(input string) (string, error) {
	if len(e.Argv) == 0 {
		return "", ErrNoCommand
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Argv is passed directly to exec (no shell), so input is never interpreted.
	cmd := exec.CommandContext(ctx, e.Argv[0], e.Argv[1:]...)
	if e.Env != nil {
		cmd.Env = e.Env
	}
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("subject: timed out after %s", timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("subject: %s", msg)
	}
	return stdout.String(), nil
}
