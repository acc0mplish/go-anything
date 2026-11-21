// Package gatecheck runs the Go toolchain and source scans that make up the completion gate.
// It only reads the project and runs the standard go tools; it never publishes or deletes anything.
package gatecheck

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// commandTimeout bounds each tool run so a hung test cannot hang the gate.
const commandTimeout = 15 * time.Minute

// Options chooses which checks run.
type Options struct {
	Root      string
	SkipTests bool
}

// Run runs every static and toolchain check and returns one result per check.
func Run(opts Options) []domain.GateCheck {
	root := opts.Root
	if root == "" {
		root = "."
	}
	var out []domain.GateCheck
	out = append(out, formatCheck(root))
	out = append(out, toolCheck(root, "go vet", "go", "vet", "./..."))
	out = append(out, buildCheck(root))
	if !opts.SkipTests {
		out = append(out, toolCheck(root, "go test", "go", "test", "-count=1", "./..."))
	}
	out = append(out, scanCheck(root, "no unimplemented functions", domain.ScanUnimplemented))
	out = append(out, scanCheck(root, "unsafe justified by SAFETY comments", domain.ScanUnsafe))
	return out
}

// formatCheck fails when gofmt would change any file. gofmt -l lists the files that differ.
func formatCheck(root string) domain.GateCheck {
	res := runTool(root, "gofmt", "-l", ".")
	if res.err != nil {
		return domain.GateCheck{Name: "gofmt", Passed: false, Detail: res.err.Error()}
	}
	if files := strings.TrimSpace(res.out); files != "" {
		return domain.GateCheck{Name: "gofmt", Passed: false, Detail: "not formatted: " + strings.ReplaceAll(files, "\n", ", ")}
	}
	return domain.GateCheck{Name: "gofmt", Passed: true, Detail: "clean"}
}

// buildCheck builds every package into a temp directory. A plain `go build ./...` writes a binary into
// root when ./... matches a single main package, and a check must not leave files behind.
func buildCheck(root string) domain.GateCheck {
	outDir, err := os.MkdirTemp("", "ga-gate-build-")
	if err != nil {
		return domain.GateCheck{Name: "go build", Passed: false, Detail: "temp dir: " + err.Error()}
	}
	defer os.RemoveAll(outDir)
	return toolCheck(root, "go build", "go", "build", "-o", outDir+string(os.PathSeparator), "./...")
}

func toolCheck(root, name, cmd string, args ...string) domain.GateCheck {
	res := runTool(root, cmd, args...)
	if res.err != nil {
		return domain.GateCheck{Name: name, Passed: false, Detail: tail(res.out + res.err.Error())}
	}
	return domain.GateCheck{Name: name, Passed: true, Detail: "ok"}
}

// scanCheck applies a source scan to every non-test Go file under root.
func scanCheck(root, name string, scan func(file, src string) []domain.Finding) domain.GateCheck {
	var findings []domain.Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		findings = append(findings, scan(filepath.ToSlash(rel), string(data))...)
		return nil
	})
	if err != nil {
		return domain.GateCheck{Name: name, Passed: false, Detail: err.Error()}
	}
	if len(findings) == 0 {
		return domain.GateCheck{Name: name, Passed: true, Detail: "ok"}
	}
	first := findings[0]
	detail := fmt.Sprintf("%d finding(s); first: %s:%d %s", len(findings), first.File, first.Line, first.Detail)
	return domain.GateCheck{Name: name, Passed: false, Detail: detail}
}

type toolResult struct {
	out string
	err error
}

func runTool(dir, name string, args ...string) toolResult {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return toolResult{out: stdout.String() + stderr.String(), err: fmt.Errorf("%s timed out after %s", name, commandTimeout)}
	}
	if err != nil {
		return toolResult{out: stdout.String() + stderr.String(), err: fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)}
	}
	return toolResult{out: stdout.String()}
}

// tail keeps the end of long tool output, where the error usually is.
func tail(s string) string {
	const max = 1500
	s = strings.TrimSpace(s)
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
