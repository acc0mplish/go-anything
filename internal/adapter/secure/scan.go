// Package secure scans one Go module against the security function confirmation checklist in
// domain.SecurityChecklist and turns every item into a pass, fail, skip or manual result.
//
// The scanner judges source patterns conservatively from the AST: auto items pass when the
// pattern is absent and fail with one finding per match otherwise; tool items run govulncheck
// and go vet when they are installed and skip with a note when they are not; manual items are
// left to a human. Findings describe the shape that matched and never repeat a matched secret,
// so a report can be shared without leaking credentials.
package secure

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// Version is the scanner version. A build can override it with the usual -ldflags pattern.
var Version = "dev"

// Tool binaries looked up in PATH and the wall-clock budget for one tool run. The variables
// are unexported so tests can point them at fakes or at a name that does not exist.
var (
	govulncheckBin = "govulncheck"
	goBin          = "go"
	toolTimeout    = 5 * time.Minute
)

// skipDirs are never walked: vendored and testdata trees are not the module's own source (the
// go tool skips them too), and the remaining entries hold dependencies, caches and build
// output. Hidden directories are skipped as well, like the analyze package does.
var skipDirs = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "dist": true,
	"build": true, ".git": true, "vendor": true, "testdata": true,
}

// Limits that keep tool output in a report at a sane size.
const (
	toolMaxLines     = 40
	toolMaxLineRunes = 240
)

// toolPosRe pulls a leading "file:line:col:" or "file:line:" prefix out of a tool output line.
var toolPosRe = regexp.MustCompile(`^(\S+:\d+(?::\d+)?):\s*(.*)$`)

// fileUnit is one parsed non-test file with its import-path to local-name mapping.
type fileUnit struct {
	path  string // slash-separated path relative to the module root
	file  *ast.File
	names map[string]string // import path -> name the file refers to (alias or default)
}

// scanner holds the whole parsed module.
type scanner struct {
	fset  *token.FileSet
	units []fileUnit
}

// secretPattern is a literal shape that always indicates a hardcoded credential.
type secretPattern struct {
	re     *regexp.Regexp
	detail string
}

// secretPatterns never appear in findings; only detail is reported.
var secretPatterns = []secretPattern{
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "AWS access key id literal"},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`), "API key literal with sk- prefix"},
	{regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`), "GitHub personal access token literal"},
	{regexp.MustCompile(`xox[baprs]-`), "Slack token literal"},
	{regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----`), "PEM private key block literal"},
	{regexp.MustCompile(`password=`), "literal embeds a password= credential"},
}

// credentialNames are identifiers whose string assignment is treated as a hardcoded credential.
var credentialNames = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true, "apikey": true, "api_key": true,
}

// dbCallNames are method names whose arguments are treated as SQL statements.
var dbCallNames = map[string]bool{"Query": true, "QueryRow": true, "Exec": true, "Ping": true}

// sqlKeywords mark a format string as SQL-shaped.
var sqlKeywords = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}

// logSecretMarkers mark a literal as carrying a secret into a log line.
var logSecretMarkers = []string{"password", "token=", "secret="}

// Run scans the module at root and returns one result per checklist item, in checklist order.
func Run(root string) (domain.SecureReport, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.SecureReport{}, fmt.Errorf("resolve root: %w", err)
	}
	modulePath, goVersion, err := readGoMod(abs)
	if err != nil {
		return domain.SecureReport{}, err
	}
	sc, err := collectUnits(abs)
	if err != nil {
		return domain.SecureReport{}, err
	}

	auto := map[string][]domain.SecureFinding{
		"SF-1.1": sc.checkCredentials(),
		"SF-2.1": sc.checkDebugEndpoints(),
		"SF-3.1": sc.checkWeakHashes(),
		"SF-3.2": sc.checkWeakCiphers(),
		"SF-3.3": sc.checkMathRand(),
		"SF-3.4": sc.checkInsecureSkipVerify(),
		"SF-3.5": sc.checkPlainHTTP(),
		"SF-4.1": sc.checkSQLAssembly(),
		"SF-4.2": sc.checkShellAssembly(),
		"SF-5.2": sc.checkSecretsInLogs(),
		"SF-6.1": sc.checkCookies(),
	}

	report := domain.SecureReport{
		Root:         abs,
		GeneratedAt:  time.Now().UTC(),
		ToolVersion:  Version,
		Items:        domain.SecurityChecklist(),
		GoVersion:    goVersion,
		ModulePath:   modulePath,
		ScannedFiles: len(sc.units),
	}
	for _, it := range report.Items {
		res := domain.SecureResult{Item: it.ID}
		switch {
		case it.Method == domain.SecureManual:
			res.Status = domain.SecureStatusManual
		case it.ID == "SF-5.1":
			if sc.hasLogging() {
				res.Status = domain.SecurePass
			} else {
				res.Status = domain.SecureFail
				res.Findings = []domain.SecureFinding{{Item: it.ID, Pos: abs,
					Detail: "no logging calls found in non-test sources"}}
			}
		case it.ID == "SF-7.2":
			if _, err := os.Stat(filepath.Join(abs, "go.sum")); err == nil {
				res.Status = domain.SecurePass
			} else if !hasExternalRequires(abs) {
				// go does not write a go.sum for a module with no dependencies; nothing is unpinned.
				res.Status = domain.SecurePass
				res.Note = "no external dependencies; go.sum is not created for a dependency-free module"
			} else {
				res.Status = domain.SecureFail
				res.Findings = []domain.SecureFinding{{Item: it.ID, Pos: abs,
					Detail: "go.sum not found next to go.mod; dependencies are not pinned"}}
			}
		case it.ID == "SF-7.1":
			res.Status, res.Findings, res.Note = runTool(abs, govulncheckBin, []string{"./..."})
			res.Findings = tag(it.ID, res.Findings)
		case it.ID == "SF-10.1":
			res.Status, res.Findings, res.Note = runTool(abs, goBin, []string{"vet", "./..."})
			res.Findings = tag(it.ID, res.Findings)
		default:
			if findings := tag(it.ID, auto[it.ID]); len(findings) == 0 {
				res.Status = domain.SecurePass
			} else {
				res.Status = domain.SecureFail
				res.Findings = findings
			}
		}
		if it.ID == "SF-2.1" {
			res.Note = "test-only usage is excluded by scanning non-test files only"
		}
		report.Results = append(report.Results, res)
	}
	return report, nil
}

// readGoMod reads the module path and go directive from the go.mod at root.
func readGoMod(root string) (modulePath, goVersion string, err error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case modulePath == "" && strings.HasPrefix(line, "module "):
			modulePath = unquote(strings.TrimSpace(strings.TrimPrefix(line, "module ")))
		case goVersion == "" && strings.HasPrefix(line, "go "):
			goVersion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	if modulePath == "" {
		return "", "", fmt.Errorf("go.mod in %s has no module directive", root)
	}
	return modulePath, goVersion, nil
}

// hasExternalRequires reports whether the go.mod at root requires any module. A dependency-free
// module legitimately has no go.sum, so pinning can only be missing when a require exists.
func hasExternalRequires(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return true // unreadable means unknown; the stricter answer keeps the item visible
	}
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case strings.HasPrefix(line, "require "):
			if reqEntry(strings.Fields(strings.TrimPrefix(line, "require "))) {
				return true
			}
		case inBlock:
			if reqEntry(strings.Fields(line)) {
				return true
			}
		}
	}
	return false
}

// reqEntry reports whether the fields of one require entry name a module.
func reqEntry(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	path := fields[0]
	return strings.Contains(path, ".") || strings.Contains(path, "/")
}

// unquote strips optional quoting from a go.mod value.
func unquote(s string) string {
	if v, err := strconv.Unquote(s); err == nil {
		return v
	}
	return strings.Trim(s, `"`)
}

// collectUnits walks the module and parses every non-test .go file below root, skipping
// vendor, testdata, hidden directories and the known dependency and build trees. A file that
// does not parse is left out; go vet and govulncheck report such breakage with better context.
func collectUnits(root string) (*scanner, error) {
	sc := &scanner{fset: token.NewFileSet()}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(sc.fset, p, nil, 0)
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		sc.units = append(sc.units, fileUnit{
			path:  filepath.ToSlash(rel),
			file:  f,
			names: localImportNames(f),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk module: %w", err)
	}
	return sc, nil
}

// localImportNames maps each import path of a file to the name the file uses for it: the
// alias when there is one, otherwise the last path element (net/http -> http).
func localImportNames(f *ast.File) map[string]string {
	m := make(map[string]string)
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if imp.Name != nil {
			m[p] = imp.Name.Name
			continue
		}
		if i := strings.LastIndex(p, "/"); i >= 0 {
			m[p] = p[i+1:]
		} else {
			m[p] = p
		}
	}
	return m
}

// posf builds a finding at p with a file:line position relative to the module root.
func (s *scanner) posf(u fileUnit, p token.Pos, format string, args ...any) domain.SecureFinding {
	return domain.SecureFinding{
		Pos:    fmt.Sprintf("%s:%d", u.path, s.fset.Position(p).Line),
		Detail: fmt.Sprintf(format, args...),
	}
}

// eachImport visits every import of every parsed file.
func (s *scanner) eachImport(fn func(u fileUnit, path string, pos token.Pos)) {
	for _, u := range s.units {
		for _, imp := range u.file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			fn(u, p, imp.Pos())
		}
	}
}

// eachStringLit visits every string literal of one file with its unquoted value.
func eachStringLit(f *ast.File, fn func(val string, pos token.Pos)) {
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if val, err := strconv.Unquote(lit.Value); err == nil {
				fn(val, lit.Pos())
			}
		}
		return true
	})
}

// isTrueIdent reports whether e is the predeclared identifier true.
func isTrueIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

// isCallOn reports whether call is pkg.name(...) for the local import name pkg.
func isCallOn(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pkg
}

// formatString returns the unquoted first argument of call when it is a string literal.
func formatString(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return val
}

// looksSQL reports whether s contains a SQL statement keyword, case-insensitively.
func looksSQL(s string) bool {
	up := strings.ToUpper(s)
	for _, kw := range sqlKeywords {
		if strings.Contains(up, kw) {
			return true
		}
	}
	return false
}

// checkCredentials implements SF-1.1: literal secret shapes and string literals assigned to
// credential-named identifiers or fields. Details describe the shape, never the value.
func (s *scanner) checkCredentials() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		ast.Inspect(u.file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				for _, pat := range secretPatterns {
					if pat.re.MatchString(val) {
						out = append(out, s.posf(u, n.Pos(), "%s", pat.detail))
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if i >= len(n.Rhs) {
						break
					}
					id, ok := lhs.(*ast.Ident)
					if !ok || !credentialNames[id.Name] {
						continue
					}
					lit, ok := n.Rhs[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil || val == "" || len(val) < 8 {
						continue
					}
					out = append(out, s.posf(u, n.Pos(),
						"identifier %q is assigned a hardcoded %d-character string literal", id.Name, len(val)))
				}
			case *ast.KeyValueExpr:
				key, ok := n.Key.(*ast.Ident)
				if !ok || !credentialNames[key.Name] {
					return true
				}
				lit, ok := n.Value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil || val == "" || len(val) < 8 {
					return true
				}
				out = append(out, s.posf(u, n.Pos(),
					"field %q is set to a hardcoded %d-character string literal", key.Name, len(val)))
			}
			return true
		})
	}
	return out
}

// checkDebugEndpoints implements SF-2.1: pprof or expvar imported by a non-test file.
func (s *scanner) checkDebugEndpoints() []domain.SecureFinding {
	var out []domain.SecureFinding
	s.eachImport(func(u fileUnit, p string, pos token.Pos) {
		if p == "net/http/pprof" || p == "expvar" {
			out = append(out, s.posf(u, pos, "production debug endpoint imported: %s", p))
		}
	})
	return out
}

// checkWeakHashes implements SF-3.1: MD5 and SHA-1 imports. Whether SHA-1 is used only for
// git or object ids is the operator's call to accept through the report.
func (s *scanner) checkWeakHashes() []domain.SecureFinding {
	var out []domain.SecureFinding
	s.eachImport(func(u fileUnit, p string, pos token.Pos) {
		if p == "crypto/md5" || p == "crypto/sha1" {
			out = append(out, s.posf(u, pos, "weak hash import: %s", p))
		}
	})
	return out
}

// checkWeakCiphers implements SF-3.2: imports containing des or rc4 (crypto/des, crypto/rc4).
func (s *scanner) checkWeakCiphers() []domain.SecureFinding {
	var out []domain.SecureFinding
	s.eachImport(func(u fileUnit, p string, pos token.Pos) {
		if strings.Contains(p, "des") || strings.Contains(p, "rc4") {
			out = append(out, s.posf(u, pos, "weak cipher import: %s", p))
		}
	})
	return out
}

// checkMathRand implements SF-3.3: math/rand (including versioned variants) in non-test files.
func (s *scanner) checkMathRand() []domain.SecureFinding {
	var out []domain.SecureFinding
	s.eachImport(func(u fileUnit, p string, pos token.Pos) {
		if p == "math/rand" || strings.HasPrefix(p, "math/rand/") {
			out = append(out, s.posf(u, pos, "non-crypto randomness import: %s", p))
		}
	})
	return out
}

// checkInsecureSkipVerify implements SF-3.4: the field set to true, in a composite literal or
// in an assignment. Setting it to false is never reported.
func (s *scanner) checkInsecureSkipVerify() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		ast.Inspect(u.file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				key, ok := n.Key.(*ast.Ident)
				if ok && key.Name == "InsecureSkipVerify" && isTrueIdent(n.Value) {
					out = append(out, s.posf(u, n.Pos(), "InsecureSkipVerify is set to true"))
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "InsecureSkipVerify" || i >= len(n.Rhs) {
						continue
					}
					if isTrueIdent(n.Rhs[i]) {
						out = append(out, s.posf(u, n.Pos(), "InsecureSkipVerify is assigned true"))
					}
				}
			}
			return true
		})
	}
	return out
}

// checkPlainHTTP implements SF-3.5: string literals starting with http://. Loopback URLs are
// a test and development pattern and are not reported.
func (s *scanner) checkPlainHTTP() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		eachStringLit(u.file, func(val string, pos token.Pos) {
			if !strings.HasPrefix(val, "http://") {
				return
			}
			if strings.HasPrefix(val, "http://localhost") || strings.HasPrefix(val, "http://127.0.0.1") {
				return
			}
			out = append(out, s.posf(u, pos,
				"plaintext http:// endpoint; loopback URLs (http://localhost, http://127.0.0.1) are treated as test/dev and not reported"))
		})
	}
	return out
}

// checkSQLAssembly implements SF-4.1. A call to Query/QueryRow/Exec/Ping whose argument
// contains a + concatenation or a SQL-keyword fmt.Sprintf is reported at the call. A
// SQL-keyword Sprintf that is not a direct argument of such a call is still reported, with a
// softer detail, because the link is unclear.
func (s *scanner) checkSQLAssembly() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		fmtName, hasFmt := u.names["fmt"]
		covered := map[token.Pos]bool{} // Sprintf calls already reported through a DB call
		ast.Inspect(u.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !dbCallNames[sel.Sel.Name] {
				return true
			}
			var concat, sprintf bool
			for _, arg := range call.Args {
				ast.Inspect(arg, func(m ast.Node) bool {
					switch m := m.(type) {
					case *ast.BinaryExpr:
						if m.Op == token.ADD {
							concat = true
						}
					case *ast.CallExpr:
						if hasFmt && isCallOn(m, fmtName, "Sprintf") && looksSQL(formatString(m)) {
							sprintf = true
							covered[m.Pos()] = true
						}
					}
					return true
				})
			}
			if concat {
				out = append(out, s.posf(u, call.Pos(), "argument of %s is assembled with + concatenation", sel.Sel.Name))
			}
			if sprintf {
				out = append(out, s.posf(u, call.Pos(), "argument of %s is built with fmt.Sprintf", sel.Sel.Name))
			}
			return true
		})
		if !hasFmt {
			continue
		}
		ast.Inspect(u.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || covered[call.Pos()] || !isCallOn(call, fmtName, "Sprintf") {
				return true
			}
			if looksSQL(formatString(call)) {
				out = append(out, s.posf(u, call.Pos(),
					"fmt.Sprintf builds a SQL-shaped string outside a direct query call; verify it is parameterized before use"))
			}
			return true
		})
	}
	return out
}

// checkShellAssembly implements SF-4.2: exec.Command or exec.CommandContext whose first
// argument is not a string literal, i.e. the command name is assembled at runtime.
func (s *scanner) checkShellAssembly() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		execName, ok := u.names["os/exec"]
		if !ok {
			continue
		}
		ast.Inspect(u.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != execName {
				return true
			}
			if len(call.Args) == 0 {
				out = append(out, s.posf(u, call.Pos(), "exec.%s called without a command argument", sel.Sel.Name))
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				return true
			}
			out = append(out, s.posf(u, call.Pos(),
				"first argument of exec.%s is not a string literal (assembled command)", sel.Sel.Name))
			return true
		})
	}
	return out
}

// hasLogging implements SF-5.1: any import of log or log/slog, or any log.Print*/slog.* call.
func (s *scanner) hasLogging() bool {
	for _, u := range s.units {
		if _, ok := u.names["log"]; ok {
			return true
		}
		if _, ok := u.names["log/slog"]; ok {
			return true
		}
	}
	for _, u := range s.units {
		logName, hasLog := u.names["log"]
		slogName, hasSlog := u.names["log/slog"]
		if !hasLog && !hasSlog {
			continue
		}
		found := false
		ast.Inspect(u.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if hasLog && x.Name == logName && strings.HasPrefix(sel.Sel.Name, "Print") {
				found = true
			}
			if hasSlog && x.Name == slogName {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// checkSecretsInLogs implements SF-5.2: a log.*, slog.* or fmt.Print* call whose argument is
// a string literal containing a secret marker such as password=.
func (s *scanner) checkSecretsInLogs() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		logName, hasLog := u.names["log"]
		slogName, hasSlog := u.names["log/slog"]
		fmtName, hasFmt := u.names["fmt"]
		if !hasLog && !hasSlog && !hasFmt {
			continue
		}
		ast.Inspect(u.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			isLogCall := (hasLog && x.Name == logName) || (hasSlog && x.Name == slogName)
			isPrint := hasFmt && x.Name == fmtName && strings.HasPrefix(sel.Sel.Name, "Print")
			if !isLogCall && !isPrint {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				lower := strings.ToLower(val)
				for _, marker := range logSecretMarkers {
					if strings.Contains(lower, marker) {
						out = append(out, s.posf(u, call.Pos(),
							"%s passes a string literal containing %q to a log or print call", sel.Sel.Name, marker))
						break
					}
				}
			}
			return true
		})
	}
	return out
}

// checkCookies implements SF-6.1: an http.Cookie composite literal (SetCookie arguments are
// such literals) where Secure and HttpOnly are not both true. Only cookies that carry a Name
// and a Domain or Path are reported, which keeps test structs out of the findings.
func (s *scanner) checkCookies() []domain.SecureFinding {
	var out []domain.SecureFinding
	for _, u := range s.units {
		httpName, ok := u.names["net/http"]
		if !ok {
			continue
		}
		ast.Inspect(u.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Cookie" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != httpName {
				return true
			}
			fields := map[string]ast.Expr{}
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok {
						fields[key.Name] = kv.Value
					}
				}
			}
			_, hasName := fields["Name"]
			_, hasDomain := fields["Domain"]
			_, hasPath := fields["Path"]
			if !hasName || !hasDomain && !hasPath {
				return true
			}
			if isTrueIdent(fields["Secure"]) && isTrueIdent(fields["HttpOnly"]) {
				return true
			}
			var missing []string
			if !isTrueIdent(fields["Secure"]) {
				missing = append(missing, "Secure")
			}
			if !isTrueIdent(fields["HttpOnly"]) {
				missing = append(missing, "HttpOnly")
			}
			out = append(out, s.posf(u, lit.Pos(),
				"http.Cookie is issued without the %s attribute(s)", strings.Join(missing, " and ")))
			return true
		})
	}
	return out
}

// runTool runs bin with args in root and maps the outcome onto the checklist statuses:
// exit 0 passes, a nonzero exit fails with the tool's finding lines, a missing binary skips
// with a note, and anything else (startup failure, timeout) skips with a note as well.
func runTool(root, bin string, args []string) (domain.SecureStatus, []domain.SecureFinding, string) {
	binPath, err := exec.LookPath(bin)
	if err != nil {
		return domain.SecureSkip, nil, bin + " not found in PATH"
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		return domain.SecurePass, nil, ""
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return domain.SecureSkip, nil, fmt.Sprintf("%s did not produce a result: %v", bin, err)
	}
	return domain.SecureFail, toolFindings(string(out), exitErr.ExitCode()), ""
}

// toolFindings turns tool output lines into findings, keeping a leading file:line position
// when the tool printed one and clipping the volume to a sane report size. Nothing is
// redacted here: the lines are module paths and rule names.
func toolFindings(out string, code int) []domain.SecureFinding {
	var findings []domain.SecureFinding
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		if m := toolPosRe.FindStringSubmatch(line); m != nil {
			findings = append(findings, domain.SecureFinding{
				Pos:    clip(m[1], toolMaxLineRunes),
				Detail: clip(m[2], toolMaxLineRunes),
			})
		} else {
			findings = append(findings, domain.SecureFinding{Detail: clip(line, toolMaxLineRunes)})
		}
		if len(findings) >= toolMaxLines {
			break
		}
	}
	if len(findings) == 0 {
		findings = append(findings, domain.SecureFinding{
			Detail: fmt.Sprintf("tool exited with status %d and printed no findings", code),
		})
	}
	return findings
}

// clip shortens s to at most max runes.
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// tag returns a copy of findings with the item id set.
func tag(item string, findings []domain.SecureFinding) []domain.SecureFinding {
	out := make([]domain.SecureFinding, len(findings))
	copy(out, findings)
	for i := range out {
		out[i].Item = item
	}
	return out
}
