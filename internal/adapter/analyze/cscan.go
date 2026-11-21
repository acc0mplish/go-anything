package analyze

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// CSource analyzes a C source tree with a scanner written in Go: comments, string and character
// constants are blanked and preprocessor lines are dropped, then includes, function definitions
// and risk calls are found with patterns. There is no parser and no type checking;
// Analysis.Limits states what that misses.
type CSource struct{}

// maxCFile bounds the size of a scanned file; larger files are usually generated.
const maxCFile = 4 << 20

const cIdent = `[A-Za-z_][A-Za-z0-9_]*`

var (
	reCInclude  = regexp.MustCompile(`#[ \t]*include[ \t]*"([^"\n]+)"`)
	reCIdentPar = regexp.MustCompile(cIdent + `[ \t]*\(`)
	reCCall     = regexp.MustCompile(`(` + cIdent + `)[ \t]*\(`)
	reCPthread  = regexp.MustCompile(`\b(pthread_[A-Za-z0-9_]+)[ \t]*\(`)
	reCFork     = regexp.MustCompile(`\b(fork|vfork)[ \t]*\(`)
	reCProcess  = regexp.MustCompile(`\b(execlp|execle|execvpe|execvp|execve|execl|execv|exec|popen|system)[ \t]*\(`)
	reCFileOp   = regexp.MustCompile(`\b(fopen64|fopen|fdopen|remove|rename)[ \t]*\(`)
)

// cKeywords are never function names: control flow, operators and declarations.
var cKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "return": true,
	"sizeof": true, "else": true, "do": true, "case": true, "defined": true,
	"typedef": true, "asm": true, "_Alignof": true, "offsetof": true,
}

// Analyze scans every .c and .h file under root, outside the folders skipDir names. A module is
// one .c file; headers join a module (see buildC) and test files are counted, not modules.
func (CSource) Analyze(root string) (domain.Analysis, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if info, err := os.Stat(abs); err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	} else if !info.IsDir() {
		return domain.Analysis{}, fmt.Errorf("analyze: %s is not a directory", abs)
	}
	var rels, testRels []string
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == abs {
				return walkErr
			}
			return nil
		}
		if d.IsDir() {
			if p != abs && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".c") && !strings.HasSuffix(name, ".h") {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isCTestRel(rel) {
			testRels = append(testRels, rel)
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if len(rels) == 0 {
		return domain.Analysis{}, fmt.Errorf("analyze: no C source under %s (test files are not modules); supported: %s", abs, SupportedLanguages)
	}
	sort.Strings(rels)
	sort.Strings(testRels)
	return buildC(abs, rels, testRels), nil
}

// isCTestRel reports whether a file is a test: its path contains test (a tests folder, a test_
// prefix or a _test suffix) or its name ends _test.c.
func isCTestRel(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.Contains(strings.ToLower(seg), "test") {
			return true
		}
	}
	return false
}

// cFacts is what the scanner finds in one file.
type cFacts struct {
	includes []cInclude
	funcs    []cFunc
	risks    []cRisk
}

type cInclude struct {
	target string // the path as written between the quotes
	line   int
}

type cFunc struct {
	name     string
	line     int
	params   string
	exported bool // not static, so callable from other files
	calls    []string
}

type cRisk struct {
	kind, detail string
	line         int
}

// cFile is one scanned .c or .h file.
type cFile struct {
	rel   string
	lines int
	err   string
	facts cFacts
}

// cUnit is one module: a .c file with the headers that extend it.
type cUnit struct {
	*unitData
	files []*cFile
}

// scanC finds includes, function definitions and risk sites in one file.
func scanC(src []byte) cFacts {
	masked, includes := maskC(src)
	text := string(masked)
	depth := braceDepths(masked)
	lines := newLineIndex(masked)
	facts := cFacts{includes: includes}
	for _, m := range reCIdentPar.FindAllStringIndex(text, -1) {
		nameStart, parenAt := m[0], m[1]-1
		name := strings.TrimRight(text[nameStart:parenAt], " \t")
		if cKeywords[name] || depth[nameStart] != 0 {
			continue // a keyword or not at file scope
		}
		if nameStart > 0 {
			if prev := text[nameStart-1]; isJSIdentByte(prev) || prev == '.' || prev == '>' || prev == ':' {
				continue // a member call or a label; definitions start a statement
			}
		}
		p := nameStart // walk back over the return type to the start of the statement
		for p > 0 && isCTypeByte(text[p-1]) {
			p--
		}
		prefix := text[p:nameStart]
		close := matchClose(masked, parenAt, '(', ')')
		if close < 0 {
			continue
		}
		after := close + 1
		for after < len(text) && strings.ContainsRune(" \t\r\n", rune(text[after])) {
			after++
		}
		if after >= len(text) || text[after] != '{' {
			continue // a declaration, a call or a macro; only a body makes a definition
		}
		params := collapseParams(text[parenAt+1 : close])
		f := cFunc{name: name, line: lines.of(nameStart), params: params, exported: !wordIn(prefix, "static")}
		if bodyEnd := matchClose(masked, after, '{', '}'); bodyEnd > after {
			f.calls = cCallNames(text[after:bodyEnd])
		}
		facts.funcs = append(facts.funcs, f)
	}
	risk := func(kind string, at int, detail string) {
		facts.risks = append(facts.risks, cRisk{kind: kind, line: lines.of(at), detail: detail})
	}
	for _, m := range reCPthread.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "calls "+text[m[2]:m[3]])
	}
	for _, m := range reCFork.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "calls "+text[m[2]:m[3]])
	}
	for _, m := range reCProcess.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskSideEffects, m[0], "calls "+text[m[2]:m[3]])
	}
	for _, m := range reCFileOp.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskFSWrite, m[0], "calls "+text[m[2]:m[3]])
	}
	sort.SliceStable(facts.funcs, func(i, j int) bool { return facts.funcs[i].line < facts.funcs[j].line })
	return facts
}

// isCTypeByte is what a return type and its storage class are made of.
func isCTypeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == ' ' || c == '\t' || c == '\n' || c == '*'
}

// wordIn reports whether word appears in s as a whole word.
func wordIn(s, word string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(s)
}

// collapseParams turns a parameter list into one line, bounded like jsParams.
func collapseParams(p string) string {
	p = strings.Join(strings.Fields(p), " ")
	if len(p) > 120 {
		p = p[:117] + "..."
	}
	return "(" + p + ")"
}

// cCallNames lists the distinct names called in a body. The context before a name is checked by
// hand so that the parenthesis of "if (" does not hide the call that follows it.
func cCallNames(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reCCall.FindAllStringSubmatchIndex(body, -1) {
		name := body[m[2]:m[3]]
		if cKeywords[name] || seen[name] {
			continue
		}
		if m[0] > 0 {
			if prev := body[m[0]-1]; isJSIdentByte(prev) || prev == '.' || prev == '>' {
				continue // a member call through a struct or a pointer
			}
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// maskC blanks what patterns must not look into: comments, the contents of string and character
// literals, and whole preprocessor lines (line continuations included), keeping every offset and
// newline so positions match the source. While blanking it collects the #include "..." targets,
// which the mask removes; angle includes are system headers and never collected.
func maskC(src []byte) ([]byte, []cInclude) {
	out := append([]byte(nil), src...)
	n := len(src)
	blank := func(a, b int) {
		for k := max(a, 0); k < b && k < n; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	var includes []cInclude
	lineStart, line := true, 1
	i := 0
	for i < n {
		c := src[i]
		switch {
		case lineStart && (c == ' ' || c == '\t' || c == '\r'):
			i++
		case lineStart && c == '#':
			end := directiveEnd(src, i)
			if m := reCInclude.FindSubmatchIndex(src[i:end]); m != nil {
				includes = append(includes, cInclude{target: string(src[i+m[2] : i+m[3]]), line: line})
			}
			blank(i, end)
			line += strings.Count(string(src[i:end]), "\n")
			i, lineStart = end, false
		case c == '/' && i+1 < n && src[i+1] == '/':
			j := strings.IndexByte(string(src[i:]), '\n')
			if j < 0 {
				j = n - i
			}
			blank(i, i+j)
			i += j
			lineStart = false
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := strings.Index(string(src[i+2:]), "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			blank(i, end)
			line += strings.Count(string(src[i:end]), "\n")
			i = end
			lineStart = false
		case c == '"' || c == '\'':
			j := i + 1
			for j < n && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			blank(i+1, min(j, n))
			if j < n && src[j] == c {
				j++
			}
			i = j
			lineStart = false
		case c == '\n':
			line++
			lineStart = true
			i++
		default:
			lineStart = false
			i++
		}
	}
	return out, includes
}

// directiveEnd returns the offset of the end of the preprocessor directive that starts at start,
// following backslash line continuations; the closing newline is not part of it.
func directiveEnd(src []byte, start int) int {
	i := start
	for i < len(src) {
		eol := strings.IndexByte(string(src[i:]), '\n')
		if eol < 0 {
			return len(src)
		}
		lineEnd := i + eol
		trimmed := strings.TrimRight(string(src[i:lineEnd]), "\r")
		if strings.HasSuffix(trimmed, "\\") {
			i = lineEnd + 1
			continue
		}
		return lineEnd
	}
	return len(src)
}

// buildC turns the scanned files into the analysis. A module is one .c file; a .h file joins the
// module of the .c file with the same stem in its directory, else the directory's only .c file,
// else the one module that includes it, else it is a module of its own (a shared header).
func buildC(abs string, rels, testRels []string) domain.Analysis {
	tests := map[string]int{}
	for _, rel := range testRels {
		stem := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		stem = strings.TrimPrefix(stem, "test_")
		stem = strings.TrimSuffix(stem, "_test")
		tests[stem]++
	}
	files := map[string]*cFile{}
	failed := 0
	for _, rel := range rels {
		f := &cFile{rel: rel}
		data, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			f.err = rel + ": cannot read: " + err.Error()
			failed++
		case len(data) > maxCFile:
			f.lines = strings.Count(string(data), "\n")
			f.err = fmt.Sprintf("%s: not scanned, %d bytes is over the %d byte limit (generated code?)", rel, len(data), maxCFile)
			failed++
		default:
			f.lines = strings.Count(string(data), "\n")
			f.facts = scanC(data)
		}
		files[rel] = f
	}

	// resolve turns an include as written into the file it names: relative to the includer first
	// (the quote-include rule), then relative to the root (a common -I flag).
	resolve := func(fromRel, target string) string {
		for _, cand := range []string{path.Clean(path.Join(path.Dir(fromRel), target)), path.Clean(target)} {
			if cand == ".." || strings.HasPrefix(cand, "../") || path.IsAbs(cand) {
				continue
			}
			if files[cand] != nil {
				return cand
			}
		}
		return ""
	}
	includers := map[string]map[string]bool{} // header rel -> the .c files that include it directly
	for _, rel := range rels {
		for _, inc := range files[rel].facts.includes {
			if t := resolve(rel, inc.target); t != "" && t != rel && strings.HasSuffix(rel, ".c") {
				if includers[t] == nil {
					includers[t] = map[string]bool{}
				}
				includers[t][rel] = true
			}
		}
	}

	byPath := map[string]*cUnit{}
	var unitOrder []*cUnit
	newUnit := func(name string) *cUnit {
		if byPath[name] != nil {
			name = name + ".h" // a header next to a same-named module of another file
		}
		segs := strings.Split(name, "/")
		if len(segs) > 1 && segs[0] == "src" {
			segs = segs[1:]
		}
		u := &cUnit{unitData: &unitData{
			pkg:       &domain.Package{ImportPath: name, Dir: path.Dir(name)},
			segments:  segs,
			importPos: map[string]string{},
		}}
		byPath[name] = u
		unitOrder = append(unitOrder, u)
		return u
	}
	own := map[string]*cUnit{} // file rel -> the module it belongs to
	dirCounts := map[string]int{}
	for _, rel := range rels {
		if strings.HasSuffix(rel, ".c") {
			u := newUnit(strings.TrimSuffix(rel, ".c"))
			own[rel] = u
			dirCounts[path.Dir(rel)]++
		}
	}
	for _, rel := range rels {
		if !strings.HasSuffix(rel, ".h") {
			continue
		}
		stem := strings.TrimSuffix(rel, ".h")
		switch u := byPath[stem]; {
		case u != nil: // util.h extends util.c
			own[rel] = u
		case dirCounts[path.Dir(rel)] == 1: // the directory's only .c file
			for _, other := range rels {
				if strings.HasSuffix(other, ".c") && path.Dir(other) == path.Dir(rel) {
					own[rel] = own[other]
				}
			}
		default:
			var cand []*cUnit
			seen := map[*cUnit]bool{}
			for inc := range includers[rel] {
				if u := own[inc]; u != nil && !seen[u] {
					seen[u] = true
					cand = append(cand, u)
				}
			}
			if len(cand) == 1 { // exactly one .c includes it
				own[rel] = cand[0]
			} else {
				own[rel] = newUnit(stem) // a shared header or a header without a home
			}
		}
	}
	for _, rel := range rels {
		own[rel].files = append(own[rel].files, files[rel])
	}
	for _, u := range unitOrder {
		p := u.pkg
		p.Files = make([]string, 0, len(u.files))
		for _, f := range u.files {
			p.Files = append(p.Files, path.Base(f.rel))
			p.Lines += f.lines
			if f.err != "" {
				p.TypeErrors = append(p.TypeErrors, f.err)
			}
		}
		p.Tests = tests[path.Base(p.ImportPath)]
		for _, f := range u.files {
			for _, inc := range f.facts.includes {
				t := resolve(f.rel, inc.target)
				if t == "" || t == f.rel {
					continue
				}
				if target := own[t]; target != nil {
					u.addImport(target.pkg.ImportPath, f.rel+":"+strconv.Itoa(inc.line))
				}
			}
		}
	}

	// the module's own names, then the functions with their callees
	funcSets := map[string]map[string]bool{}
	for _, u := range unitOrder {
		set := map[string]bool{}
		for _, f := range u.files {
			for _, fn := range f.facts.funcs {
				set[fn.name] = true
			}
		}
		funcSets[u.pkg.ImportPath] = set
	}
	for _, u := range unitOrder {
		p := u.pkg
		for _, f := range u.files {
			pos := func(line int) string { return f.rel + ":" + strconv.Itoa(line) }
			for _, fn := range f.facts.funcs {
				p.Funcs = appendUnique(p.Funcs, fn.name)
				if fn.exported {
					p.Exported = appendUnique(p.Exported, fn.name)
				}
				if hasFunc(p.Functions, fn.name) {
					continue // a redefinition keeps the first position
				}
				info := domain.FuncInfo{Name: fn.name, Signature: fn.params, Pos: pos(fn.line), Exported: fn.exported}
				self := domain.FuncID(p.ImportPath, "", fn.name)
				callees := map[string]bool{}
				for _, call := range fn.calls {
					if funcSets[p.ImportPath][call] {
						if id := domain.FuncID(p.ImportPath, "", call); id != self {
							callees[id] = true
						}
						continue
					}
					for _, imp := range p.Imports {
						if funcSets[imp][call] {
							callees[domain.FuncID(imp, "", call)] = true
							break
						}
					}
				}
				for id := range callees {
					info.Callees = append(info.Callees, id)
				}
				sort.Strings(info.Callees)
				p.Functions = append(p.Functions, info)
			}
			for _, r := range f.facts.risks {
				u.risk(r.kind, pos(r.line), r.detail)
			}
		}
	}

	out := domain.Analysis{Root: filepath.ToSlash(abs), Module: filepath.Base(abs), Language: LangC}
	data := make([]*unitData, len(unitOrder))
	for i, u := range unitOrder {
		data[i] = u.unitData
	}
	finishOriginal(&out, data)
	out.Limits = cLimits(failed)
	return out
}

func cLimits(failed int) []string {
	out := []string{
		"Scanned with patterns after blanking comments, strings, character constants and preprocessor lines; there is no parser and no type checking, and nothing was compiled or run.",
		"A module is one .c file; a header joins the module of the .c file with the same stem in its directory, else the directory's only .c file, else the one module that includes it, else it is a module of its own.",
		"Functions are definitions at file scope: a name, a parameter list and a body in braces. K&R definitions, functions whose opening brace comes from a macro and code under unresolved conditionals are missed or misplaced.",
		"Imports are #include \"...\" lines that resolve to a scanned file, relative to the includer or to the root; angle includes are system headers and never imports, and includes that resolve nowhere (generated headers) are dropped.",
		"Callees are calls to names defined in the same module or in a module the file includes; calls through function pointers and macros are not followed, so Callers is a lower bound.",
		"Risks come from a fixed list of calls: pthread_* and fork/vfork (concurrency), popen, system and the exec family (side_effects), fopen, fopen64, fdopen, remove and rename (fs_write).",
		"A module counts the test files whose path contains test or whose name ends _test.c, attributed by the file name with test_ or _test stripped.",
	}
	if failed > 0 {
		out = append(out, fmt.Sprintf("%d %s could not be scanned; see type_errors.", failed, pluralWord(failed, "file", "files")))
	}
	return out
}
