package wiregen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// CasesFile is where the generated behavior test for a unit lives.
const CasesFile = "cases_test.go"

// WriteCases writes units/<unit>/cases_test.go.
func WriteCases(root, unit string, data []byte) (string, error) {
	// The comparison copies go with every cases file: the cases test calls them, and writing them here
	// means no caller can write one without the other.
	helpers, err := usecase.ComparisonTestFiles(unit)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "units", unit)
	for name, src := range helpers {
		hp := filepath.Join(dir, name)
		if err := os.WriteFile(hp, src, 0o644); err != nil {
			return "", fmt.Errorf("wire: write %s: %w", hp, err)
		}
	}
	p := filepath.Join(dir, CasesFile)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", fmt.Errorf("wire: write %s: %w", p, err)
	}
	return p, nil
}

// RunAgent asks an external coding agent to write the rewrite, then checks it against the recorded
// behavior with go test. The agent command runs through the shell in the module root, reads the brief
// on stdin, and must write units/<unit>/rewrite.go. Each failed attempt's test output goes into the
// next brief. It stops at the first attempt that passes.
func RunAgent(root, unit, command string, attempts int, brief func(previousFailure string) string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("wire: an agent command is required")
	}
	if attempts < 1 {
		attempts = 1
	}
	var lastOut string
	for i := 1; i <= attempts; i++ {
		prompt := brief(lastOut)
		if err := runShell(root, command, prompt); err != nil {
			lastOut = "the agent command failed: " + err.Error()
			continue
		}
		out, err := goTest(root, unit)
		if err == nil {
			return fmt.Sprintf("passed on attempt %d", i), nil
		}
		lastOut = out
	}
	return "", fmt.Errorf("wire: the rewrite did not pass the recorded behavior after %d attempt(s); last output:\n%s", attempts, tail(lastOut))
}

// runShell runs the agent command with the brief on stdin. The command is the user's own setting.
func runShell(dir, command, input string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	// The operator still sees the agent live; the copy lets a failure say why the agent stopped
	// (a usage limit, a missing login) instead of only "exit status 1".
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stderr, &out)
	cmd.Stderr = io.MultiWriter(os.Stderr, &out)
	if err := cmd.Run(); err != nil {
		if last := lastLines(out.String(), 5); last != "" {
			return fmt.Errorf("%w: %s", err, last)
		}
		return err
	}
	return nil
}

// lastLines is the end of an agent's output, at most n non-empty lines.
func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return domain.ClipForReport(strings.Join(keep, " | "), 1000)
}

func goTest(root, unit string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "./units/"+unit+"/")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func tail(s string) string {
	const max = 3000
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
