// Package analyze is the filesystem adapter that implements usecase.Analyzer for Go source.
package analyze

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-anything/go-anything/internal/domain"
)

// GoSource analyzes Go source trees using the standard library parser and type checker
// (no execution of code).
type GoSource struct{}

// source is one package directory: its analysis row and the parsed files that match the
// current build context.
type source struct {
	dir   string
	pkg   *domain.Package
	files []*ast.File
}

// Analyze walks root, parses every non-test .go file, and groups them into packages. It then
// type-checks each package and walks function bodies for types, call relations and risk sites.
// A package that fails to type-check keeps its syntax level data and records its errors.
// Vendor, hidden directories and testdata are skipped, as the go tool skips them.
func (GoSource) Analyze(root string) (domain.Analysis, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	module, err := readModule(filepath.Join(abs, "go.mod"))
	if err != nil {
		return domain.Analysis{}, err
	}
	byDir := map[string]*source{}
	testFiles := map[string]int{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if path != abs && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles[filepath.Dir(path)]++
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("analyze: read %s: %w", path, err)
		}
		file, err := parser.ParseFile(fset, path, data, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("analyze: parse %s: %w", path, err)
		}
		dir := filepath.Dir(path)
		src, ok := byDir[dir]
		if !ok {
			rel, _ := filepath.Rel(abs, dir)
			src = &source{dir: dir, pkg: &domain.Package{ImportPath: importPath(module, rel), Dir: filepath.ToSlash(rel), Internal: isInternal(rel)}}
			byDir[dir] = src
		}
		pkg := src.pkg
		base := filepath.Base(path)
		pkg.Files = append(pkg.Files, base)
		pkg.Lines += bytes.Count(data, []byte{'\n'})
		for _, imp := range file.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				pkg.Imports = appendUnique(pkg.Imports, p)
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil {
				name = receiverName(fn) + "." + name
			}
			pkg.Funcs = appendUnique(pkg.Funcs, name)
			if fn.Name.IsExported() {
				pkg.Exported = appendUnique(pkg.Exported, name)
			}
		}
		// Only files that the go tool would build here are type-checked; the syntax level data
		// above still covers every file.
		if match, err := matchFile(dir, base, data); err == nil && match {
			src.files = append(src.files, file)
		} else {
			pkg.BuildIgnored = append(pkg.BuildIgnored, base)
		}
		return nil
	})
	if err != nil {
		return domain.Analysis{}, err
	}
	srcs := make([]*source, 0, len(byDir))
	for dir, src := range byDir {
		p := src.pkg
		p.Tests = testFiles[dir]
		sort.Strings(p.Files)
		sort.Strings(p.Imports)
		sort.Strings(p.Funcs)
		sort.Strings(p.Exported)
		sort.Strings(p.BuildIgnored)
		srcs = append(srcs, src)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].pkg.ImportPath < srcs[j].pkg.ImportPath })
	out := domain.Analysis{Root: filepath.ToSlash(abs), Module: module, Language: LangGo}
	deepen(&out, abs, module, fset, srcs)
	return out, nil
}

// matchFile reports whether the go tool would build the file in the current build context,
// reading the build constraints from data instead of opening the file again.
func matchFile(dir, name string, data []byte) (bool, error) {
	ctxt := build.Default
	ctxt.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	return ctxt.MatchFile(dir, name)
}

func readModule(gomod string) (string, error) {
	f, err := os.Open(gomod)
	if err != nil {
		return "", fmt.Errorf("analyze: no go.mod at %s: %w", gomod, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("analyze: read go.mod: %w", err)
	}
	return "", fmt.Errorf("analyze: go.mod has no module directive")
}

func importPath(module, rel string) string {
	if rel == "." {
		return module
	}
	return module + "/" + filepath.ToSlash(rel)
}

func isInternal(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "internal" {
			return true
		}
	}
	return false
}

func receiverName(fn *ast.FuncDecl) string {
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	// A generic receiver is written T[P] or T[P, Q].
	switch g := t.(type) {
	case *ast.IndexExpr:
		t = g.X
	case *ast.IndexListExpr:
		t = g.X
	}
	if id, ok := t.(*ast.Ident); ok && id.Name != "" && unicode.IsLetter(rune(id.Name[0])) {
		return id.Name
	}
	return "?"
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}
