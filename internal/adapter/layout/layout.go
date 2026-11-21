// Package layout plans and writes what a Go module needs to be installed and shipped:
// `go install` instructions, a tag-triggered release workflow and a local cross-build script.
// The plan is computed from go.mod and `go list` output only. Nothing is published, ever.
package layout

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Target is one GOOS/GOARCH pair.
type Target struct {
	GOOS   string
	GOARCH string
}

func (t Target) String() string { return t.GOOS + "/" + t.GOARCH }

// DefaultTargets are the platforms planned when no -targets are given.
var DefaultTargets = []Target{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
}

// HostTarget is the platform go-anything itself runs on.
func HostTarget() Target { return Target{runtime.GOOS, runtime.GOARCH} }

// MainPkg is one package main of the module, which is one installable binary.
type MainPkg struct {
	ImportPath string
	Dir        string // slash-separated, relative to the module root; "." for the root
	BinName    string // what `go install` names the binary (without .exe)
	// VersionVar is true when the package declares `var version string` (or `var version = "..."`),
	// the only form `-ldflags -X main.version=...` can set.
	VersionVar bool
}

// PlannedFile is one file the plan would create. Existing files are never overwritten.
type PlannedFile struct {
	Path    string // slash-separated, relative to the module root
	Content string
	Mode    fs.FileMode
	Exists  bool // already present: kept as is
}

// LayoutPlan is everything `go-anything package` would do for one module.
type LayoutPlan struct {
	Root      string // absolute module root
	Module    string
	GoVersion string
	Mains     []MainPkg
	UsesCgo   bool
	CgoFiles  []string // files that import "C": module files relative to root, dependency files as importpath/file
	Targets   []Target
	Files     []PlannedFile
	Notes     []string // warnings the user should read before the first release
}

// Options adjust Plan.
type Options struct {
	Targets []Target // empty means DefaultTargets
	Host    Target   // zero means HostTarget(); the only target when cgo is used
}

// safePath limits the module path, package directories and binary names that end up in generated
// shell and YAML. Go import paths fit easily; anything else is refused rather than quoted.
var safePath = regexp.MustCompile(`^[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*$`)

// Plan computes the layout plan for the module at root. It runs `go list` (and `go tool dist list`)
// but writes nothing.
func Plan(ctx context.Context, root string, opts Options) (*LayoutPlan, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("layout: root: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	gomod, err := os.ReadFile(filepath.Join(abs, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("layout: %s is not a module root: %w", abs, err)
	}
	mf, err := parseGoMod(gomod)
	if err != nil {
		return nil, fmt.Errorf("layout: go.mod: %w", err)
	}
	if !safePath.MatchString(mf.module) {
		return nil, fmt.Errorf("layout: module path %q has characters this generator will not put in a shell script", mf.module)
	}

	targets := opts.Targets
	if len(targets) == 0 {
		targets = DefaultTargets
	}
	targets = dedupeTargets(targets)
	if err := validateTargets(ctx, abs, targets); err != nil {
		return nil, err
	}
	host := opts.Host
	if host == (Target{}) {
		host = HostTarget()
	}

	// One listing per target: package main directories and cgo files depend on GOOS/GOARCH.
	listings := make([][]listPkg, len(targets))
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			listings[i], errs[i] = goList(ctx, abs, t)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	p := &LayoutPlan{Root: abs, Module: mf.module, GoVersion: mf.goVersion}
	mains := map[string]*MainPkg{}
	mainFiles := map[string][]string{}
	cgo := map[string]bool{}
	var pkgErrs []string
	for i, pkgs := range listings {
		for _, lp := range pkgs {
			if lp.Standard {
				continue
			}
			own := !lp.DepOnly && lp.Module != nil && lp.Module.Path == mf.module
			if own && lp.Error != nil && !strings.Contains(lp.Error.Err, "build constraints exclude all Go files") {
				pkgErrs = append(pkgErrs, fmt.Sprintf("%s (%s): %s", lp.ImportPath, targets[i], strings.TrimSpace(lp.Error.Err)))
			}
			for _, f := range lp.CgoFiles {
				cgo[cgoFileName(abs, lp, own, f)] = true
			}
			if !own || lp.Name != "main" {
				continue
			}
			if _, ok := mains[lp.ImportPath]; !ok {
				dir, err := relDir(abs, lp.Dir)
				if err != nil {
					return nil, err
				}
				mains[lp.ImportPath] = &MainPkg{ImportPath: lp.ImportPath, Dir: dir, BinName: binName(lp.ImportPath)}
			}
			if len(mainFiles[lp.ImportPath]) == 0 {
				for _, f := range append(slices.Clone(lp.GoFiles), lp.CgoFiles...) {
					mainFiles[lp.ImportPath] = append(mainFiles[lp.ImportPath], filepath.Join(lp.Dir, f))
				}
			}
		}
	}
	if len(pkgErrs) > 0 {
		sort.Strings(pkgErrs)
		pkgErrs = slices.Compact(pkgErrs)
		return nil, fmt.Errorf("layout: the module does not list cleanly; fix these first:\n  %s", strings.Join(pkgErrs, "\n  "))
	}
	if len(mains) == 0 {
		return nil, fmt.Errorf("layout: module %s has no package main, so there is no binary to install or ship", mf.module)
	}

	bins := map[string]string{}
	for _, ip := range sortedKeys(mains) {
		m := mains[ip]
		if !safePath.MatchString(m.Dir) || !safePath.MatchString(m.BinName) {
			return nil, fmt.Errorf("layout: package %s: directory %q or binary name %q has characters this generator will not put in a shell script", ip, m.Dir, m.BinName)
		}
		if other, dup := bins[m.BinName]; dup {
			return nil, fmt.Errorf("layout: %s and %s both build a binary named %q; they would overwrite each other in one archive and in GOBIN", other, ip, m.BinName)
		}
		bins[m.BinName] = ip
		ok, why := versionVar(mainFiles[ip])
		m.VersionVar = ok
		if !ok {
			p.Notes = append(p.Notes, fmt.Sprintf("%s: -X main.version has no effect because %s. Use `var version = \"dev\"` to stamp release builds.", m.Dir, why))
		}
		p.Mains = append(p.Mains, *m)
	}

	p.CgoFiles = sortedKeys(cgo)
	p.UsesCgo = len(p.CgoFiles) > 0
	p.Targets = targets
	if p.UsesCgo {
		p.Targets = []Target{host}
		p.Notes = append(p.Notes, fmt.Sprintf("cgo is used (%s), so CGO_ENABLED=0 cross builds would fail or change behavior. Only the host target %s is planned; other platforms need a native runner with a C toolchain each.",
			strings.Join(p.CgoFiles, ", "), host))
	}
	if mf.hasReplace {
		p.Notes = append(p.Notes, "go.mod has replace or exclude directives. `go install module/cmd@version` refuses such modules, so remote installs fail until they are removed.")
	}
	if first, _, _ := strings.Cut(mf.module, "/"); !strings.Contains(first, ".") {
		p.Notes = append(p.Notes, fmt.Sprintf("module path %q does not start with a domain, so `go install %s/...@latest` cannot fetch it. Only local `go install ./...` works.", mf.module, mf.module))
	}
	if !ignoresDist(abs) {
		p.Notes = append(p.Notes, "scripts/build-all.sh writes to dist/, which .gitignore does not ignore. Add `/dist/` so archives are not committed.")
	}
	p.Notes = append(p.Notes, "`go install ...@latest` builds from source and does not apply -ldflags. Those binaries report their version through runtime/debug.ReadBuildInfo, not main.version.")

	p.Files = []PlannedFile{
		{Path: ".github/workflows/release.yml", Content: renderWorkflow(p), Mode: 0o644},
		{Path: "scripts/build-all.sh", Content: renderBuildScript(p), Mode: 0o755},
		{Path: "docs/INSTALL.md", Content: renderInstall(p), Mode: 0o644},
	}
	for i := range p.Files {
		if _, err := os.Lstat(filepath.Join(abs, filepath.FromSlash(p.Files[i].Path))); err == nil {
			p.Files[i].Exists = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("layout: %s: %w", p.Files[i].Path, err)
		}
	}
	return p, nil
}

// ParseTargets reads "linux/amd64,darwin/arm64". It checks the shape only; Plan checks the pairs
// against the installed toolchain.
func ParseTargets(s string) ([]Target, error) {
	var out []Target
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		goos, goarch, ok := strings.Cut(part, "/")
		if !ok || !isWord(goos) || !isWord(goarch) {
			return nil, fmt.Errorf("layout: target %q is not GOOS/GOARCH", part)
		}
		out = append(out, Target{goos, goarch})
	}
	if len(out) == 0 {
		return nil, errors.New("layout: no targets given")
	}
	return out, nil
}

func isWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func dedupeTargets(ts []Target) []Target {
	var out []Target
	for _, t := range ts {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// validateTargets refuses pairs the installed toolchain cannot build.
func validateTargets(ctx context.Context, dir string, ts []Target) error {
	for _, t := range ts {
		if !isWord(t.GOOS) || !isWord(t.GOARCH) {
			return fmt.Errorf("layout: target %q is not GOOS/GOARCH", t)
		}
	}
	cmd := exec.CommandContext(ctx, "go", "tool", "dist", "list")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("layout: go tool dist list: %w: %s", err, tail(stderr.String(), 5))
	}
	known := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		known[line] = true
	}
	var bad []string
	for _, t := range ts {
		if !known[t.String()] {
			bad = append(bad, t.String())
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("layout: the Go toolchain does not support %s (see `go tool dist list`)", strings.Join(bad, ", "))
	}
	return nil
}

type listPkg struct {
	ImportPath string
	Dir        string
	Name       string
	Standard   bool
	DepOnly    bool
	Module     *struct{ Path string }
	GoFiles    []string
	CgoFiles   []string
	Error      *struct{ Err string }
}

// goList lists the module's packages and their non-test dependencies as the target sees them.
// CGO_ENABLED=1 makes go list report the files that import "C" for that target; it needs no C compiler.
func goList(ctx context.Context, root string, t Target) ([]listPkg, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-deps",
		"-json=ImportPath,Dir,Name,Standard,DepOnly,Module,GoFiles,CgoFiles,Error", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+t.GOOS, "GOARCH="+t.GOARCH, "CGO_ENABLED=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("layout: go list for %s: %w: %s", t, err, tail(stderr.String(), 10))
	}
	var pkgs []listPkg
	dec := json.NewDecoder(&stdout)
	for {
		var lp listPkg
		if err := dec.Decode(&lp); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("layout: go list for %s: bad JSON: %w", t, err)
		}
		pkgs = append(pkgs, lp)
	}
	return pkgs, nil
}

func cgoFileName(root string, lp listPkg, own bool, file string) string {
	if own {
		if rel, err := relDir(root, lp.Dir); err == nil {
			return path.Join(rel, file)
		}
	}
	return lp.ImportPath + "/" + file + " (dependency)"
}

func relDir(root, dir string) (string, error) {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("layout: package directory %s is outside the module root %s", dir, root)
	}
	return filepath.ToSlash(rel), nil
}

var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// binName is the file name `go install` gives the binary: the last path element, skipping a /vN major suffix.
func binName(importPath string) string {
	elems := strings.Split(importPath, "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && majorSuffix.MatchString(name) {
		name = elems[len(elems)-2]
	}
	return name
}

// versionVar reports whether the package declares a string var `version` that -X can set, and why not otherwise.
func versionVar(files []string) (bool, string) {
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
				continue
			}
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if n.Name != "version" {
						continue
					}
					if gd.Tok == token.CONST {
						return false, "version is a const"
					}
					typed := false
					if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "string" {
						typed = true
					}
					if len(vs.Values) == 0 {
						if typed {
							return true, ""
						}
						return false, "version is not a string var"
					}
					if i < len(vs.Values) {
						if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING && (typed || vs.Type == nil) {
							return true, ""
						}
					}
					return false, "version is not initialized to a plain string literal"
				}
			}
		}
	}
	return false, "the package declares no `version` variable"
}

type goModFile struct {
	module     string
	goVersion  string
	hasReplace bool
}

// parseGoMod reads the few go.mod directives the plan needs.
func parseGoMod(data []byte) (goModFile, error) {
	var mf goModFile
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "module":
			if len(fields) != 2 {
				return mf, fmt.Errorf("malformed module line %q", sc.Text())
			}
			mod := fields[1]
			if uq, err := strconv.Unquote(mod); err == nil {
				mod = uq
			}
			mf.module = mod
		case "go":
			if len(fields) == 2 {
				mf.goVersion = fields[1]
			}
		case "replace", "exclude":
			mf.hasReplace = true
		}
	}
	if err := sc.Err(); err != nil {
		return mf, err
	}
	if mf.module == "" {
		return mf, errors.New("no module directive")
	}
	return mf, nil
}

// ignoresDist reports whether the root .gitignore has a plain dist rule.
func ignoresDist(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(l) {
		case "dist", "dist/", "/dist", "/dist/":
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// tail returns the last n non-empty lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
