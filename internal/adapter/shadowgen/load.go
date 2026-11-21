package shadowgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// listedPackage is the part of `go list -json` output the loader needs.
type listedPackage struct {
	ImportPath string
	Name       string
	Export     string
	Error      *struct{ Err string }
}

// loadPackages type-checks the original and the rewrite as seen from moduleRoot. It asks the go command
// for compiled export data of both packages and their dependencies, then reads it with go/importer. One
// importer serves both packages, so a type they share is the same types.Type and types.Identical holds.
func loadPackages(moduleRoot, original, rewrite string) (orig, rew *types.Package, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-export", "-deps",
		"-json=ImportPath,Name,Export,Error", "--", original, rewrite)
	cmd.Dir = moduleRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("shadow gen: go list in %s: %v\n%s", moduleRoot, err, strings.TrimSpace(stderr.String()))
	}
	exports := map[string]string{}
	names := map[string]string{}
	dec := json.NewDecoder(&stdout)
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("shadow gen: read go list output: %w", err)
		}
		if p.Error != nil && (p.ImportPath == original || p.ImportPath == rewrite) {
			return nil, nil, fmt.Errorf("shadow gen: package %s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		names[p.ImportPath] = p.Name
	}
	for _, path := range []string{original, rewrite} {
		if exports[path] == "" {
			return nil, nil, fmt.Errorf("shadow gen: package %s has no compiled export data; does it build in %s?", path, moduleRoot)
		}
		if names[path] == "main" {
			return nil, nil, fmt.Errorf("shadow gen: %s is a main package; only library packages can be shadowed", path)
		}
	}
	lookup := func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	}
	imp := importer.ForCompiler(token.NewFileSet(), "gc", lookup)
	if orig, err = imp.Import(original); err != nil {
		return nil, nil, fmt.Errorf("shadow gen: load %s: %w", original, err)
	}
	if rew, err = imp.Import(rewrite); err != nil {
		return nil, nil, fmt.Errorf("shadow gen: load %s: %w", rewrite, err)
	}
	return orig, rew, nil
}
