package analyze

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// PythonSource analyzes a Python source tree with Python's own parser. It runs the interpreter
// on an embedded script that only parses (ast.parse); no analyzed code is imported or run.
type PythonSource struct {
	// Python is the interpreter to run; empty means python3 from PATH.
	Python string
	// Timeout bounds the parser run; zero means two minutes.
	Timeout time.Duration
}

// pyReport is the document pyScript prints.
type pyReport struct {
	Python string   `json:"python"`
	Files  []pyFile `json:"files"`
	Tests  []string `json:"tests"`
}

type pyFile struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Error *struct {
		Line int    `json:"line"`
		Msg  string `json:"msg"`
	} `json:"error"`
	Imports []pyImport `json:"imports"`
	Funcs   []pyFunc   `json:"funcs"`
	Risks   []pyRisk   `json:"risks"`
}

type pyImport struct {
	Kind   string      `json:"kind"` // import or from
	Module string      `json:"module"`
	Level  int         `json:"level"`
	Names  [][2]string `json:"names"` // name and asname ("" when none)
	Line   int         `json:"line"`
}

type pyFunc struct {
	Name   string   `json:"name"`
	Line   int      `json:"line"`
	Params string   `json:"params"`
	Calls  []string `json:"calls"`
}

type pyRisk struct {
	Kind   string `json:"kind"`
	Line   int    `json:"line"`
	Detail string `json:"detail"`
}

// maxStderr bounds the interpreter error text kept for the error message.
const maxStderr = 4 << 10

// Analyze parses every .py file under root, outside the folders skipDir names, and maps each
// module to one domain.Package.
func (s PythonSource) Analyze(root string) (domain.Analysis, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if info, err := os.Stat(abs); err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	} else if !info.IsDir() {
		return domain.Analysis{}, fmt.Errorf("analyze: %s is not a directory", abs)
	}
	rep, err := s.run(abs)
	if err != nil {
		return domain.Analysis{}, err
	}
	if len(rep.Files) == 0 {
		return domain.Analysis{}, fmt.Errorf("analyze: no Python modules under %s (test files are not modules); supported: %s", abs, SupportedLanguages)
	}
	return buildPython(abs, rep), nil
}

// run executes the parser script. The root goes in argv, never into the code.
func (s PythonSource) run(abs string) (pyReport, error) {
	bin := s.Python
	if bin == "" {
		bin = "python3"
	}
	exe, err := exec.LookPath(bin)
	if err != nil {
		return pyReport{}, fmt.Errorf("analyze: %s not found (%v); Python sources are parsed with Python's own parser, so install Python 3.9 or later or put it on PATH", bin, err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-I", "-c", pyScript, abs)
	var stdout bytes.Buffer
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	cmd.Dir = abs
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return pyReport{}, fmt.Errorf("analyze: the Python parser did not finish within %s on %s", timeout, abs)
	}
	if err != nil {
		return pyReport{}, fmt.Errorf("analyze: the Python parser failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var rep pyReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		return pyReport{}, fmt.Errorf("analyze: the Python parser printed no valid JSON: %w", err)
	}
	return rep, nil
}

// tailBuffer keeps the last max bytes written, so a noisy interpreter cannot exhaust memory.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// pyUnit is one Python module with what resolution needs.
type pyUnit struct {
	*unitData
	file   pyFile
	isInit bool
	// pkgSegs is the package the module belongs to, for relative imports.
	pkgSegs []string
	// scriptDir is the dotted folder of a module outside any package, whose siblings Python
	// finds when the module runs as a script; "" for a module inside a package.
	scriptDir string
	funcs     map[string]bool
	// bindings maps a local name to the dotted name it stands for after an import.
	bindings map[string]string
	// stars are the repo modules imported with "from m import *".
	stars []*pyUnit
}

// pyImportRisks classify imports that leave the repo. A name matches an entry when it equals it
// or starts with it followed by a dot. Pure modules such as urllib.parse, http.HTTPStatus or
// importlib.metadata are deliberately absent.
var pyImportRisks = []struct{ name, kind string }{
	{"subprocess", domain.RiskSideEffects}, {"pty", domain.RiskSideEffects},
	{"threading", domain.RiskConcurrency}, {"_thread", domain.RiskConcurrency},
	{"multiprocessing", domain.RiskConcurrency}, {"asyncio", domain.RiskConcurrency},
	{"concurrent.futures", domain.RiskConcurrency},
	{"importlib.import_module", domain.RiskDynamicCode}, {"importlib.reload", domain.RiskDynamicCode},
	{"importlib.util", domain.RiskDynamicCode}, {"importlib.machinery", domain.RiskDynamicCode},
	{"runpy", domain.RiskDynamicCode},
	{"socket", domain.RiskNetwork}, {"ssl", domain.RiskNetwork}, {"socketserver", domain.RiskNetwork},
	{"http.client", domain.RiskNetwork}, {"http.server", domain.RiskNetwork},
	{"urllib.request", domain.RiskNetwork}, {"urllib3", domain.RiskNetwork},
	{"requests", domain.RiskNetwork}, {"httpx", domain.RiskNetwork}, {"aiohttp", domain.RiskNetwork},
	{"ftplib", domain.RiskNetwork}, {"smtplib", domain.RiskNetwork}, {"poplib", domain.RiskNetwork},
	{"imaplib", domain.RiskNetwork}, {"xmlrpc", domain.RiskNetwork}, {"websockets", domain.RiskNetwork},
	{"ctypes", domain.RiskNativeCode}, {"cffi", domain.RiskNativeCode},
	{"pickle", domain.RiskUnsafeDeserialize}, {"_pickle", domain.RiskUnsafeDeserialize},
	{"cPickle", domain.RiskUnsafeDeserialize}, {"dill", domain.RiskUnsafeDeserialize},
	{"shelve", domain.RiskUnsafeDeserialize}, {"marshal", domain.RiskUnsafeDeserialize},
	{"random", domain.RiskNondeterminism}, {"secrets", domain.RiskNondeterminism},
}

// pyImportRisk returns the risk kind of an imported module or name, or "".
func pyImportRisk(name string) string {
	for _, r := range pyImportRisks {
		if name == r.name || strings.HasPrefix(name, r.name+".") {
			return r.kind
		}
	}
	return ""
}

// buildPython names the modules, resolves imports and calls inside the repo and fills the analysis.
func buildPython(abs string, rep pyReport) domain.Analysis {
	rootName := filepath.Base(abs)
	initDirs := map[string]bool{}
	for _, f := range rep.Files {
		if path.Base(f.Path) == "__init__.py" {
			initDirs[path.Dir(f.Path)] = true
		}
	}
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })

	registry := map[string]*pyUnit{}
	var units []*pyUnit
	for _, f := range rep.Files {
		dir := path.Dir(f.Path)
		stem := strings.TrimSuffix(path.Base(f.Path), ".py")
		isInit := stem == "__init__"
		var chain []string
		for d := dir; initDirs[d]; d = path.Dir(d) {
			if d == "." {
				chain = append([]string{rootName}, chain...)
				break
			}
			chain = append([]string{path.Base(d)}, chain...)
		}
		var dirParts []string
		if dir != "." {
			dirParts = strings.Split(dir, "/")
		}
		full := append(append([]string{}, dirParts...), stem)
		if isInit {
			full = append([]string{}, dirParts...)
			if len(full) == 0 {
				full = []string{rootName}
			}
		}
		var segs []string
		switch {
		case len(chain) > 0 && isInit:
			segs = chain
		case len(chain) > 0:
			segs = append(chain, stem)
		case len(full) > 1 && full[0] == "src":
			segs = full[1:] // a src layout puts src/ on sys.path
		default:
			segs = full
		}
		name := strings.Join(segs, ".")
		if registry[name] != nil {
			segs, name = full, strings.Join(full, ".")
		}
		if registry[name] != nil {
			segs, name = strings.Split(strings.TrimSuffix(f.Path, ".py"), "/"), strings.TrimSuffix(f.Path, ".py")
		}
		u := &pyUnit{
			unitData: &unitData{
				pkg: &domain.Package{
					ImportPath: name, Dir: dir, Files: []string{path.Base(f.Path)}, Lines: f.Lines,
				},
				segments:  segs,
				importPos: map[string]string{},
			},
			file: f, isInit: isInit, funcs: map[string]bool{}, bindings: map[string]string{},
		}
		u.pkgSegs = segs
		if !isInit {
			u.pkgSegs = segs[:len(segs)-1]
		}
		if len(chain) == 0 {
			u.scriptDir = strings.Join(dirParts, ".")
		}
		for _, fn := range f.Funcs {
			u.funcs[fn.Name] = true
		}
		registry[name] = u
		units = append(units, u)
	}
	// The full folder path is an alias, so "import src.shop.price" also resolves.
	for _, u := range units {
		dir := strings.TrimSuffix(u.file.Path, ".py")
		if u.isInit {
			dir = path.Dir(u.file.Path)
		}
		if alias := strings.ReplaceAll(dir, "/", "."); alias != "." && registry[alias] == nil {
			registry[alias] = u
		}
	}

	lookup := func(from *pyUnit, name string) *pyUnit {
		if u := registry[name]; u != nil {
			return u
		}
		if from.scriptDir != "" {
			return registry[from.scriptDir+"."+name]
		}
		return nil
	}
	// longest finds the repo module that is the longest dotted prefix of name and the segments left.
	longest := func(from *pyUnit, name string) (*pyUnit, []string) {
		parts := strings.Split(name, ".")
		for i := len(parts); i > 0; i-- {
			if u := lookup(from, strings.Join(parts[:i], ".")); u != nil {
				return u, parts[i:]
			}
		}
		return nil, nil
	}

	tests := map[string]int{}
	for _, t := range rep.Tests {
		stem := strings.TrimSuffix(path.Base(t), ".py")
		switch {
		case strings.HasPrefix(stem, "test_"):
			tests[strings.TrimPrefix(stem, "test_")]++
		case strings.HasSuffix(stem, "_test"):
			tests[strings.TrimSuffix(stem, "_test")]++
		}
	}

	failed := 0
	for _, u := range units {
		f, p := u.file, u.pkg
		p.Tests = tests[u.segments[len(u.segments)-1]]
		pos := func(line int) string { return f.Path + ":" + strconv.Itoa(line) }
		if f.Error != nil {
			failed++
			if f.Error.Line > 0 {
				p.TypeErrors = []string{pos(f.Error.Line) + ": " + f.Error.Msg}
			} else {
				p.TypeErrors = []string{f.Path + ": " + f.Error.Msg}
			}
			continue
		}
		// One site per kind and imported module: ten names taken from ctypes are one fact.
		seenRisk := map[string]bool{}
		importRisk := func(name string, line int, detail string) {
			kind := pyImportRisk(name)
			module, _, _ := strings.Cut(name, ".")
			if kind == "" || seenRisk[kind+" "+module] {
				return
			}
			seenRisk[kind+" "+module] = true
			u.risk(kind, pos(line), detail)
		}
		for _, imp := range f.Imports {
			switch imp.Kind {
			case "import":
				for _, n := range imp.Names {
					if t, _ := longest(u, n[0]); t != nil {
						u.addImport(t.pkg.ImportPath, pos(imp.Line))
					} else {
						importRisk(n[0], imp.Line, "imports "+n[0])
					}
					if n[1] != "" {
						u.bindings[n[1]] = n[0]
					} else {
						first := strings.Split(n[0], ".")[0]
						u.bindings[first] = first
					}
				}
			case "from":
				base := imp.Module
				if imp.Level > 0 {
					up := imp.Level - 1
					if up > len(u.pkgSegs) {
						continue // beyond the top-level package; Python would fail too
					}
					segs := append([]string{}, u.pkgSegs[:len(u.pkgSegs)-up]...)
					if imp.Module != "" {
						segs = append(segs, imp.Module)
					}
					base = strings.Join(segs, ".")
				}
				baseUnit, _ := longest(u, base)
				for _, n := range imp.Names {
					local := n[1]
					if local == "" {
						local = n[0]
					}
					if n[0] == "*" {
						if t := lookup(u, base); t != nil {
							u.addImport(t.pkg.ImportPath, pos(imp.Line))
							u.stars = append(u.stars, t)
						} else if baseUnit == nil && imp.Level == 0 {
							importRisk(base, imp.Line, "from "+base+" import *")
						}
						continue
					}
					full := base + "." + n[0]
					if base == "" {
						full = n[0]
					}
					if t := lookup(u, full); t != nil {
						u.addImport(t.pkg.ImportPath, pos(imp.Line))
					} else if baseUnit != nil {
						u.addImport(baseUnit.pkg.ImportPath, pos(imp.Line))
					} else if imp.Level == 0 {
						if pyImportRisk(full) != "" {
							importRisk(full, imp.Line, "from "+base+" import "+n[0])
						} else {
							importRisk(base, imp.Line, "from "+base+" import "+n[0])
						}
					}
					u.bindings[local] = full
				}
			}
		}
		for _, r := range f.Risks {
			u.risk(r.Kind, pos(r.Line), r.Detail)
		}
	}

	// Functions and their callees, now that every module's bindings are known.
	for _, u := range units {
		p := u.pkg
		for _, fn := range u.file.Funcs {
			p.Funcs = appendUnique(p.Funcs, fn.Name)
			exported := !strings.HasPrefix(fn.Name, "_")
			if exported {
				p.Exported = appendUnique(p.Exported, fn.Name)
			}
			info := domain.FuncInfo{
				Name: fn.Name, Signature: fn.Params, Pos: u.file.Path + ":" + strconv.Itoa(fn.Line), Exported: exported,
			}
			self := domain.FuncID(p.ImportPath, "", fn.Name)
			callees := map[string]bool{}
			for _, call := range fn.Calls {
				if id := resolvePyCall(u, call, longest); id != "" && id != self {
					callees[id] = true
				}
			}
			for id := range callees {
				info.Callees = append(info.Callees, id)
			}
			sort.Strings(info.Callees)
			if !hasFunc(p.Functions, fn.Name) { // a redefinition keeps the first position
				p.Functions = append(p.Functions, info)
			}
		}
	}

	out := domain.Analysis{Root: filepath.ToSlash(abs), Module: rootName, Language: LangPython}
	data := make([]*unitData, len(units))
	for i, u := range units {
		data[i] = u.unitData
	}
	finishOriginal(&out, data)
	out.Limits = pythonLimits(rep.Python, failed)
	return out
}

// resolvePyCall turns a dotted call name into the FuncID of a repo function, or "".
func resolvePyCall(u *pyUnit, call string, longest func(*pyUnit, string) (*pyUnit, []string)) string {
	first, rest, dotted := strings.Cut(call, ".")
	if target, ok := u.bindings[first]; ok {
		name := target
		if dotted {
			name += "." + rest
		}
		m, left := longest(u, name)
		if m != nil && len(left) == 1 && m.funcs[left[0]] {
			return domain.FuncID(m.pkg.ImportPath, "", left[0])
		}
		return ""
	}
	if dotted {
		return ""
	}
	if u.funcs[call] {
		return domain.FuncID(u.pkg.ImportPath, "", call)
	}
	for _, s := range u.stars {
		if s.funcs[call] && !strings.HasPrefix(call, "_") {
			return domain.FuncID(s.pkg.ImportPath, "", call)
		}
	}
	return ""
}

func hasFunc(fs []domain.FuncInfo, name string) bool {
	for _, f := range fs {
		if f.Name == name {
			return true
		}
	}
	return false
}

func pythonLimits(version string, failed int) []string {
	out := []string{
		"Parsed with Python's own ast module (Python " + version + "); no analyzed code was imported or run.",
		"Functions are the top-level def statements only; classes and their methods are not listed.",
		"Callees are direct calls to top-level functions of the same module and to functions imported from repo modules. Method calls and calls through objects are not followed, so Callers is a lower bound.",
		"Imports resolve against the repo's own modules by package layout (__init__.py), with a leading src/ dropped; sys.path changes and imports built at run time are not followed.",
		"Risks come from imports and a fixed list of calls (eval, exec, os.system, open for writing, time.time, datetime.now and the like); a call made through another name is missed.",
		"A module counts the test files named test_<name>.py or <name>_test.py anywhere in the repo.",
	}
	if failed > 0 {
		out = append(out, fmt.Sprintf("%d %s did not parse; %s line counts are kept, but functions, imports and risks are missing.",
			failed, pluralWord(failed, "file", "files"), pluralWord(failed, "its", "their")))
	}
	return out
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
