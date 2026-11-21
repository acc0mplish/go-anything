package layout

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeModule creates a module in a temp dir from path -> content.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// snapshot lists every path under root with its size, to prove a call wrote nothing.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %v %d %d", p, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const goMod = "module example.com/shipme\n\ngo 1.22\n"

// twoMains is a pure-Go module with two binaries, a library and a cgo generator that never builds.
func twoMains() map[string]string {
	return map[string]string{
		"go.mod":              goMod,
		"cmd/alpha/main.go":   "package main\n\nimport \"example.com/shipme/lib\"\n\nvar version = \"dev\"\n\nfunc main() { println(lib.Hello(), version) }\n",
		"tools/beta/main.go":  "package main\n\nconst version = \"1.0\"\n\nfunc main() { println(version) }\n",
		"lib/lib.go":          "package lib\n\nfunc Hello() string { return \"hi\" }\n",
		"lib/gen_types.go":    "//go:build ignore\n\npackage lib\n\n// #include <stdio.h>\nimport \"C\"\n",
		"internal/x/x_win.go": "//go:build windows\n\npackage x\n",
	}
}

func TestPlanFindsBothMains(t *testing.T) {
	root := writeModule(t, twoMains())
	before := snapshot(t, root)
	p, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Module != "example.com/shipme" || p.GoVersion != "1.22" {
		t.Fatalf("module %q go %q", p.Module, p.GoVersion)
	}
	if len(p.Mains) != 2 {
		t.Fatalf("mains = %+v, want 2", p.Mains)
	}
	a, b := p.Mains[0], p.Mains[1]
	if a.ImportPath != "example.com/shipme/cmd/alpha" || a.Dir != "cmd/alpha" || a.BinName != "alpha" || !a.VersionVar {
		t.Errorf("alpha = %+v", a)
	}
	if b.ImportPath != "example.com/shipme/tools/beta" || b.Dir != "tools/beta" || b.BinName != "beta" || b.VersionVar {
		t.Errorf("beta = %+v", b)
	}
	if p.UsesCgo {
		t.Errorf("an //go:build ignore generator importing C must not count as cgo: %v", p.CgoFiles)
	}
	if !slices.Equal(p.Targets, DefaultTargets) {
		t.Errorf("targets = %v", p.Targets)
	}
	if !hasNote(p, "tools/beta: -X main.version has no effect because version is a const") {
		t.Errorf("missing const version note: %q", p.Notes)
	}
	install := fileContent(t, p, "docs/INSTALL.md")
	for _, want := range []string{"go install example.com/shipme/cmd/alpha@latest", "go install example.com/shipme/tools/beta@latest", "Go 1.22"} {
		if !strings.Contains(install, want) {
			t.Errorf("INSTALL.md lacks %q", want)
		}
	}
	if !slices.Equal(before, snapshot(t, root)) {
		t.Fatal("Plan wrote to the module")
	}
}

func TestPlanDetectsCgoAndFallsBackToHost(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":  goMod,
		"main.go": "package main\n\n// int two(void) { return 2; }\nimport \"C\"\n\nfunc main() { println(C.two()) }\n",
	})
	host := Target{"linux", "arm64"}
	p, err := Plan(context.Background(), root, Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if !p.UsesCgo || !slices.Equal(p.CgoFiles, []string{"main.go"}) {
		t.Fatalf("cgo = %v files %v", p.UsesCgo, p.CgoFiles)
	}
	if !slices.Equal(p.Targets, []Target{host}) {
		t.Fatalf("targets = %v, want host only", p.Targets)
	}
	if p.Mains[0].BinName != "shipme" || p.Mains[0].Dir != "." {
		t.Errorf("root main = %+v", p.Mains[0])
	}
	if !hasNote(p, "cgo is used (main.go)") {
		t.Errorf("notes = %q", p.Notes)
	}
	if !strings.Contains(fileContent(t, p, "docs/INSTALL.md"), "uses cgo (main.go)") {
		t.Error("INSTALL.md does not say why only the host is built")
	}
	wf := fileContent(t, p, ".github/workflows/release.yml")
	if !strings.Contains(wf, `CGO_ENABLED: "1"`) || !strings.Contains(wf, "runner: ubuntu-24.04-arm") {
		t.Errorf("cgo workflow should build natively:\n%s", wf)
	}
	if strings.Contains(fileContent(t, p, "scripts/build-all.sh"), "export CGO_ENABLED=0") {
		t.Error("cgo build script must not force CGO_ENABLED=0")
	}
}

func TestPlanRefusesUnknownTargetAndNoMain(t *testing.T) {
	root := writeModule(t, twoMains())
	if _, err := Plan(context.Background(), root, Options{Targets: []Target{{"linux", "z80"}}}); err == nil || !strings.Contains(err.Error(), "linux/z80") {
		t.Fatalf("err = %v", err)
	}
	lib := writeModule(t, map[string]string{"go.mod": goMod, "lib/lib.go": "package lib\n"})
	if _, err := Plan(context.Background(), lib, Options{}); err == nil || !strings.Contains(err.Error(), "no package main") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseTargets("linux"); err == nil {
		t.Fatal("ParseTargets accepted a target without GOARCH")
	}
	ts, err := ParseTargets(" linux/amd64 ,windows/arm64,")
	if err != nil || !slices.Equal(ts, []Target{{"linux", "amd64"}, {"windows", "arm64"}}) {
		t.Fatalf("ParseTargets = %v %v", ts, err)
	}
}

func TestPlanNotesReplaceAndMajorSuffix(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":          "module example.com/tool/v2\n\ngo 1.22\n\nreplace example.com/x => ../x\n",
		"cmd/v2/main.go":  "package main\n\nfunc main() {}\n",
		"cmd/run/main.go": "package main\n\nvar version string\n\nfunc main() {}\n",
	})
	p, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{p.Mains[0].BinName, p.Mains[1].BinName}
	if !slices.Equal(names, []string{"run", "cmd"}) {
		t.Errorf("bin names = %v, want run and cmd (a /v2 suffix is skipped)", names)
	}
	if !p.Mains[0].VersionVar {
		t.Error("var version string must be settable")
	}
	if !hasNote(p, "replace or exclude") {
		t.Errorf("notes = %q", p.Notes)
	}
}

func TestWriteKeepsExistingFiles(t *testing.T) {
	files := twoMains()
	files["docs/INSTALL.md"] = "hand written\n"
	root := writeModule(t, files)
	p, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Write(p)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range res {
		got[r.Path] = r.Created
	}
	want := map[string]bool{".github/workflows/release.yml": true, "scripts/build-all.sh": true, "docs/INSTALL.md": false}
	for k, v := range want {
		if c, ok := got[k]; !ok || c != v {
			t.Errorf("%s created=%v (present %v), want %v", k, c, ok, v)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "docs/INSTALL.md")); string(b) != "hand written\n" {
		t.Fatalf("existing file changed: %q", b)
	}
	if fi, err := os.Stat(filepath.Join(root, "scripts/build-all.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("build-all.sh not executable: %v %v", fi, err)
	}

	// A second run plans every file as kept and changes nothing.
	before := snapshot(t, root)
	p2, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p2.Files {
		if !f.Exists {
			t.Errorf("%s not reported as existing", f.Path)
		}
	}
	if _, err := Write(p2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, snapshot(t, root)) {
		t.Fatal("second Write changed files")
	}

	// A file that appears after Plan is kept too (O_EXCL), not overwritten.
	root3 := writeModule(t, twoMains())
	p3, err := Plan(context.Background(), root3, Options{})
	if err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(root3, "scripts", "build-all.sh")
	if err := os.MkdirAll(filepath.Dir(late), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(late, []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(p3); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(late); string(b) != "late\n" {
		t.Fatalf("late file overwritten: %q", b)
	}
}

// runBlocks returns the dedented bodies of every `run: |` block in a workflow.
func runBlocks(t *testing.T, yml string) []string {
	t.Helper()
	lines := strings.Split(yml, "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) != "run: |" {
			continue
		}
		keyIndent := len(l) - len(strings.TrimLeft(l, " "))
		var body []string
		bodyIndent := -1
		for i+1 < len(lines) {
			n := lines[i+1]
			ind := len(n) - len(strings.TrimLeft(n, " "))
			if strings.TrimSpace(n) != "" && ind <= keyIndent {
				break
			}
			i++
			if strings.TrimSpace(n) == "" {
				body = append(body, "")
				continue
			}
			if bodyIndent < 0 {
				bodyIndent = ind
			}
			body = append(body, n[bodyIndent:])
		}
		blocks = append(blocks, strings.Join(body, "\n"))
	}
	return blocks
}

func TestWorkflowShapeAndSecurity(t *testing.T) {
	p, err := Plan(context.Background(), writeModule(t, twoMains()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	yml := fileContent(t, p, ".github/workflows/release.yml")
	for _, want := range []string{
		"name: release\n",
		"on:\n  push:\n    tags:\n      - \"v*\"\n",
		"permissions:\n  contents: read\n",
		"    permissions:\n      contents: write\n",
		"uses: actions/checkout@v4\n        with:\n          persist-credentials: false\n",
		"uses: actions/setup-go@v5\n        with:\n          go-version-file: go.mod\n",
		"CGO_ENABLED: \"0\"\n",
		"VERSION: ${{ github.ref_name }}\n",
		"GH_TOKEN: ${{ github.token }}\n",
		`go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name/alpha$ext" "./cmd/alpha"`,
		`go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name/beta$ext" "./tools/beta"`,
		"sha256sum -- \"${files[@]}\" > SHA256SUMS\n",
		"gh release create \"$VERSION\"",
	} {
		if !strings.Contains(yml, want) {
			t.Errorf("workflow lacks %q", want)
		}
	}
	for _, tg := range DefaultTargets {
		if !strings.Contains(yml, "- goos: "+tg.GOOS+"\n            goarch: "+tg.GOARCH+"\n") {
			t.Errorf("matrix lacks %s", tg)
		}
	}
	for _, bad := range []string{"pull_request_target", "pull_request", "workflow_run", "write-all", "packages: write", "id-token", "\t"} {
		if strings.Contains(yml, bad) {
			t.Errorf("workflow contains %q", bad)
		}
	}
	// Plain YAML structure: two-space indentation, every non-comment line is a key, a list item,
	// or part of a block scalar.
	inBlock, blockIndent := false, 0
	for n, l := range strings.Split(yml, "\n") {
		ind := len(l) - len(strings.TrimLeft(l, " "))
		trim := strings.TrimSpace(l)
		if inBlock && (trim == "" || ind > blockIndent) {
			continue
		}
		inBlock = false
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if ind%2 != 0 {
			t.Errorf("line %d: odd indentation: %q", n+1, l)
		}
		if !strings.HasPrefix(trim, "- ") && !regexp.MustCompile(`^[A-Za-z_-]+:( |$)`).MatchString(trim) {
			t.Errorf("line %d is not a key or list item: %q", n+1, l)
		}
		if strings.HasSuffix(trim, ": |") {
			inBlock, blockIndent = true, ind
		}
	}
	// No ${{ }} expression may be expanded inside a shell script.
	blocks := runBlocks(t, yml)
	if len(blocks) != 2 {
		t.Fatalf("found %d run blocks, want 2", len(blocks))
	}
	for _, b := range blocks {
		if strings.Contains(b, "${{") {
			t.Errorf("run block interpolates an expression:\n%s", b)
		}
	}
	// Every action is pinned to a major version.
	for _, m := range regexp.MustCompile(`uses: (\S+)`).FindAllStringSubmatch(yml, -1) {
		if !regexp.MustCompile(`^actions/[a-z-]+@v[0-9]+$`).MatchString(m[1]) {
			t.Errorf("action %q is not a GitHub action pinned by major version", m[1])
		}
	}
}

// TestWorkflowScriptsRunLocally executes the two run: blocks of the workflow with bash: the build
// step for the host platform, then the release step against a fake gh that records its arguments.
func TestWorkflowScriptsRunLocally(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	root := writeModule(t, twoMains())
	p, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	blocks := runBlocks(t, fileContent(t, p, ".github/workflows/release.yml"))
	env := append(os.Environ(), "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0", "VERSION=v1.2.3")
	run := func(script string, env []string) (string, error) {
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = root
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// An injection attempt in the tag name is refused before it reaches the shell.
	evil := append(slices.Clone(env), "VERSION=v1$(touch${IFS}pwned)")
	if out, err := run(blocks[0], evil); err == nil || !strings.Contains(out, "refusing to build") {
		t.Fatalf("evil tag built: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
		t.Fatal("tag name was executed")
	}

	if out, err := run(blocks[0], env); err != nil {
		t.Fatalf("build step: %v\n%s", err, out)
	}
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	archive := "shipme_v1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + ext
	if _, err := os.Stat(filepath.Join(root, "dist", archive)); err != nil {
		t.Fatalf("build step made no archive: %v", err)
	}
	if runtime.GOOS != "windows" {
		out, err := exec.Command("tar", "-tzf", filepath.Join(root, "dist", archive)).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "/alpha") || !strings.Contains(string(out), "/beta") {
			t.Fatalf("archive content: %v\n%s", err, out)
		}
	}

	bin := t.TempDir()
	logFile := filepath.Join(bin, "gh.log")
	fake := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + logFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	relEnv := append(slices.Clone(env), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GH_TOKEN=x", "GITHUB_REPOSITORY=owner/shipme")
	if out, err := run(blocks[1], relEnv); err != nil {
		t.Fatalf("release step: %v\n%s", err, out)
	}
	sums, err := os.ReadFile(filepath.Join(root, "dist", "SHA256SUMS"))
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{64}  `+regexp.QuoteMeta(archive)+"\n$").Match(sums) {
		t.Fatalf("SHA256SUMS = %q %v", sums, err)
	}
	args, _ := os.ReadFile(logFile)
	want := strings.Join([]string{"release", "create", "v1.2.3", archive, "SHA256SUMS", "--repo", "owner/shipme", "--title", "v1.2.3", "--generate-notes", "--verify-tag"}, "\n") + "\n"
	if string(args) != want {
		t.Fatalf("gh args:\n%s\nwant:\n%s", args, want)
	}
}

func TestBuildAllScriptRuns(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	sum, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum not installed")
	}
	root := writeModule(t, twoMains())
	p, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(p); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "build-all.sh")
	if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	if !strings.Contains(fileContent(t, p, "scripts/build-all.sh"), "set -euo pipefail\n") {
		t.Error("script lacks set -euo pipefail")
	}
	cmd := exec.Command(script)
	cmd.Dir = t.TempDir() // the script must find the module from its own path
	host := runtime.GOOS + "/" + runtime.GOARCH
	cmd.Env = append(os.Environ(), "VERSION=v0.1.0", "TARGETS="+host)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build-all.sh: %v\n%s", err, out)
	}
	check := exec.Command(sum, "-c", "SHA256SUMS")
	check.Dir = filepath.Join(root, "dist")
	if out, err := check.CombinedOutput(); err != nil || !strings.Contains(string(out), "shipme_v0.1.0_"+runtime.GOOS+"_"+runtime.GOARCH) {
		t.Fatalf("sha256sum -c: %v\n%s", err, out)
	}
	bad := exec.Command(script)
	bad.Env = append(os.Environ(), "VERSION=1;rm", "TARGETS="+host)
	if out, err := bad.CombinedOutput(); err == nil {
		t.Fatalf("unsafe VERSION accepted:\n%s", out)
	}
}

// TestCheckBuildsAllDefaultTargets really cross-compiles both mains for all five default targets.
func TestCheckBuildsAllDefaultTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles five targets")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	p, err := Plan(ctx, writeModule(t, twoMains()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := Check(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("check of %d targets took %s", len(res), time.Since(start).Round(time.Millisecond))
	if len(res) != len(DefaultTargets) {
		t.Fatalf("%d results", len(res))
	}
	for _, r := range res {
		t.Logf("%s ok=%v %s %+v", r.Target, r.OK, r.Duration.Round(time.Millisecond), r.Binaries)
		if !r.OK || len(r.Binaries) != 2 {
			t.Errorf("%s failed: %s", r.Target, r.ErrTail)
		}
		for _, b := range r.Binaries {
			if b.Format == "" || !b.Match || b.Size == 0 {
				t.Errorf("%s %s: format %q match %v size %d", r.Target, b.Name, b.Format, b.Match, b.Size)
			}
		}
	}
}

func TestCheckReportsFailureTail(t *testing.T) {
	files := twoMains()
	files["cmd/alpha/broken_windows.go"] = "package main\n\nvar _ = undefinedOnWindows\n"
	p, err := Plan(context.Background(), writeModule(t, files), Options{Targets: []Target{{runtime.GOOS, runtime.GOARCH}, {"windows", "amd64"}}})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the broken file targets windows, which is the host here")
	}
	res, err := Check(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].OK {
		t.Errorf("host build failed: %s", res[0].ErrTail)
	}
	if res[1].OK || !strings.Contains(res[1].ErrTail, "undefinedOnWindows") {
		t.Errorf("windows result = %+v", res[1])
	}
}

func TestInspectRejectsWrongPlatform(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := Target{runtime.GOOS, runtime.GOARCH}
	if f, ok := inspect(exe, host); !ok || f == "" {
		t.Fatalf("host binary: %q %v", f, ok)
	}
	other := Target{"plan9", runtime.GOARCH}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		other = Target{"windows", runtime.GOARCH}
	}
	if f, ok := inspect(exe, other); ok {
		t.Fatalf("host binary %q accepted as %s", f, other)
	}
}

func hasNote(p *LayoutPlan, sub string) bool {
	for _, n := range p.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func fileContent(t *testing.T, p *LayoutPlan, path string) string {
	t.Helper()
	for _, f := range p.Files {
		if f.Path == path {
			return f.Content
		}
	}
	t.Fatalf("plan has no %s", path)
	return ""
}

func TestPlanNotesUnignoredDist(t *testing.T) {
	files := twoMains()
	root := writeModule(t, files)
	p, err := Plan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasNote(p, "Add `/dist/`") {
		t.Errorf("no dist note without .gitignore: %q", p.Notes)
	}
	files[".gitignore"] = "*.test\n/dist/\n"
	p, err = Plan(context.Background(), writeModule(t, files), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hasNote(p, "Add `/dist/`") {
		t.Errorf("dist note despite /dist/ in .gitignore: %q", p.Notes)
	}
}
