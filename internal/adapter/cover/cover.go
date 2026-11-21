// Package cover captures statement coverage of an original Go program while it is recorded.
//
// It uses the official Go mechanism: the original is built with `go build -cover`, each run
// writes its counters into GOCOVERDIR, and `go tool covdata` merges them into a coverprofile.
// Nothing is estimated; the numbers are exactly what the Go toolchain measured.
package cover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// Build compiles pkg with coverage instrumentation into outBin and returns its path.
func Build(pkg, outBin string) error {
	if pkg == "" {
		return errors.New("cover: package is required")
	}
	var stderr bytes.Buffer
	cmd := exec.Command("go", "build", "-cover", "-o", outBin, pkg)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cover: build %s: %w: %s", pkg, err, stderr.String())
	}
	return nil
}

// ImportPath resolves a package argument (a path relative to dir, "." or an import path) to its
// import path. dir is the directory the package argument is relative to; "" means the current one.
func ImportPath(dir, pkg string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}", pkg)
	cmd.Dir = dir
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cover: resolve import path of %s: %w: %s", pkg, err, stderr.String())
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("cover: %s has no import path", pkg)
	}
	return p, nil
}

// Collector gathers counters from every instrumented run into one directory.
type Collector struct {
	Dir string
}

// NewCollector makes a fresh, empty counter directory.
func NewCollector(dir string) (Collector, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Collector{}, fmt.Errorf("cover: mkdir %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Collector{}, fmt.Errorf("cover: read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return Collector{}, fmt.Errorf("cover: %s is not empty; use a fresh directory so old runs do not leak in", dir)
	}
	return Collector{Dir: dir}, nil
}

// Env returns the environment for one instrumented run.
func (c Collector) Env() []string {
	return append(os.Environ(), "GOCOVERDIR="+c.Dir)
}

// Profile merges the collected counters into a coverprofile and reads its statement coverage.
func (c Collector) Profile(ctx context.Context) (domain.Coverage, error) {
	prof := filepath.Join(c.Dir, "..", filepath.Base(c.Dir)+".coverprofile")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "go", "tool", "covdata", "textfmt", "-i="+c.Dir, "-o="+prof)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return domain.Coverage{}, fmt.Errorf("cover: merge counters: %w: %s", err, stderr.String())
	}
	f, err := os.Open(prof)
	if err != nil {
		return domain.Coverage{}, fmt.Errorf("cover: open profile: %w", err)
	}
	defer f.Close()
	return domain.ParseCoverProfile(f)
}

// Timeout bounds the merge step so a bad counter directory cannot hang recording.
const Timeout = 2 * time.Minute
