package analyze

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// listTimeout bounds the single go list call that resolves dependencies.
const listTimeout = 2 * time.Minute

// listedPkg is the part of `go list -json` output the dependency importer needs.
type listedPkg struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	ImportMap  map[string]string
	Standard   bool
	Error      *struct{ Err string }

	pkg   *types.Package
	err   error
	state int // 0 not started, 1 in progress, 2 done
}

// parseFiles parses one dependency's files on all CPUs. A token.FileSet is safe for concurrent
// use. Parsing per package, when it is imported, keeps only one package's syntax alive at a
// time; the standard library and large dependencies would otherwise hold hundreds of MB.
func parseFiles(fset *token.FileSet, dir string, names []string) ([]*ast.File, error) {
	files := make([]*ast.File, len(names))
	errs := make([]error, len(names))
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			files[i], errs[i] = parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return files, nil
}

// listDeps resolves the given import paths and everything they depend on with one go list call.
// Without it the "source" importer runs go list once per imported package, which costs tens of
// seconds on a slow file system. Listing only the external paths, not ./..., keeps go list from
// rescanning the module tree. An error means the caller falls back to the "source" importer.
func listDeps(root string, paths []string) (map[string]*listedPkg, error) {
	var args []string
	for _, p := range paths {
		if validImportPath(p) { // paths come from the analyzed code; never let one act as a flag
			args = append(args, p)
		}
	}
	if len(args) == 0 {
		return map[string]*listedPkg{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", append([]string{"list", "-e", "-deps",
		"-json=ImportPath,Dir,GoFiles,CgoFiles,ImportMap,Standard,Error", "--"}, args...)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	byPath := map[string]*listedPkg{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPkg
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: decode: %w", err)
		}
		if p.Error != nil || p.Dir == "" || len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue // left to the fallback importer, which reports the error in context
		}
		byPath[p.ImportPath] = &p
	}
	return byPath, nil
}

// checkDep checks one listed dependency for its exported API only, as the "source"
// importer does: function bodies are skipped and cgo's C package is faked.
func (m *moduleImporter) checkDep(p *listedPkg) (*types.Package, error) {
	switch p.state {
	case 2:
		return p.pkg, p.err
	case 1:
		return nil, fmt.Errorf("import cycle through %s", p.ImportPath)
	}
	p.state = 1
	defer func() { p.state = 2 }()
	files, err := parseFiles(m.fset, p.Dir, append(append([]string{}, p.GoFiles...), p.CgoFiles...))
	if err != nil {
		p.err = err
		return nil, p.err
	}
	var first error
	conf := types.Config{
		Importer:         depScope{m: m, p: p},
		IgnoreFuncBodies: true,
		FakeImportC:      true,
		Error: func(err error) {
			if first == nil {
				first = err
			}
		},
	}
	pkg, err := safeCheck(&conf, p.ImportPath, m.fset, files, nil)
	var panicked *checkerPanic
	if errors.As(err, &panicked) {
		first = panicked
	}
	p.pkg = pkg
	// Like the "source" importer, a dependency with errors is still returned: the module's own
	// type check then reports only what it actually uses and cannot resolve.
	if pkg == nil && first != nil {
		p.err = first
	}
	return p.pkg, p.err
}

// depScope imports on behalf of one dependency, applying its ImportMap (the standard library
// vendors golang.org/x packages under "vendor/").
type depScope struct {
	m *moduleImporter
	p *listedPkg
}

func (d depScope) Import(path string) (*types.Package, error) {
	return d.ImportFrom(path, d.p.Dir, 0)
}

func (d depScope) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if mapped, ok := d.p.ImportMap[path]; ok {
		path = mapped
	}
	return d.m.ImportFrom(path, dir, mode)
}

var _ types.ImporterFrom = depScope{}

// validImportPath accepts the characters the go tool allows in import paths and rejects
// anything that could be read as a flag or climb out of a directory.
func validImportPath(p string) bool {
	if p == "" || p[0] == '-' || p[0] == '/' || p[0] == '.' || p == "C" {
		return false
	}
	for _, r := range p {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' || r == '~' || r == '/' || r == '+'
		if !ok {
			return false
		}
	}
	for _, elem := range strings.Split(p, "/") {
		if elem == "" || elem == "." || elem == ".." {
			return false
		}
	}
	return true
}
