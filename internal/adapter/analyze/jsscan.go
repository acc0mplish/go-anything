package analyze

import (
	"encoding/json"
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

// JSSource analyzes a JavaScript or TypeScript tree with a scanner written in Go. It needs no
// Node.js and no npm package: comments, strings, template text and regular expression literals
// are blanked first, then imports, exports and functions are found with patterns. There is no
// parser and no type checking; Analysis.Limits states what that misses.
type JSSource struct{}

// maxJSFile bounds the size of a scanned file; larger files are usually bundles or generated.
const maxJSFile = 4 << 20

// jsTestDirs hold test files whatever their names.
var jsTestDirs = map[string]bool{"__tests__": true, "test": true, "tests": true}

var reJSTestName = regexp.MustCompile(`^(.+?)[._-](?:test|spec)\.[cm]?[jt]sx?$`)

// jsTestOf returns the module name a test file is for and whether rel is a test file.
func jsTestOf(rel string) (string, bool) {
	base := path.Base(rel)
	if m := reJSTestName.FindStringSubmatch(base); m != nil {
		return m[1], true
	}
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if jsTestDirs[part] {
			return strings.TrimSuffix(base, path.Ext(base)), true
		}
	}
	return "", false
}

// Analyze scans every JS and TS source under root, outside the folders skipDir names, and maps
// each file to one domain.Package.
func (JSSource) Analyze(root string) (domain.Analysis, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if info, err := os.Stat(abs); err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	} else if !info.IsDir() {
		return domain.Analysis{}, fmt.Errorf("analyze: %s is not a directory", abs)
	}
	var rels []string
	tests := map[string]int{}
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
		if !d.Type().IsRegular() || !isJSSource(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if name, ok := jsTestOf(rel); ok {
			tests[name]++
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	if len(rels) == 0 {
		return domain.Analysis{}, fmt.Errorf("analyze: no JavaScript or TypeScript modules under %s (test files are not modules); supported: %s", abs, SupportedLanguages)
	}
	sort.Strings(rels)
	return buildJS(abs, rels, tests), nil
}

// jsUnit is one JS or TS file with what resolution needs.
type jsUnit struct {
	*unitData
	rel   string
	facts jsFacts
}

func buildJS(abs string, rels []string, tests map[string]int) domain.Analysis {
	stems := map[string]int{}
	for _, rel := range rels {
		stems[strings.TrimSuffix(rel, path.Ext(rel))]++
	}
	byRel := map[string]*jsUnit{}
	var units []*jsUnit
	failed := 0
	for _, rel := range rels {
		name := strings.TrimSuffix(rel, path.Ext(rel))
		if stems[name] > 1 { // price.js next to price.ts: keep the extension to tell them apart
			name = rel
		}
		segs := strings.Split(name, "/")
		if len(segs) > 1 && segs[0] == "src" {
			segs = segs[1:]
		}
		if n := len(segs); n > 1 && segs[n-1] == "index" {
			segs = segs[:n-1] // an index file is its folder's entry point
		}
		u := &jsUnit{
			unitData: &unitData{
				pkg:       &domain.Package{ImportPath: name, Dir: path.Dir(rel), Files: []string{path.Base(rel)}},
				segments:  segs,
				importPos: map[string]string{},
			},
			rel: rel,
		}
		stem := path.Base(strings.TrimSuffix(rel, path.Ext(rel)))
		u.pkg.Tests = tests[stem]
		data, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			u.pkg.TypeErrors = []string{rel + ": cannot read: " + err.Error()}
			failed++
		case len(data) > maxJSFile:
			u.pkg.Lines = strings.Count(string(data), "\n")
			u.pkg.TypeErrors = []string{fmt.Sprintf("%s: not scanned, %d bytes is over the %d byte limit (bundled or generated code?)", rel, len(data), maxJSFile)}
			failed++
		default:
			u.pkg.Lines = strings.Count(string(data), "\n")
			u.facts = scanJS(data)
		}
		byRel[rel] = u
		units = append(units, u)
	}

	for _, u := range units {
		p := u.pkg
		pos := func(line int) string { return u.rel + ":" + strconv.Itoa(line) }
		fsNames := map[string]bool{} // local names bound from an fs module
		usesFS := false
		for _, imp := range u.facts.imports {
			if imp.dynamic {
				u.risk(domain.RiskDynamicCode, pos(imp.line), imp.how+" with "+describeSpec(imp.spec))
			}
			if imp.spec == "" {
				continue
			}
			if t := resolveJSSpec(byRel, path.Dir(u.rel), imp.spec); t != nil {
				u.addImport(t.pkg.ImportPath, pos(imp.line))
				for i := range imp.binds {
					imp.binds[i].target = t
				}
				continue
			}
			bare := strings.TrimPrefix(imp.spec, "node:")
			if jsIsFS(bare) {
				usesFS = true
				for _, b := range imp.binds {
					fsNames[b.local] = true
				}
			}
			if kind := jsSpecRisk(bare); kind != "" {
				u.risk(kind, pos(imp.line), imp.how+" "+imp.spec)
			}
		}
		for _, r := range u.facts.risks {
			if r.needsFS && !(usesFS && (r.member || fsNames[r.name])) {
				continue
			}
			u.risk(r.kind, pos(r.line), r.detail)
		}
		// bindings from imported names to their target unit
		binds := map[string]jsBind{}
		for _, imp := range u.facts.imports {
			for _, b := range imp.binds {
				if b.target != nil {
					binds[b.local] = b
				}
			}
		}
		for _, f := range u.facts.funcs {
			p.Funcs = appendUnique(p.Funcs, f.name)
			exported := f.exported || u.facts.exportNames[f.name]
			if exported {
				p.Exported = appendUnique(p.Exported, f.name)
			}
			if hasFunc(p.Functions, f.name) { // an overload or a redefinition keeps the first position
				continue
			}
			info := domain.FuncInfo{Name: f.name, Signature: f.params, Pos: pos(f.line), Exported: exported}
			self := domain.FuncID(p.ImportPath, "", f.name)
			callees := map[string]bool{}
			for _, call := range f.calls {
				if id := resolveJSCall(u, binds, call); id != "" && id != self {
					callees[id] = true
				}
			}
			for id := range callees {
				info.Callees = append(info.Callees, id)
			}
			sort.Strings(info.Callees)
			p.Functions = append(p.Functions, info)
		}
	}

	out := domain.Analysis{Root: filepath.ToSlash(abs), Module: jsModuleName(abs), Language: LangJS}
	data := make([]*unitData, len(units))
	for i, u := range units {
		data[i] = u.unitData
	}
	finishOriginal(&out, data)
	out.Limits = jsLimits(failed)
	return out
}

func describeSpec(spec string) string {
	if spec == "" {
		return "a computed argument"
	}
	return strconv.Quote(spec)
}

// resolveJSCall turns a call written in u into the FuncID of a repo function, or "".
func resolveJSCall(u *jsUnit, binds map[string]jsBind, call string) string {
	first, member, dotted := strings.Cut(call, ".")
	if b, ok := binds[first]; ok {
		t := b.target
		switch {
		case dotted && (b.imported == "*" || b.imported == "default"):
			if t.exports(member) {
				return domain.FuncID(t.pkg.ImportPath, "", member)
			}
		case !dotted && b.imported == "default":
			if name := t.facts.defaultName; name != "" && t.hasFunc(name) {
				return domain.FuncID(t.pkg.ImportPath, "", name)
			}
		case !dotted && b.imported != "*":
			if t.exports(b.imported) {
				return domain.FuncID(t.pkg.ImportPath, "", b.imported)
			}
		}
		return ""
	}
	if !dotted && u.hasFunc(call) {
		return domain.FuncID(u.pkg.ImportPath, "", call)
	}
	return ""
}

func (u *jsUnit) hasFunc(name string) bool {
	for _, f := range u.facts.funcs {
		if f.name == name {
			return true
		}
	}
	return false
}

func (u *jsUnit) exports(name string) bool {
	for _, f := range u.facts.funcs {
		if f.name == name && (f.exported || u.facts.exportNames[name]) {
			return true
		}
	}
	return false
}

// jsExtOrder is the order extensions are tried when a specifier leaves the extension out.
var jsExtOrder = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// resolveJSSpec finds the repo file a relative specifier names, as Node and TypeScript resolve it:
// the exact file, the file with an extension added, a TypeScript file behind a .js specifier, or
// the folder's index file. A bare specifier is a package outside the repo and returns nil.
func resolveJSSpec(byRel map[string]*jsUnit, fromDir, spec string) *jsUnit {
	if !(spec == "." || spec == ".." || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")) {
		return nil
	}
	p := path.Clean(path.Join(fromDir, spec))
	if p == ".." || strings.HasPrefix(p, "../") {
		return nil
	}
	if u := byRel[p]; u != nil {
		return u
	}
	for _, ext := range jsExtOrder {
		if u := byRel[p+ext]; u != nil {
			return u
		}
	}
	switch path.Ext(p) {
	case ".js", ".jsx", ".mjs", ".cjs":
		stem := strings.TrimSuffix(p, path.Ext(p))
		for _, ext := range []string{".ts", ".tsx", ".mts", ".cts"} {
			if u := byRel[stem+ext]; u != nil {
				return u
			}
		}
	}
	for _, ext := range jsExtOrder {
		if u := byRel[path.Join(p, "index"+ext)]; u != nil {
			return u
		}
	}
	return nil
}

// jsSpecRisks classify imported packages; a specifier matches an entry when it equals it or
// continues it with a slash.
var jsSpecRisks = []struct{ name, kind string }{
	{"child_process", domain.RiskSideEffects},
	{"worker_threads", domain.RiskConcurrency}, {"cluster", domain.RiskConcurrency},
	{"vm", domain.RiskDynamicCode},
	{"net", domain.RiskNetwork}, {"http", domain.RiskNetwork}, {"https", domain.RiskNetwork},
	{"http2", domain.RiskNetwork}, {"dgram", domain.RiskNetwork}, {"tls", domain.RiskNetwork},
	{"dns", domain.RiskNetwork}, {"axios", domain.RiskNetwork}, {"node-fetch", domain.RiskNetwork},
	{"undici", domain.RiskNetwork}, {"ws", domain.RiskNetwork}, {"got", domain.RiskNetwork},
	{"superagent", domain.RiskNetwork}, {"request", domain.RiskNetwork},
	{"ffi-napi", domain.RiskNativeCode}, {"ffi", domain.RiskNativeCode}, {"ref-napi", domain.RiskNativeCode},
	{"bindings", domain.RiskNativeCode}, {"node-gyp-build", domain.RiskNativeCode},
}

func jsSpecRisk(spec string) string {
	if strings.HasSuffix(spec, ".node") {
		return domain.RiskNativeCode
	}
	for _, r := range jsSpecRisks {
		if spec == r.name || strings.HasPrefix(spec, r.name+"/") {
			return r.kind
		}
	}
	return ""
}

func jsIsFS(spec string) bool {
	switch spec {
	case "fs", "fs/promises", "fs-extra", "graceful-fs":
		return true
	}
	return false
}

// jsModuleName is the package.json name, or the folder name when there is none.
func jsModuleName(abs string) string {
	if b, err := os.ReadFile(filepath.Join(abs, "package.json")); err == nil {
		var pj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &pj) == nil && strings.TrimSpace(pj.Name) != "" {
			return strings.TrimSpace(pj.Name)
		}
	}
	return filepath.Base(abs)
}

func jsLimits(failed int) []string {
	out := []string{
		"Scanned with patterns after blanking comments, strings, template text and regular expressions; there is no parser and no type checking, and nothing was run.",
		"Functions are top-level function declarations, top-level const, let or var bound to an arrow function or function expression, and CommonJS exports.name assignments. Class methods, object methods and code inside wrappers (IIFE, UMD, namespaces) are not listed.",
		"Imports resolve relative specifiers (./, ../) to files with a JS or TS extension or to an index file. Bare specifiers, tsconfig paths and package.json exports maps count as outside the repo.",
		"A dynamic import() or a require() with a computed argument is a dynamic_code risk; its target counts as an import only when it is a plain string.",
		"Callees are calls to imported names and to top-level functions of the same file; calls through objects and this are not followed, so Callers is a lower bound.",
		"Risks come from imported modules and a fixed list of calls (eval, new Function, fetch, fs writes when fs is imported, Date.now, Math.random and the like).",
		"JSX text with an apostrophe or a quote can confuse the string blanking until the end of that line.",
		"A module counts the test files named <name>.test.*, <name>.spec.* or <name>_test.*, and the files under __tests__, test and tests folders, anywhere in the repo.",
	}
	if failed > 0 {
		out = append(out, fmt.Sprintf("%d %s could not be scanned; see type_errors.", failed, pluralWord(failed, "file", "files")))
	}
	return out
}

// jsFacts is what the scanner finds in one file.
type jsFacts struct {
	imports []jsImport
	funcs   []jsFunc
	risks   []jsRisk
	// exportNames are local names exported by an export list or a CommonJS export.
	exportNames map[string]bool
	// defaultName is the local function behind export default, if any.
	defaultName string
}

type jsImport struct {
	spec    string // "" when the argument is computed
	line    int
	how     string // imports, requires, dynamic import
	dynamic bool
	binds   []jsBind
}

// jsBind is one local name an import binds. imported is the exported name, "default", or "*"
// for a namespace import or a whole required module.
type jsBind struct {
	local, imported string
	target          *jsUnit
}

type jsFunc struct {
	name     string
	line     int
	params   string
	exported bool
	calls    []string
}

type jsRisk struct {
	kind, detail string
	line         int
	// needsFS marks an fs write call that only counts when the file imports an fs module;
	// member is true for a call written x.writeFile(...), name is the called name.
	needsFS bool
	member  bool
	name    string
}

const jsIdent = `[A-Za-z_$][\w$]*`

var (
	reJSImportFrom = regexp.MustCompile(`(?:^|[^.\w$])(import|export)\s+((?:type\s+)?[^;'"` + "`" + `(=]*?)\bfrom\s*(['"])`)
	reJSImportBare = regexp.MustCompile(`(?:^|[^.\w$])import\s*(['"])`)
	reJSRequire    = regexp.MustCompile(`(?:^|[^.\w$])require\s*\(\s*(['"` + "`" + `]?)`)
	reJSDynImport  = regexp.MustCompile(`(?:^|[^.\w$])import\s*\(\s*(['"` + "`" + `]?)`)
	reJSRequireVar = regexp.MustCompile(`\b(?:const|let|var)\s+(?:(` + jsIdent + `)|\{([^}]*)\})\s*=\s*require\s*\(\s*['"]`)
	reJSFuncDecl   = regexp.MustCompile(`(?:^|[^.\w$])(export\s+(?:default\s+)?)?(async\s+)?function\s*(?:\*\s*)?(` + jsIdent + `)?\s*(?:<[^>(]*>)?\s*\(`)
	reJSVarFunc    = regexp.MustCompile(`(?:^|[^.\w$])(export\s+)?(?:const|let|var)\s+(` + jsIdent + `)\s*(?::[^=;]+)?=\s*(?:async\s+)?(?:(function)\b[^(]*\(|(?:<[^>(]*>\s*)?(\()|(` + jsIdent + `)\s*=>)`)
	reJSCJSExport  = regexp.MustCompile(`(?:^|[^.\w$])(?:module\.)?exports\.(` + jsIdent + `)\s*=\s*(?:async\s+)?(?:(function)\b[^(]*\(|(\()|(` + jsIdent + `)\s*(=>)?)`)
	reJSModExports = regexp.MustCompile(`(?:^|[^.\w$])module\.exports\s*=\s*(?:(\{)|(?:async\s+)?(function)\b\s*(?:\*\s*)?(` + jsIdent + `)?\s*\(|(` + jsIdent + `)\s*(?:[;\n]|$))`)
	reJSExportList = regexp.MustCompile(`(?:^|[^.\w$])export\s*\{([^}]*)\}`)
	reJSExportDef  = regexp.MustCompile(`(?:^|[^.\w$])export\s+default\s+(` + jsIdent + `)\s*(?:[;\n]|$)`)
	reJSFromAfter  = regexp.MustCompile(`^\s*from\b`)
	reJSIdentOnly  = regexp.MustCompile(`^` + jsIdent + `$`)
	reJSCall       = regexp.MustCompile(`(?:^|[^.\w$])(` + jsIdent + `(?:\s*\.\s*` + jsIdent + `)?)\s*\(`)

	reJSEval     = regexp.MustCompile(`(?:^|[^.\w$])eval\s*\(`)
	reJSNewFunc  = regexp.MustCompile(`\bnew\s+Function\s*\(`)
	reJSFetch    = regexp.MustCompile(`(?:^|[^.\w$])fetch\s*\(`)
	reJSNewNet   = regexp.MustCompile(`\bnew\s+(WebSocket|XMLHttpRequest|EventSource)\s*\(`)
	reJSNondet   = regexp.MustCompile(`\b(Date\.now|Math\.random|performance\.now|crypto\.randomUUID|crypto\.randomBytes|crypto\.getRandomValues)\s*\(`)
	reJSNewDate  = regexp.MustCompile(`\bnew\s+Date\s*\(\s*\)`)
	reJSFSMember = regexp.MustCompile(`\.\s*(` + jsFSWrites + `)\s*\(`)
	reJSFSBare   = regexp.MustCompile(`(?:^|[^.\w$])(` + jsFSWrites + `)\s*\(`)
)

const jsFSWrites = `writeFileSync|writeFile|appendFileSync|appendFile|createWriteStream|mkdirSync|mkdir|rmSync|rm|rmdirSync|rmdir|unlinkSync|unlink|renameSync|rename|copyFileSync|copyFile|cpSync|cp|truncateSync|truncate|symlinkSync|symlink|chmodSync|chmod|mkdtempSync|mkdtemp`

// jsKeywords are never function names in a call.
var jsKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "function": true,
	"return": true, "typeof": true, "new": true, "super": true, "import": true, "require": true,
	"await": true, "yield": true, "void": true, "delete": true, "in": true, "of": true,
}

// scanJS finds imports, functions, exports and risk sites in one file.
func scanJS(src []byte) jsFacts {
	masked := maskJS(src)
	text := string(masked)
	depth := braceDepths(masked)
	lines := newLineIndex(masked)
	facts := jsFacts{exportNames: map[string]bool{}}
	literal := func(q int) (string, bool) { // the string whose opening quote is at q
		if q < 0 || q >= len(masked) {
			return "", false
		}
		end := strings.IndexByte(text[q+1:], masked[q])
		if end < 0 {
			return "", false
		}
		v := string(src[q+1 : q+1+end])
		if masked[q] == '`' && strings.Contains(v, "${") {
			return "", false
		}
		return v, true
	}
	// wholeArg is the string at q when it is the whole first argument of a call, as in
	// require("x") but not require("./" + name).
	wholeArg := func(q int) (string, bool) {
		v, ok := literal(q)
		if !ok {
			return "", false
		}
		rest := strings.TrimLeft(text[q+len(v)+2:], " \t\r\n")
		if rest == "" || (rest[0] != ')' && rest[0] != ',') {
			return "", false
		}
		return v, true
	}
	// keyword start of a match: skip the one byte of context the patterns may have consumed
	start := func(m []int) int {
		s := m[0]
		if s < len(text) && !isJSIdentByte(text[s]) {
			s++
		}
		return s
	}

	seenSpec := map[int]bool{} // quote offsets already taken as an import
	for _, m := range reJSImportFrom.FindAllStringSubmatchIndex(text, -1) {
		q := m[6]
		spec, ok := literal(q)
		if !ok || seenSpec[q] {
			continue
		}
		seenSpec[q] = true
		imp := jsImport{spec: spec, line: lines.of(start(m)), how: "imports"}
		if text[m[2]:m[3]] == "import" {
			imp.binds = parseImportClause(text[m[4]:m[5]])
		}
		facts.imports = append(facts.imports, imp)
	}
	for _, m := range reJSImportBare.FindAllStringSubmatchIndex(text, -1) {
		q := m[2]
		if spec, ok := literal(q); ok && !seenSpec[q] {
			seenSpec[q] = true
			facts.imports = append(facts.imports, jsImport{spec: spec, line: lines.of(start(m)), how: "imports"})
		}
	}
	requireBinds := map[int][]jsBind{} // offset of "require" -> bindings
	for _, m := range reJSRequireVar.FindAllStringSubmatchIndex(text, -1) {
		at := strings.LastIndex(text[m[0]:m[1]], "require") + m[0]
		if m[2] >= 0 {
			requireBinds[at] = []jsBind{{local: text[m[2]:m[3]], imported: "*"}}
		} else {
			requireBinds[at] = parseDestructure(text[m[4]:m[5]])
		}
	}
	for _, m := range reJSRequire.FindAllStringSubmatchIndex(text, -1) {
		at := strings.Index(text[m[0]:m[1]], "require") + m[0]
		imp := jsImport{line: lines.of(at), how: "requires", binds: requireBinds[at]}
		if m[3] > m[2] {
			if spec, ok := wholeArg(m[2]); ok {
				imp.spec = spec
				seenSpec[m[2]] = true
			}
		}
		imp.dynamic = imp.spec == ""
		facts.imports = append(facts.imports, imp)
	}
	for _, m := range reJSDynImport.FindAllStringSubmatchIndex(text, -1) {
		imp := jsImport{line: lines.of(start(m)), how: "dynamic import", dynamic: true}
		if m[3] > m[2] {
			if spec, ok := wholeArg(m[2]); ok {
				imp.spec = spec
			}
		}
		facts.imports = append(facts.imports, imp)
	}
	sort.SliceStable(facts.imports, func(i, j int) bool { return facts.imports[i].line < facts.imports[j].line })

	addFunc := func(name string, at int, params string, exported bool, bodyFrom int, arrow bool) {
		f := jsFunc{name: name, line: lines.of(at), params: params, exported: exported}
		if b, e := jsBody(masked, bodyFrom, arrow); b >= 0 {
			f.calls = jsCalls(text[b:e])
		}
		facts.funcs = append(facts.funcs, f)
	}
	for _, m := range reJSFuncDecl.FindAllStringSubmatchIndex(text, -1) {
		s := start(m)
		if depth[s] != 0 {
			continue
		}
		open := m[1] - 1
		exportClause := ""
		if m[2] >= 0 {
			exportClause = text[m[2]:m[3]]
		}
		name := ""
		if m[6] >= 0 {
			name = text[m[6]:m[7]]
		}
		isDefault := strings.Contains(exportClause, "default")
		switch {
		case name == "" && isDefault:
			name = "default"
		case name == "":
			continue // a function expression; reJSVarFunc and the export patterns name it
		}
		if isDefault {
			facts.defaultName = name
		}
		if !statementStart(text, s) { // const f = function g() is named f by reJSVarFunc; a callback is not top level
			continue
		}
		params, close := jsParams(masked, src, open)
		addFunc(name, s, params, exportClause != "", close, false)
	}
	for _, m := range reJSVarFunc.FindAllStringSubmatchIndex(text, -1) {
		s := start(m)
		if depth[s] != 0 {
			continue
		}
		name := text[m[4]:m[5]]
		exported := m[2] >= 0
		switch {
		case m[6] >= 0 || m[8] >= 0: // function( or (params) =>
			open := m[1] - 1
			params, close := jsParams(masked, src, open)
			arrow := m[8] >= 0
			if arrow {
				var ok bool
				if close, ok = arrowFollows(text, close); !ok {
					continue // an expression such as const x = (a) + b, not an arrow function
				}
			}
			addFunc(name, s, params, exported, close, arrow)
		case m[10] >= 0: // x => ...
			addFunc(name, s, "("+text[m[10]:m[11]]+")", exported, m[1], true)
		}
	}
	for _, m := range reJSCJSExport.FindAllStringSubmatchIndex(text, -1) {
		s := start(m)
		if depth[s] != 0 {
			continue
		}
		name := text[m[2]:m[3]]
		switch {
		case m[4] >= 0 || m[6] >= 0:
			open := m[1] - 1
			params, close := jsParams(masked, src, open)
			arrow := m[6] >= 0
			if arrow {
				var ok bool
				if close, ok = arrowFollows(text, close); !ok {
					continue
				}
			}
			addFunc(name, s, params, true, close, arrow)
		case m[8] >= 0 && m[10] >= 0: // exports.x = a => ...
			addFunc(name, s, "("+text[m[8]:m[9]]+")", true, m[1], true)
		case m[8] >= 0: // exports.x = localName
			facts.exportNames[text[m[8]:m[9]]] = true
		}
	}
	for _, m := range reJSModExports.FindAllStringSubmatchIndex(text, -1) {
		s := start(m)
		if depth[s] != 0 {
			continue
		}
		switch {
		case m[2] >= 0: // module.exports = { a, b: c }
			if end := matchClose(masked, m[3]-1, '{', '}'); end > 0 {
				for _, n := range objectExportNames(text[m[3]:end]) {
					facts.exportNames[n] = true
				}
			}
		case m[4] >= 0: // module.exports = function [name](
			name := "module.exports"
			if m[6] >= 0 {
				name = text[m[6]:m[7]]
			}
			params, close := jsParams(masked, src, m[1]-1)
			addFunc(name, s, params, true, close, false)
		case m[8] >= 0:
			facts.exportNames[text[m[8]:m[9]]] = true
		}
	}
	for _, m := range reJSExportList.FindAllStringSubmatchIndex(text, -1) {
		if reJSFromAfter.MatchString(text[m[1]:]) {
			continue // a re-export from another module, already an import
		}
		for _, b := range parseNamedList(text[m[2]:m[3]]) {
			facts.exportNames[b.imported] = true // in an export list the local name comes first
		}
	}
	for _, m := range reJSExportDef.FindAllStringSubmatchIndex(text, -1) {
		n := text[m[2]:m[3]]
		if n != "function" && n != "async" && n != "class" {
			facts.exportNames[n] = true
			facts.defaultName = n
		}
	}

	risk := func(kind string, at int, detail string) {
		facts.risks = append(facts.risks, jsRisk{kind: kind, line: lines.of(at), detail: detail})
	}
	for _, m := range reJSEval.FindAllStringIndex(text, -1) {
		risk(domain.RiskDynamicCode, start(m), "calls eval")
	}
	for _, m := range reJSNewFunc.FindAllStringIndex(text, -1) {
		risk(domain.RiskDynamicCode, m[0], "new Function")
	}
	for _, m := range reJSFetch.FindAllStringIndex(text, -1) {
		risk(domain.RiskNetwork, start(m), "calls fetch")
	}
	for _, m := range reJSNewNet.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskNetwork, m[0], "new "+text[m[2]:m[3]])
	}
	for _, m := range reJSNondet.FindAllStringSubmatchIndex(text, -1) {
		risk(domain.RiskNondeterminism, m[0], "calls "+text[m[2]:m[3]])
	}
	for _, m := range reJSNewDate.FindAllStringIndex(text, -1) {
		risk(domain.RiskNondeterminism, m[0], "new Date()")
	}
	for _, m := range reJSFSMember.FindAllStringSubmatchIndex(text, -1) {
		n := text[m[2]:m[3]]
		facts.risks = append(facts.risks, jsRisk{kind: domain.RiskFSWrite, line: lines.of(m[2]), detail: "calls " + n, needsFS: true, member: true, name: n})
	}
	for _, m := range reJSFSBare.FindAllStringSubmatchIndex(text, -1) {
		n := text[m[2]:m[3]]
		facts.risks = append(facts.risks, jsRisk{kind: domain.RiskFSWrite, line: lines.of(m[2]), detail: "calls " + n, needsFS: true, name: n})
	}
	// functions in source order, whichever pattern found them
	sort.SliceStable(facts.funcs, func(i, j int) bool { return facts.funcs[i].line < facts.funcs[j].line })
	return facts
}

func isJSIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// statementStart reports whether a declaration at offset at starts a statement: the text before it
// does not end in an operator, an open parenthesis or bracket, or a comma, which would make it an
// expression such as a callback or the right side of an assignment.
func statementStart(text string, at int) bool {
	before := strings.TrimRight(text[:at], " \t\r\n")
	if before == "" {
		return true
	}
	return !strings.ContainsRune("=(,:?&|![+-*%<>~^", rune(before[len(before)-1]))
}

// arrowFollows reports whether the text after a parameter list leads to => directly or through a
// TypeScript return type, and returns the offset just after the =>.
func arrowFollows(text string, close int) (int, bool) {
	rest := text[close:]
	i := strings.Index(rest, "=>")
	if i < 0 {
		return 0, false
	}
	between := strings.TrimSpace(rest[:i])
	if between != "" && !(strings.HasPrefix(between, ":") && !strings.ContainsAny(between, ";=")) {
		return 0, false
	}
	return close + i + 2, true
}

// parseImportClause reads the bindings of an import clause: a default name, "* as ns", and
// "{ a, b as c }", in any valid combination. "type" markers are ignored.
func parseImportClause(clause string) []jsBind {
	clause = strings.TrimSpace(clause)
	clause = strings.TrimSpace(strings.TrimPrefix(clause, "type "))
	var out []jsBind
	if i := strings.IndexByte(clause, '{'); i >= 0 {
		if j := strings.IndexByte(clause[i:], '}'); j >= 0 {
			out = append(out, parseNamedList(clause[i+1:i+j])...)
			clause = clause[:i] + clause[i+j+1:]
		}
	}
	for _, part := range strings.Split(clause, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, "*"):
			if f := strings.Fields(part); len(f) == 3 && f[1] == "as" {
				out = append(out, jsBind{local: f[2], imported: "*"})
			}
		default:
			if f := strings.Fields(part); len(f) == 1 {
				out = append(out, jsBind{local: f[0], imported: "default"})
			}
		}
	}
	return out
}

// parseNamedList reads "a, b as c, type d" into bindings (imported a local a, imported b local c).
func parseNamedList(list string) []jsBind {
	var out []jsBind
	for _, part := range strings.Split(list, ",") {
		f := strings.Fields(part)
		if len(f) > 0 && f[0] == "type" && len(f) != 1 && f[1] != "as" {
			f = f[1:]
		}
		switch {
		case len(f) == 1:
			out = append(out, jsBind{local: f[0], imported: f[0]})
		case len(f) == 3 && f[1] == "as":
			out = append(out, jsBind{local: f[2], imported: f[0]})
		}
	}
	return out
}

// parseDestructure reads "{ a, b: c }" of a require into bindings.
func parseDestructure(list string) []jsBind {
	var out []jsBind
	for _, part := range strings.Split(list, ",") {
		name, local, renamed := strings.Cut(part, ":")
		name, local = strings.TrimSpace(name), strings.TrimSpace(local)
		if i := strings.IndexByte(local, '='); i >= 0 { // a default value
			local = strings.TrimSpace(local[:i])
		}
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if !renamed {
			local = name
		}
		if name != "" && local != "" && !strings.HasPrefix(name, "...") {
			out = append(out, jsBind{local: local, imported: name})
		}
	}
	return out
}

// objectExportNames reads the local names a "module.exports = { ... }" object exports:
// shorthand entries and "key: localName" entries.
func objectExportNames(body string) []string {
	var out []string
	depth := 0
	startAt := 0
	flush := func(entry string) {
		entry = strings.TrimSpace(entry)
		key, val, ok := strings.Cut(entry, ":")
		if !ok {
			val = key
		}
		val = strings.TrimSpace(val)
		if reJSIdentOnly.MatchString(val) {
			out = append(out, val)
		}
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				flush(body[startAt:i])
				startAt = i + 1
			}
		}
	}
	flush(body[startAt:])
	return out
}

// jsParams returns the parameter list that opens at open, with whitespace collapsed, and the
// offset just after its closing parenthesis.
func jsParams(masked, src []byte, open int) (string, int) {
	close := matchClose(masked, open, '(', ')')
	if close < 0 {
		return "(...)", open + 1
	}
	p := strings.Join(strings.Fields(string(src[open+1:close])), " ")
	if len(p) > 120 {
		p = p[:117] + "..."
	}
	return "(" + p + ")", close + 1
}

// jsBody finds a function body from just after its parameters (or after => for an arrow): a block
// in braces, or for an arrow an expression that ends at a ; or a line end outside brackets.
func jsBody(masked []byte, from int, arrow bool) (int, int) {
	i := from
	for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t' || masked[i] == '\r' || masked[i] == '\n') {
		i++
	}
	if !arrow {
		j := strings.IndexByte(string(masked[i:]), '{')
		if j < 0 {
			return -1, -1
		}
		i += j
	}
	if i >= len(masked) {
		return -1, -1
	}
	if masked[i] == '{' {
		end := matchClose(masked, i, '{', '}')
		if end < 0 {
			return i, len(masked)
		}
		return i, end
	}
	depth := 0
	for j := i; j < len(masked); j++ {
		switch masked[j] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth < 0 {
				return i, j
			}
		case ';', '\n':
			if depth == 0 {
				return i, j
			}
		}
	}
	return i, len(masked)
}

// jsCalls lists the distinct names called in a body, "f" or "ns.f".
func jsCalls(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reJSCall.FindAllStringSubmatch(body, -1) {
		name := strings.Join(strings.Fields(strings.ReplaceAll(m[1], ".", " . ")), "")
		if first, _, _ := strings.Cut(name, "."); jsKeywords[first] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// matchClose returns the offset of the bracket that closes the one at open, or -1.
func matchClose(masked []byte, open int, o, c byte) int {
	depth := 0
	for i := open; i < len(masked); i++ {
		switch masked[i] {
		case o:
			depth++
		case c:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// braceDepths gives the curly brace depth at every offset of the masked text.
func braceDepths(masked []byte) []int32 {
	out := make([]int32, len(masked)+1)
	var d int32
	for i, c := range masked {
		out[i] = d
		switch c {
		case '{':
			d++
		case '}':
			if d > 0 {
				d--
			}
		}
	}
	out[len(masked)] = d
	return out
}

// lineIndex maps offsets to 1-based line numbers.
type lineIndex []int

func newLineIndex(b []byte) lineIndex {
	var nl lineIndex
	for i, c := range b {
		if c == '\n' {
			nl = append(nl, i)
		}
	}
	return nl
}

func (l lineIndex) of(off int) int { return sort.SearchInts(l, off) + 1 }

// regexAfterWord are the keywords after which a slash starts a regular expression, not a division.
var regexAfterWord = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true,
	"delete": true, "void": true, "throw": true, "case": true, "do": true, "else": true,
	"yield": true, "await": true,
}

// maskJS blanks what patterns must not look into: comments entirely, and the contents of string,
// template and regular expression literals. Quotes, backticks, ${ } and slashes stay, as does every
// newline, so offsets and line numbers match the source. A string that does not close on its line
// ends there, so one stray quote (as in JSX text) cannot blank the rest of the file.
func maskJS(src []byte) []byte {
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
	if n > 1 && src[0] == '#' && src[1] == '!' {
		j := strings.IndexByte(string(src), '\n')
		if j < 0 {
			j = n
		}
		blank(0, j)
		i = j
	}
	var tmpl []int // brace depth at each open ${
	depth := 0
	lastSig := byte(0)
	lastWord := ""
	// template scans template text from i until the closing backtick or a ${, blanking the text.
	template := func(i int) int {
		j := i
		for j < n {
			switch {
			case src[j] == '\\':
				j += 2
				continue
			case src[j] == '`':
				blank(i, j)
				lastSig, lastWord = '"', ""
				return j + 1
			case src[j] == '$' && j+1 < n && src[j+1] == '{':
				blank(i, j)
				tmpl = append(tmpl, depth)
				depth++
				lastSig, lastWord = '{', ""
				return j + 2
			}
			j++
		}
		blank(i, n)
		return n
	}
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
		case c == '\'' || c == '"':
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
			lastSig, lastWord = '"', ""
		case c == '`':
			i = template(i + 1)
		case c == '/':
			regex := lastSig == 0 || (lastSig == 'a' && regexAfterWord[lastWord]) ||
				(lastSig != 'a' && lastSig != ')' && lastSig != ']' && lastSig != '"')
			if !regex {
				lastSig, lastWord = '/', ""
				i++
				continue
			}
			j, inClass := i+1, false
			for j < n && src[j] != '\n' {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '[' {
					inClass = true
				} else if src[j] == ']' {
					inClass = false
				} else if src[j] == '/' && !inClass {
					break
				}
				j++
			}
			if j >= n || src[j] != '/' { // not a regular expression after all
				lastSig, lastWord = '/', ""
				i++
				continue
			}
			blank(i+1, j)
			j++
			for j < n && isJSIdentByte(src[j]) { // flags
				j++
			}
			i = j
			lastSig, lastWord = 'a', ""
		case c == '{':
			depth++
			lastSig, lastWord = '{', ""
			i++
		case c == '}':
			if k := len(tmpl); k > 0 && tmpl[k-1] == depth-1 {
				tmpl = tmpl[:k-1]
				depth--
				i = template(i + 1)
				continue
			}
			depth--
			lastSig, lastWord = '}', ""
			i++
		case isJSIdentByte(c):
			j := i
			for j < n && isJSIdentByte(src[j]) {
				j++
			}
			lastSig, lastWord = 'a', string(src[i:j])
			i = j
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		default:
			lastSig, lastWord = c, ""
			i++
		}
	}
	return out
}
