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

// JavaSource analyzes a Java source tree with a scanner written in Go: comments, strings and text
// blocks are blanked first, then packages, imports, methods and risk sites are found with
// patterns. There is no parser and no type checking; Analysis.Limits states what that misses.
type JavaSource struct{}

// maxJavaFile bounds the size of a scanned file; larger files are usually generated.
const maxJavaFile = 4 << 20

const javaIdent = `[A-Za-z_$][\w$]*`

var (
	reJavaPackage  = regexp.MustCompile(`(?m)^[ \t]*package[ \t]+(` + javaIdent + `(?:\.` + javaIdent + `)*)[ \t]*;`)
	reJavaImport   = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+(static[ \t]+)?([\w$.]+)[ \t]*\*?[ \t]*;`)
	reJavaIdentPar = regexp.MustCompile(javaIdent + `[ \t]*\(`)
	reJavaCall     = regexp.MustCompile(`((?:` + javaIdent + `)(?:\.[ \t]*` + javaIdent + `)?)[ \t]*\(`)

	reJavaNewThread  = regexp.MustCompile(`\bnew[ \t\n]+Thread[ \t]*\(`)
	reJavaExtThread  = regexp.MustCompile(`\bextends[ \t\n]+Thread\b`)
	reJavaThreadCall = regexp.MustCompile(`\bThread[ \t]*\.[ \t]*` + javaIdent + `[ \t]*\(`)
	reJavaConcurType = regexp.MustCompile(`\b(ExecutorService|CompletableFuture)\b`)
	reJavaSync       = regexp.MustCompile(`\bsynchronized[ \t]*\(`)
	reJavaFilesCall  = regexp.MustCompile(`\bFiles[ \t]*\.[ \t]*(` + javaIdent + `)[ \t]*\(`)
	// Concrete java.io class names are suffixes (FileOutputStream holds OutputStream with no word
	// boundary before it), so the match allows identifier characters in front instead of \b.
	reJavaStreamType = regexp.MustCompile(`\b\w*(InputStream|OutputStream|RandomAccessFile|Reader|Writer)\b`)
	reJavaRuntimeEx  = regexp.MustCompile(`\bRuntime[ \t]*\.[ \t]*getRuntime[ \t]*\([ \t]*\)[ \t]*\.[ \t]*exec[ \t]*\(`)
	reJavaProcBuild  = regexp.MustCompile(`\bnew[ \t\n]+ProcessBuilder[ \t]*\(`)

	reJavaPublic  = regexp.MustCompile(`\bpublic\b`)
	reJavaSyncMod = regexp.MustCompile(`\bsynchronized\b`)
	reJavaSkipPre = regexp.MustCompile(`\b(?:new|record)\b`)
)

// javaKeywords are never method names in a call or a declaration.
var javaKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true,
	"new": true, "super": true, "throw": true, "else": true, "do": true, "assert": true,
	"synchronized": true, "sizeof": true, "case": true,
}

// Analyze scans every .java file under root, outside the folders skipDir names. Modules are the
// packages under src/main/java, or the packages of the whole tree when there is no src/main/java;
// files under src/test/java are tests and counted, not modules.
func (JavaSource) Analyze(root string) (domain.Analysis, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if info, err := os.Stat(abs); err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	} else if !info.IsDir() {
		return domain.Analysis{}, fmt.Errorf("analyze: %s is not a directory", abs)
	}
	hasMain := false
	if info, err := os.Stat(filepath.Join(abs, "src", "main", "java")); err == nil && info.IsDir() {
		hasMain = true
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
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".java") {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasPrefix(rel, "src/test/java/"):
			testRels = append(testRels, rel)
		case hasMain && !strings.HasPrefix(rel, "src/main/java/"):
			// a src/main/java tree is the program; anything else on it is not source
		default:
			rels = append(rels, rel)
		}
		return nil
	})
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if len(rels) == 0 {
		return domain.Analysis{}, fmt.Errorf("analyze: no Java source under %s (files under src/test/java are tests, not modules); supported: %s", abs, SupportedLanguages)
	}
	sort.Strings(rels)
	sort.Strings(testRels)
	return buildJava(abs, rels, testRels), nil
}

// javaFacts is what the scanner finds in one file.
type javaFacts struct {
	pkgName string
	hasPkg  bool
	imports []javaImport
	methods []javaFunc
	risks   []javaRisk
}

type javaImport struct {
	path   string // the dotted path as written, without static and without the star
	line   int
	static bool
	star   bool
}

type javaFunc struct {
	name     string
	line     int
	params   string
	exported bool
	calls    []string
}

type javaRisk struct {
	kind, detail string
	line         int
}

// javaFile is one scanned .java file.
type javaFile struct {
	rel   string
	lines int
	err   string
	facts javaFacts
}

// javaUnit is one module: a package with the files that declare it.
type javaUnit struct {
	*unitData
	files []*javaFile
	// binds maps a file's imported class simple name to the module of its package.
	binds map[string]map[string]*javaUnit
}

// scanJava finds the package, imports, methods and risk sites in one file.
func scanJava(src []byte) javaFacts {
	masked := maskJava(src)
	text := string(masked)
	depth := braceDepths(masked)
	lines := newLineIndex(masked)
	facts := javaFacts{}
	if m := reJavaPackage.FindStringSubmatchIndex(text); m != nil {
		facts.pkgName, facts.hasPkg = text[m[2]:m[3]], true
	}
	for _, m := range reJavaImport.FindAllStringSubmatchIndex(text, -1) {
		body := strings.TrimRight(text[m[4]:m[5]], " \t")
		imp := javaImport{line: lines.of(m[0]), static: m[2] >= 0}
		if strings.HasSuffix(body, "*") {
			imp.star = true
			body = strings.TrimSpace(strings.TrimSuffix(body, "*"))
		}
		imp.path = strings.TrimSpace(strings.TrimSuffix(body, "."))
		if imp.path != "" {
			facts.imports = append(facts.imports, imp)
		}
	}
	for _, m := range reJavaIdentPar.FindAllStringIndex(text, -1) {
		nameStart, parenAt := m[0], m[1]-1
		name := strings.TrimRight(text[nameStart:parenAt], " \t")
		if javaKeywords[name] || depth[nameStart] != 1 {
			continue // a keyword, or not directly inside a top-level type
		}
		if nameStart > 0 {
			if prev := text[nameStart-1]; isJSIdentByte(prev) || prev == '.' || prev == '>' || prev == ':' {
				continue
			}
		}
		p := nameStart // walk back over modifiers, the return type and its generics
		for p > 0 && isJavaDeclByte(text[p-1]) {
			p--
		}
		prefix := text[p:nameStart]
		if reJavaSkipPre.MatchString(prefix) {
			continue // new Foo(...) or a nested record header
		}
		close := matchClose(masked, parenAt, '(', ')')
		if close < 0 {
			continue
		}
		after := close + 1
		for after < len(text) && strings.ContainsRune(" \t\r\n", rune(text[after])) {
			after++
		}
		if strings.HasPrefix(text[after:], "throws") {
			after += len("throws")
			for after < len(text) && isJavaDeclByte(text[after]) {
				after++
			}
		}
		if after >= len(text) || text[after] != '{' {
			continue // a declaration without a body, a call or a header of a type
		}
		f := javaFunc{
			name: name, line: lines.of(nameStart), params: collapseParams(text[parenAt+1 : close]),
			exported: reJavaPublic.MatchString(prefix),
		}
		if reJavaSyncMod.MatchString(prefix) {
			facts.risks = append(facts.risks, javaRisk{kind: domain.RiskConcurrency, line: f.line, detail: "synchronized method"})
		}
		if bodyEnd := matchClose(masked, after, '{', '}'); bodyEnd > after {
			f.calls = javaCallNames(text[after:bodyEnd])
		}
		facts.methods = append(facts.methods, f)
	}

	// skipAt reports whether the match at at sits on an import or package line, whose type
	// names are mentions, not uses.
	skipAt := func(at int) bool {
		start := strings.LastIndexByte(text[:at], '\n') + 1
		trimmed := strings.TrimLeft(text[start:at], " \t")
		return strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "package ")
	}
	// one site per kind, line and detail: a declaration and the constructor on the same line are
	// one finding about the same thing.
	risk := func(kind string, at int, detail string) {
		if skipAt(at) {
			return
		}
		line := lines.of(at)
		for _, r := range facts.risks {
			if r.kind == kind && r.line == line && r.detail == detail {
				return
			}
		}
		facts.risks = append(facts.risks, javaRisk{kind: kind, line: line, detail: detail})
	}
	for _, m := range reJavaNewThread.FindAllStringIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "new Thread")
	}
	for _, m := range reJavaExtThread.FindAllStringIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "extends Thread")
	}
	for _, m := range reJavaThreadCall.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "Thread."+text[m[2]:m[3]])
	}
	for _, m := range reJavaConcurType.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "uses "+text[m[2]:m[3]])
	}
	for _, m := range reJavaSync.FindAllStringIndex(text, -1) {
		risk(domain.RiskConcurrency, m[0], "synchronized block")
	}
	for _, m := range reJavaFilesCall.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskFSWrite, m[0], "calls Files."+text[m[2]:m[3]])
	}
	for _, m := range reJavaStreamType.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskFSWrite, m[0], "uses "+text[m[2]:m[3]])
	}
	for _, m := range reJavaRuntimeEx.FindAllStringIndex(text, -1) {
		risk(domain.RiskSideEffects, m[0], "Runtime exec")
	}
	for _, m := range reJavaProcBuild.FindAllStringIndex(text, -1) {
		risk(domain.RiskSideEffects, m[0], "new ProcessBuilder")
	}
	sort.SliceStable(facts.methods, func(i, j int) bool { return facts.methods[i].line < facts.methods[j].line })
	return facts
}

// isJavaDeclByte is what modifiers, types, generics and throws clauses are made of.
func isJavaDeclByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '$' || c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
		c == '*' || c == '.' || c == ',' || c == '<' || c == '>' || c == '[' || c == ']' || c == '?' || c == '&'
}

// javaCallNames lists the distinct names called in a body: "name" or "Class.name". The
// context before a name is checked by hand so that the parenthesis of "if (" does not hide the
// call that follows it.
func javaCallNames(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reJavaCall.FindAllStringSubmatchIndex(body, -1) {
		name := strings.Join(strings.Fields(strings.ReplaceAll(body[m[2]:m[3]], ".", " . ")), "")
		if m[0] > 0 {
			if prev := body[m[0]-1]; isJSIdentByte(prev) || prev == '.' || prev == '>' {
				continue // a qualified or a member call through an object
			}
		}
		first, _, _ := strings.Cut(name, ".")
		if javaKeywords[first] || first == "this" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// maskJava blanks what patterns must not look into: comments, the contents of strings, text
// blocks and character constants, keeping every offset and newline. A string that does not close
// on its line ends there, so one stray quote cannot blank the rest of the file.
func maskJava(src []byte) []byte {
	out := append([]byte(nil), src...)
	n := len(src)
	blank := func(a, b int) {
		for k := max(a, 0); k < b && k < n; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	i := 0
	for i < n {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			j := strings.IndexByte(string(src[i:]), '\n')
			if j < 0 {
				j = n - i
			}
			blank(i, i+j)
			i += j
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := strings.Index(string(src[i+2:]), "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			blank(i, end)
			i = end
		case c == '"' && i+3 <= n && string(src[i:i+3]) == `"""`:
			j := i + 3
			for j < n {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if j+3 <= n && string(src[j:j+3]) == `"""` {
					break
				}
				j++
			}
			if j+3 <= n && string(src[j:j+3]) == `"""` {
				blank(i+3, j)
				i = j + 3
			} else {
				blank(i+3, n)
				i = n
			}
		case c == '"' || c == '\'':
			q := c
			j := i + 1
			for j < n && src[j] != q && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			blank(i+1, min(j, n))
			if j < n && src[j] == q {
				j++
			}
			i = j
		default:
			i++
		}
	}
	return out
}

// javaImportPackage names the in-repo package an import refers to: import a.b.C; and
// import a.b.*; name a.b, a static import names the package of the imported member's class.
func javaImportPackage(imp javaImport) string {
	segs := strings.Split(imp.path, ".")
	drop := 0
	if !imp.star {
		drop++
	}
	if imp.static && !imp.star {
		drop++
	}
	if drop >= len(segs) {
		return ""
	}
	return strings.Join(segs[:len(segs)-drop], ".")
}

// buildJava groups the files into packages, resolves imports and calls, and fills the analysis.
func buildJava(abs string, rels, testRels []string) domain.Analysis {
	tests := map[string]int{}
	for _, rel := range testRels {
		data, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if facts := scanJava(data); facts.hasPkg {
			tests[facts.pkgName]++
		}
	}
	byModule := map[string][]*javaFile{}
	var names []string
	for _, rel := range rels {
		f := &javaFile{rel: rel}
		data, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			f.err = rel + ": cannot read: " + err.Error()
		case len(data) > maxJavaFile:
			f.lines = strings.Count(string(data), "\n")
			f.err = fmt.Sprintf("%s: not scanned, %d bytes is over the %d byte limit (generated code?)", rel, len(data), maxJavaFile)
		default:
			f.lines = strings.Count(string(data), "\n")
			f.facts = scanJava(data)
		}
		name := f.facts.pkgName
		if !f.facts.hasPkg { // the default package falls back to the folder path
			name = strings.ReplaceAll(path.Dir(rel), "/", ".")
			if name == "." {
				name = "default"
			}
		}
		if _, ok := byModule[name]; !ok {
			names = append(names, name)
		}
		byModule[name] = append(byModule[name], f)
	}
	sort.Strings(names)

	byPath := map[string]*javaUnit{}
	var unitOrder []*javaUnit
	for _, name := range names {
		files := byModule[name]
		u := &javaUnit{
			unitData: &unitData{
				pkg:       &domain.Package{ImportPath: name, Dir: path.Dir(files[0].rel)},
				segments:  strings.Split(name, "."),
				importPos: map[string]string{},
			},
			files: files,
			binds: map[string]map[string]*javaUnit{},
		}
		byPath[name] = u
		unitOrder = append(unitOrder, u)
	}
	failed := 0
	for _, u := range unitOrder {
		p := u.pkg
		for _, f := range u.files {
			p.Files = append(p.Files, path.Base(f.rel))
			p.Lines += f.lines
			if f.err != "" {
				p.TypeErrors = append(p.TypeErrors, f.err)
				failed++
			}
			for _, imp := range f.facts.imports {
				target := byPath[javaImportPackage(imp)]
				if target == nil {
					continue
				}
				u.addImport(target.pkg.ImportPath, f.rel+":"+strconv.Itoa(imp.line))
				if !imp.star && !imp.static {
					if segs := strings.Split(imp.path, "."); len(segs) > 1 {
						if u.binds[f.rel] == nil {
							u.binds[f.rel] = map[string]*javaUnit{}
						}
						u.binds[f.rel][segs[len(segs)-1]] = target
					}
				}
			}
		}
		p.Tests = tests[p.ImportPath]
	}

	// the module's own names, then the methods with their callees
	funcSets := map[string]map[string]bool{}
	for _, u := range unitOrder {
		set := map[string]bool{}
		for _, f := range u.files {
			for _, m := range f.facts.methods {
				set[m.name] = true
			}
		}
		funcSets[u.pkg.ImportPath] = set
	}
	for _, u := range unitOrder {
		p := u.pkg
		for _, f := range u.files {
			pos := func(line int) string { return f.rel + ":" + strconv.Itoa(line) }
			for _, fn := range f.facts.methods {
				p.Funcs = appendUnique(p.Funcs, fn.name)
				if fn.exported {
					p.Exported = appendUnique(p.Exported, fn.name)
				}
				if hasFunc(p.Functions, fn.name) {
					continue // an overload or a same-named method keeps the first position
				}
				info := domain.FuncInfo{Name: fn.name, Signature: fn.params, Pos: pos(fn.line), Exported: fn.exported}
				self := domain.FuncID(p.ImportPath, "", fn.name)
				callees := map[string]bool{}
				for _, call := range fn.calls {
					first, member, dotted := strings.Cut(call, ".")
					if !dotted {
						if funcSets[p.ImportPath][call] && call != fn.name {
							callees[domain.FuncID(p.ImportPath, "", call)] = true
						}
						continue
					}
					if t := u.binds[f.rel][first]; t != nil && funcSets[t.pkg.ImportPath][member] {
						callees[domain.FuncID(t.pkg.ImportPath, "", member)] = true
					}
				}
				for id := range callees {
					if id != self {
						info.Callees = append(info.Callees, id)
					}
				}
				sort.Strings(info.Callees)
				p.Functions = append(p.Functions, info)
			}
			for _, r := range f.facts.risks {
				u.risk(r.kind, pos(r.line), r.detail)
			}
		}
	}

	out := domain.Analysis{Root: filepath.ToSlash(abs), Module: filepath.Base(abs), Language: LangJava}
	data := make([]*unitData, len(unitOrder))
	for i, u := range unitOrder {
		data[i] = u.unitData
	}
	finishOriginal(&out, data)
	out.Limits = javaLimits(failed)
	return out
}

func javaLimits(failed int) []string {
	out := []string{
		"Scanned with patterns after blanking comments, strings, text blocks and character constants; there is no parser and no type checking, and nothing was compiled or run.",
		"A module is one package: the packages under src/main/java, or of the whole tree when there is no src/main/java. Files under src/test/java are tests and files outside a src/main/java tree are ignored when it exists.",
		"Methods are definitions directly inside a top-level type: a name, a parameter list, an optional throws clause and a body in braces. Methods of nested, local and anonymous types and bodyless declarations are not listed; same-named methods and overloads share one entry.",
		"Imports resolve by package name: import a.b.C; and import a.b.*; name the package a.b, a static import the package of the class. Packages the repo does not declare count as outside it.",
		"Callees are calls to methods of the same package and to Class.method where Class is imported from a repo package; calls through objects, this and super are not followed, so Callers is a lower bound.",
		"Risks come from a fixed list: new Thread, extends Thread, Thread.x(...), ExecutorService, CompletableFuture and synchronized (concurrency); Files.x(...), InputStream, OutputStream and RandomAccessFile (fs_write); Runtime.getRuntime().exec and new ProcessBuilder (side_effects).",
		"A package counts the test files under src/test/java that declare it.",
	}
	if failed > 0 {
		out = append(out, fmt.Sprintf("%d %s could not be scanned; see type_errors.", failed, pluralWord(failed, "file", "files")))
	}
	return out
}
