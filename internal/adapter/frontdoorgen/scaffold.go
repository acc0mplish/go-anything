// Package frontdoorgen writes the Go front door of a program written in any language: a Go module that
// owns the entry point from day one while every request still reaches the original. Units then move to
// Go one at a time with wire gen, which mounts them into the generated main.go. It never overwrites.
package frontdoorgen

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/go-anything/go-anything/internal/domain"
)

// Defaults for Options.
const (
	DefaultPortEnv = "PORT"
	DefaultListen  = "127.0.0.1:8080"
	DefaultModule  = "app"
)

// Front door kinds, named after the recorded case kinds that select them.
const (
	KindServer = "http"
	KindCLI    = "cli"
)

// ErrExists is returned when the front door directory already exists. It is never overwritten.
var ErrExists = errors.New("frontdoorgen: front door already exists")

// ErrMixedKinds is returned when a recording holds both HTTP and CLI cases.
var ErrMixedKinds = errors.New("frontdoorgen: recording mixes http and cli cases")

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	modulePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*(/[A-Za-z0-9][A-Za-z0-9._~-]*)*$`)
)

// Options describe the front door to write.
type Options struct {
	// Cases are the recorded cases; their kind decides between a server and a CLI front door.
	Cases []domain.AppCase
	// OriginalCmd is the command that runs the original, split with SplitWords. For a CLI it is the
	// program plus fixed leading arguments; each invocation's arguments are appended.
	OriginalCmd string
	// PortEnv names the variable that tells a server original its port. Empty means DefaultPortEnv.
	PortEnv string
	// Listen is where a server front door listens. Empty means DefaultListen.
	Listen string
	// Module is the Go module path of the front door. Empty means DefaultModule.
	Module string
}

// Result describes a written front door.
type Result struct {
	Dir      string   // <repo>/go/app
	Kind     string   // KindServer or KindCLI
	Module   string   // Go module path
	Listen   string   // server only: the listen address written into main.go
	Original []string // the original command, already split
	Files    []string // written files, sorted
}

// Dir returns where the front door of repo is written.
func Dir(repo string) string { return filepath.Join(repo, "go", "app") }

// DetectKind returns the front door kind the cases need. Both kinds in one recording is an error that
// names the counts, because one front door serves one kind.
func DetectKind(cases []domain.AppCase) (string, error) {
	if len(cases) == 0 {
		return "", fmt.Errorf("frontdoorgen: the recording has no cases")
	}
	var nHTTP, nCLI int
	for _, c := range cases {
		switch c.Kind {
		case KindServer:
			nHTTP++
		case KindCLI:
			nCLI++
		default:
			return "", fmt.Errorf("frontdoorgen: case %q has unknown kind %q", c.Name, c.Kind)
		}
	}
	if nHTTP > 0 && nCLI > 0 {
		return "", fmt.Errorf("%w: %d http and %d cli cases; a front door serves one kind, so split the recording", ErrMixedKinds, nHTTP, nCLI)
	}
	if nHTTP > 0 {
		return KindServer, nil
	}
	return KindCLI, nil
}

// Scaffold writes the front door module at Dir(repo). It refuses when that directory exists, and it
// renders every file before it touches the disk, so a bad option writes nothing.
func Scaffold(repo string, opts Options) (Result, error) {
	kind, err := DetectKind(opts.Cases)
	if err != nil {
		return Result{}, err
	}
	argv, err := SplitWords(opts.OriginalCmd)
	if err != nil {
		return Result{}, err
	}
	if opts.PortEnv == "" {
		opts.PortEnv = DefaultPortEnv
	}
	if opts.Listen == "" {
		opts.Listen = DefaultListen
	}
	if opts.Module == "" {
		opts.Module = DefaultModule
	}
	if !envNamePattern.MatchString(opts.PortEnv) {
		return Result{}, fmt.Errorf("frontdoorgen: port variable %q is not an environment variable name", opts.PortEnv)
	}
	if err := validListen(opts.Listen); err != nil {
		return Result{}, err
	}
	if !modulePattern.MatchString(opts.Module) || strings.Contains(opts.Module, "..") {
		return Result{}, fmt.Errorf("frontdoorgen: module path %q is not valid", opts.Module)
	}
	files, err := render(kind, argv, opts)
	if err != nil {
		return Result{}, err
	}

	info, err := os.Stat(repo)
	if err != nil {
		return Result{}, fmt.Errorf("frontdoorgen: repo %s: %w", repo, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("frontdoorgen: repo %s is not a directory", repo)
	}
	dir := Dir(repo)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Result{}, fmt.Errorf("frontdoorgen: create %s: %w", filepath.Dir(dir), err)
	}
	// Mkdir, not MkdirAll: it fails when the directory exists, so two runs cannot both write into it.
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Result{}, fmt.Errorf("%w: %s exists and is never overwritten; move it aside to scaffold again", ErrExists, dir)
		}
		return Result{}, fmt.Errorf("frontdoorgen: create %s: %w", dir, err)
	}
	res := Result{Dir: dir, Kind: kind, Module: opts.Module, Original: argv}
	if kind == KindServer {
		res.Listen = opts.Listen
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(dir, name)
		if err := writeNew(p, files[name]); err != nil {
			// The directory was created above by this call, so removing it removes only what it wrote.
			os.RemoveAll(dir)
			return Result{}, err
		}
		res.Files = append(res.Files, p)
	}
	return res, nil
}

func validListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("frontdoorgen: listen address %q: %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("frontdoorgen: listen address %q needs a port from 1 to 65535", addr)
	}
	return nil
}

// render returns every file of the front door, formatted, keyed by file name.
func render(kind string, argv []string, opts Options) (map[string][]byte, error) {
	data := struct {
		Name, Module, OriginalCmd, PortEnv, Listen string
	}{
		Name:        filepath.Base(Dir("")),
		Module:      opts.Module,
		OriginalCmd: fmt.Sprintf("%#v", argv),
		PortEnv:     strconv.Quote(opts.PortEnv),
		Listen:      strconv.Quote(opts.Listen),
	}
	mainSrc := cliMain
	if kind == KindServer {
		mainSrc = serverMain
	}
	sources := map[string]string{
		"go.mod":        goModTemplate,
		"main.go":       mainSrc,
		"proc_linux.go": procLinux,
		"proc_other.go": procOther,
	}
	out := map[string][]byte{}
	for name, src := range sources {
		t, err := template.New(name).Option("missingkey=error").Parse(src)
		if err != nil {
			return nil, fmt.Errorf("frontdoorgen: template %s: %w", name, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("frontdoorgen: render %s: %w", name, err)
		}
		b := buf.Bytes()
		if strings.HasSuffix(name, ".go") {
			if b, err = format.Source(b); err != nil {
				return nil, fmt.Errorf("frontdoorgen: %s does not format: %w", name, err)
			}
		}
		out[name] = b
	}
	return out, nil
}

// writeNew creates path and fails if it exists.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("frontdoorgen: create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("frontdoorgen: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("frontdoorgen: close %s: %w", path, err)
	}
	return nil
}
