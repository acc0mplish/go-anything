package analyze

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func needPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; the Python analyzer cannot run here")
	}
}

func pkgByPath(t *testing.T, a domain.Analysis, path string) domain.Package {
	t.Helper()
	for _, p := range a.Packages {
		if p.ImportPath == path {
			return p
		}
	}
	var have []string
	for _, p := range a.Packages {
		have = append(have, p.ImportPath)
	}
	t.Fatalf("no unit %q; have %v", path, have)
	return domain.Package{}
}

func hasRisk(p domain.Package, kind, pos string) bool {
	for _, r := range p.RiskSites {
		if r.Kind == kind && r.Pos == pos {
			return true
		}
	}
	return false
}

func TestAnalyzeGoPackagePath(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"shop", "price"}, "internal/shop/price"},
		{[]string{"Shop", "Price_List"}, "internal/shop/pricelist"},
		{[]string{"my-lib", "2fa"}, "internal/mylib/p2fa"},
		{[]string{"type"}, "internal/typepkg"},
		{[]string{"func", "range"}, "internal/funcpkg/rangepkg"},
		{[]string{"testdata", "vendor"}, "internal/testdatapkg/vendorpkg"},
		{[]string{"main"}, "internal/mainpkg"},
		{[]string{"internal", "re"}, "internal/re"},
		{[]string{"lib", "internal", "re"}, "internal/lib/internal/re"},
		{[]string{"internal"}, "internal/internal"},
		{[]string{"__private"}, "internal/private"},
		{[]string{"é"}, "internal/x"},
		{[]string{"tool", "__main__"}, "cmd/tool"},
		{[]string{"__main__"}, "cmd/main"},
		{nil, "internal/x"},
	}
	for _, c := range cases {
		if got := GoPackagePath(c.in); got != c.want {
			t.Errorf("GoPackagePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// pyFixture has three modules, one importing another, one with subprocess and threading, one
// with a syntax error, a test file, and a top-level file whose name has quotes and spaces.
var pyFixture = map[string]string{
	"shop/__init__.py": "",
	"shop/price.py": `TAX = 0.1
_cache = {}


def total(items):
    return sum(items) * (1 + TAX)


def _remember(x):
    _cache[x] = x
`,
	"shop/cart.py": `import subprocess
import threading
from .price import total
from shop import price as p

COUNT = 0


def checkout(items):
    global COUNT
    COUNT += 1
    subprocess.run(["true"])
    return total(items) + p.total(items)


def run_async():
    threading.Thread(target=checkout).start()
`,
	"shop/broken.py":      "def ok():\n    return 1\n\n\ndef bad(:\n    pass\n",
	"tests/test_price.py": "from shop.price import total\n\n\ndef test_total():\n    assert total([1]) > 1\n",
	`odd 'name' "x".py`:   "import time\n\n\ndef stamp():\n    return time.time()\n",
}

func TestAnalyzePythonRepo(t *testing.T) {
	needPython(t)
	root := filepath.Join(t.TempDir(), "my repo") // a space in the path
	writeTree(t, root, pyFixture)
	a, err := Source{}.Analyze(root)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.Language != LangPython || a.Module != "my repo" {
		t.Fatalf("language %q module %q", a.Language, a.Module)
	}
	if len(a.Packages) != 5 {
		t.Fatalf("want 5 modules (shop, price, cart, broken, the odd file), got %d", len(a.Packages))
	}
	price := pkgByPath(t, a, "shop.price")
	cart := pkgByPath(t, a, "shop.cart")
	broken := pkgByPath(t, a, "shop.broken")
	odd := pkgByPath(t, a, `odd 'name' "x"`)

	if price.GoPackage != "internal/shop/price" || cart.GoPackage != "internal/shop/cart" || odd.GoPackage != "internal/oddnamex" {
		t.Errorf("go packages: %q %q %q", price.GoPackage, cart.GoPackage, odd.GoPackage)
	}
	if !slices.Equal(cart.Imports, []string{"shop.price"}) || len(price.Imports) != 0 {
		t.Errorf("imports: cart %v price %v", cart.Imports, price.Imports)
	}
	if !slices.Equal(price.Funcs, []string{"_remember", "total"}) || !slices.Equal(price.Exported, []string{"total"}) {
		t.Errorf("price funcs %v exported %v", price.Funcs, price.Exported)
	}
	if price.Tests != 1 || cart.Tests != 0 {
		t.Errorf("tests: price %d cart %d", price.Tests, cart.Tests)
	}
	if price.Lines != 10 {
		t.Errorf("price lines %d", price.Lines)
	}
	for _, want := range []struct{ kind, pos string }{
		{domain.RiskSideEffects, "shop/cart.py:1"},
		{domain.RiskConcurrency, "shop/cart.py:2"},
		{domain.RiskGlobalMutable, "shop/cart.py:6"},
	} {
		if !hasRisk(cart, want.kind, want.pos) {
			t.Errorf("cart: missing %s at %s in %+v", want.kind, want.pos, cart.RiskSites)
		}
	}
	if !hasRisk(price, domain.RiskGlobalMutable, "shop/price.py:2") {
		t.Errorf("price: _cache is changed in _remember: %+v", price.RiskSites)
	}
	if !hasRisk(odd, domain.RiskNondeterminism, `odd 'name' "x".py:5`) {
		t.Errorf("odd file: time.time: %+v", odd.RiskSites)
	}
	if len(broken.TypeErrors) != 1 || !strings.HasPrefix(broken.TypeErrors[0], "shop/broken.py:5: ") || broken.Lines != 6 {
		t.Errorf("broken: %v lines %d", broken.TypeErrors, broken.Lines)
	}
	// checkout calls total through two names; both resolve to the one function.
	var checkout domain.FuncInfo
	for _, f := range cart.Functions {
		if f.Name == "checkout" {
			checkout = f
		}
	}
	if !slices.Equal(checkout.Callees, []string{"shop.price.total"}) || checkout.Pos != "shop/cart.py:9" {
		t.Errorf("checkout: %+v", checkout)
	}
	if price.Functions[0].Name != "total" || price.Functions[0].Callers != 1 || price.Functions[0].Signature != "(items)" {
		t.Errorf("total: %+v", price.Functions[0])
	}

	rows := domain.PortPlan(a)
	wave := map[string]int{}
	for _, r := range rows {
		wave[r.ImportPath] = r.Wave
	}
	if wave["shop.price"] != 0 || wave["shop.cart"] != 1 {
		t.Errorf("waves: %v", wave)
	}
	for _, r := range rows {
		if r.ImportPath == "shop.broken" && !slices.ContainsFunc(r.Risks, func(s string) bool { return strings.HasPrefix(s, "did not parse") }) {
			t.Errorf("a module that did not parse must say so: %v", r.Risks)
		}
		if r.ImportPath == "shop.cart" && !slices.ContainsFunc(r.Risks, func(s string) bool { return strings.HasPrefix(s, "concurrency") }) {
			t.Errorf("cart risks: %v", r.Risks)
		}
	}

	// start_here: price first (one importer), then the odd file; cart imports, broken did not parse.
	if len(a.StartHere) != 2 || a.StartHere[0].Func != "shop.price.total" || a.StartHere[0].Importers != 1 ||
		a.StartHere[0].Pos != "shop/price.py:5" || a.StartHere[1].Package != `odd 'name' "x"` {
		t.Errorf("start_here: %+v", a.StartHere)
	}
	if len(a.Limits) == 0 || !strings.Contains(strings.Join(a.Limits, " "), "did not parse") {
		t.Errorf("limits must be stated, including the parse failure: %v", a.Limits)
	}
}

func TestAnalyzePythonImportCycle(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.py": "import b\n\n\ndef fa():\n    return b.fb()\n",
		"b.py": "def fb():\n    from a import fa\n    return 1\n",
	})
	a, err := PythonSource{}.Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRisk(pkgByPath(t, a, "a"), domain.RiskImportCycle, "a.py:1") || !hasRisk(pkgByPath(t, a, "b"), domain.RiskImportCycle, "b.py:2") {
		t.Fatalf("both imports of the cycle must be marked: %+v", a.Packages)
	}
	if len(a.StartHere) != 0 {
		t.Fatalf("modules in a cycle import each other, so none is a leaf: %+v", a.StartHere)
	}
}

func TestAnalyzePythonMissingInterpreter(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.py": "x = 1\n"})
	_, err := PythonSource{Python: "python3-does-not-exist"}.Analyze(root)
	if err == nil || !strings.Contains(err.Error(), "python3-does-not-exist not found") {
		t.Fatalf("a missing interpreter must be named: %v", err)
	}
}

func TestAnalyzePythonSkipsEnvironments(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app.py":                    "def run():\n    return 1\n",
		".venv/lib/x.py":            "def hidden(): pass\n",
		"venv/lib/y.py":             "def hidden(): pass\n",
		"node_modules/z.py":         "def hidden(): pass\n",
		"__pycache__/c.py":          "def hidden(): pass\n",
		"build/lib/app.py":          "def hidden(): pass\n",
		".go-anything/reports/r.py": "def hidden(): pass\n",
	})
	a, err := PythonSource{}.Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Packages) != 1 || a.Packages[0].ImportPath != "app" {
		t.Fatalf("only app.py is the program's source: %+v", a.Packages)
	}
}

var jsFixture = map[string]string{
	"package.json": `{"name": "@demo/shop"}`,
	"src/shop/price.ts": `// import { fake } from "./nope"
export function total(items: number[]): number {
  const s = "require('./cart')"; // a string, not an import
  return items.reduce((a, b) => a + b, 0) * 1.1;
}

export const discount = (p: number, pct = 10): number => p - (p * pct) / 100;

const re = /import x from "y"/g;
function internal() { return re.test("a"); }
`,
	"src/shop/cart.js": "const { execSync } = require(\"child_process\");\n" +
		"import { Worker } from \"worker_threads\";\n" +
		"import { total as sum, discount } from \"./price.js\";\n" +
		"import * as fs from \"node:fs\";\n" +
		"\n" +
		"export async function checkout(items) {\n" +
		"  const t = `total ${sum(items)} done`;\n" +
		"  fs.writeFileSync(\"/tmp/x\", t);\n" +
		"  execSync(\"true\");\n" +
		"  return discount(sum(items));\n" +
		"}\n" +
		"\n" +
		"module.exports.stamp = () => Date.now() + Math.random();\n" +
		"const mod = await import(\"./\" + name);\n",
	"src/shop/index.js":          "export { total } from './price';\nexport function hello() { return eval('1'); }\n",
	"src/shop/price.test.ts":     "import { total } from './price';\n",
	"src/shop/__tests__/cart.js": "require('../cart');\n",
	"node_modules/dep/index.js":  "export function hidden() {}\n",
	"dist/bundle.js":             "export function hidden() {}\n",
	"src/types.d.ts":             "export declare function typed(): void;\n",
	`src/odd name's "file".js`:   "exports.go = function (a) { return fetch(a); };\n",
}

func TestAnalyzeJSRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "js repo")
	writeTree(t, root, jsFixture)
	a, err := Source{}.Analyze(root)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.Language != LangJS || a.Module != "@demo/shop" || len(a.Limits) == 0 {
		t.Fatalf("language %q module %q limits %d", a.Language, a.Module, len(a.Limits))
	}
	if len(a.Packages) != 4 {
		t.Fatalf("want price, cart, index and the odd file, got %+v", a.Packages)
	}
	price := pkgByPath(t, a, "src/shop/price")
	cart := pkgByPath(t, a, "src/shop/cart")
	index := pkgByPath(t, a, "src/shop/index")
	odd := pkgByPath(t, a, `src/odd name's "file"`)
	if price.GoPackage != "internal/shop/price" || cart.GoPackage != "internal/shop/cart" || index.GoPackage != "internal/shop" {
		t.Errorf("go packages: %q %q %q", price.GoPackage, cart.GoPackage, index.GoPackage)
	}
	if len(price.Imports) != 0 || !slices.Equal(cart.Imports, []string{"src/shop/price"}) || !slices.Equal(index.Imports, []string{"src/shop/price"}) {
		t.Errorf("imports: price %v cart %v index %v", price.Imports, cart.Imports, index.Imports)
	}
	if !slices.Equal(price.Exported, []string{"discount", "total"}) || !slices.Equal(price.Funcs, []string{"discount", "internal", "total"}) {
		t.Errorf("price exported %v funcs %v", price.Exported, price.Funcs)
	}
	if price.Tests != 1 || cart.Tests != 1 {
		t.Errorf("tests: price %d cart %d", price.Tests, cart.Tests)
	}
	if !slices.Equal(odd.Exported, []string{"go"}) || !hasRisk(odd, domain.RiskNetwork, `src/odd name's "file".js:1`) {
		t.Errorf("odd file: %+v", odd)
	}
	for _, want := range []struct{ kind, pos string }{
		{domain.RiskSideEffects, "src/shop/cart.js:1"},
		{domain.RiskConcurrency, "src/shop/cart.js:2"},
		{domain.RiskFSWrite, "src/shop/cart.js:8"},
		{domain.RiskNondeterminism, "src/shop/cart.js:13"},
		{domain.RiskDynamicCode, "src/shop/cart.js:14"},
	} {
		if !hasRisk(cart, want.kind, want.pos) {
			t.Errorf("cart: missing %s at %s in %+v", want.kind, want.pos, cart.RiskSites)
		}
	}
	if !hasRisk(index, domain.RiskDynamicCode, "src/shop/index.js:2") {
		t.Errorf("index: eval: %+v", index.RiskSites)
	}
	if len(price.RiskSites) != 0 {
		t.Errorf("strings, comments and regular expressions must not produce findings: %+v", price.RiskSites)
	}
	var checkout domain.FuncInfo
	for _, f := range cart.Functions {
		if f.Name == "checkout" {
			checkout = f
		}
	}
	if !checkout.Exported || checkout.Pos != "src/shop/cart.js:6" ||
		!slices.Equal(checkout.Callees, []string{"src/shop/price.discount", "src/shop/price.total"}) {
		t.Errorf("checkout: %+v", checkout)
	}
	wave := map[string]int{}
	for _, r := range domain.PortPlan(a) {
		wave[r.ImportPath] = r.Wave
	}
	if wave["src/shop/price"] != 0 || wave["src/shop/cart"] != 1 {
		t.Errorf("waves: %v", wave)
	}
	// price is imported by cart and index; the odd file is a leaf with fetch (network), which
	// does not block a first port.
	if len(a.StartHere) < 2 || a.StartHere[0].Package != "src/shop/price" || a.StartHere[0].Importers != 2 {
		t.Errorf("start_here: %+v", a.StartHere)
	}
	for _, s := range a.StartHere {
		if s.Package == "src/shop/index" || s.Package == "src/shop/cart" {
			t.Errorf("index has eval and cart imports a repo module; neither is a first port: %+v", s)
		}
	}
}

func TestAnalyzeJSMaskKeepsOffsets(t *testing.T) {
	src := "const a = 'x // y'; // c\nconst r = /[/]\"/g; /* m\n */ const t = `a ${b + `c`} d`;\nx = a / b / c;\n"
	m := maskJS([]byte(src))
	if len(m) != len(src) || strings.Count(string(m), "\n") != strings.Count(src, "\n") {
		t.Fatal("masking must keep every offset and newline")
	}
	got := string(m)
	for _, gone := range []string{"x // y", "// c", "[/]", "m\n */", "c`"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q must be blanked: %q", gone, got)
		}
	}
	for _, kept := range []string{"${b + `", "x = a / b / c;"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q must stay: %q", kept, got)
		}
	}
}

func TestAnalyzeDetectLanguage(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
		err   string
	}{
		{"go.mod wins", map[string]string{"go.mod": "module m\n", "a.py": "", "b.py": ""}, LangGo, ""},
		{"more python", map[string]string{"a.py": "", "b.py": "", "c.js": ""}, LangPython, ""},
		{"more js", map[string]string{"a.py": "", "b.ts": "", "c.tsx": "", "d.d.ts": "", "e.d.ts": ""}, LangJS, ""},
		{"dependencies do not count", map[string]string{"a.js": "", "node_modules/x/a.py": "", "node_modules/x/b.py": "", ".venv/c.py": ""}, LangJS, ""},
		{"tie", map[string]string{"a.py": "", "b.js": ""}, "", "-lang python or -lang js"},
		{"empty", map[string]string{"README.md": ""}, "", "supported: go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, c.files)
			got, err := DetectLanguage(root)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("want error with %q, got %q, %v", c.err, got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := (Source{Lang: "cobol"}).Analyze(t.TempDir()); err == nil || !strings.Contains(err.Error(), "auto, go, python or js") {
		t.Fatalf("an unknown language must list the supported ones: %v", err)
	}
}

func TestAnalyzeLangOverride(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.js": "export function f() {}\n", "b.js": "", "tool.py": "def run():\n    pass\n"})
	if a, err := (Source{}).Analyze(root); err != nil || a.Language != LangJS {
		t.Fatalf("auto picks js: %v %v", a.Language, err)
	}
	a, err := Source{Lang: LangPython}.Analyze(root)
	if err != nil || a.Language != LangPython || len(a.Packages) != 1 || a.Packages[0].ImportPath != "tool" {
		t.Fatalf("-lang python must analyze the Python files: %+v %v", a, err)
	}
}
