package wiregen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Markers written into main.go, so a second wire gen does not add the same code again.
const (
	MountMarker = "// go-anything: units mounted (wire gen)"
	CLIMarker   = "// go-anything: command-line units routed (wire gen)"
)

// WireMain puts the code that reaches a unit into the program's main.go and checks that the module
// still builds. An HTTP unit gets a mount before the http.ListenAndServe call; a command-line unit gets
// a RunCLI check at the start of main. It restores the original file when the build fails, and it
// returns false when the code is already there.
func WireMain(root, mainFile, module, kind string) (bool, error) {
	path := filepath.Join(root, mainFile)
	orig, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("wire: read %s: %w", mainFile, err)
	}
	marker := MountMarker
	insert := insertMount
	if kind == "cli" {
		marker = CLIMarker
		insert = insertCLIMount
	}
	if bytes.Contains(orig, []byte(marker)) {
		return false, nil
	}
	updated, err := insert(orig, module)
	if err != nil {
		return false, fmt.Errorf("wire: %s: %w (wire it by hand: see the instructions above)", mainFile, err)
	}
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return false, fmt.Errorf("wire: write %s: %w", mainFile, err)
	}
	if out, err := buildModule(root); err != nil {
		if rerr := os.WriteFile(path, orig, 0o644); rerr != nil {
			return false, fmt.Errorf("wire: build failed and the original could not be restored (%v): %w", rerr, err)
		}
		return false, fmt.Errorf("wire: %s did not build with the change, so it was restored unchanged: %w\n%s", mainFile, err, out)
	}
	return true, nil
}

// parseMain parses a main package and returns its syntax tree, token positions and source lines.
func parseMain(src []byte) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("does not parse: %v", err)
	}
	if f.Name.Name != "main" {
		return nil, nil, fmt.Errorf("not a main package")
	}
	return f, fset, nil
}

// insertMount adds the HTTP mount before the line that calls http.ListenAndServe, plus its imports.
func insertMount(src []byte, module string) ([]byte, error) {
	f, fset, err := parseMain(src)
	if err != nil {
		return nil, err
	}
	var call *ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || call != nil {
			return call == nil
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "ListenAndServe" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "http" && len(c.Args) == 2 {
				call = c
				return false
			}
		}
		return true
	})
	if call == nil {
		return nil, fmt.Errorf("no http.ListenAndServe(addr, handler) call found")
	}
	mux := ""
	switch h := call.Args[1].(type) {
	case *ast.Ident:
		if h.Name == "nil" {
			mux = "http.DefaultServeMux"
		} else {
			mux = h.Name
		}
	default:
		return nil, fmt.Errorf("the handler is an expression, not a variable; pass a mux variable to ListenAndServe")
	}
	lines := strings.SplitAfter(string(src), "\n")
	callLine := fset.Position(call.Pos()).Line
	indent := leadingSpace(lines[callLine-1])
	block := indent + MountMarker + "\n" +
		indent + "if err := units.Mount(" + mux + `, os.Getenv("GA_ORIGINAL_URL")); err != nil {` + "\n" +
		indent + "\tlog.Fatal(err)\n" + indent + "}\n"
	return []byte(addImports(insertBefore(lines, callLine, block), f, module, []string{"log", "os"})), nil
}

// insertCLIMount adds the command-line routing as the first statement of main, plus its imports.
// RunCLI handles a unit's command and returns false for everything else, so the rest of main is unchanged.
func insertCLIMount(src []byte, module string) ([]byte, error) {
	f, fset, err := parseMain(src)
	if err != nil {
		return nil, err
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			body = fd.Body
		}
	}
	if body == nil {
		return nil, fmt.Errorf("no func main found")
	}
	lines := strings.SplitAfter(string(src), "\n")
	openLine := fset.Position(body.Lbrace).Line
	block := "\t" + CLIMarker + "\n" +
		"\tif code, handled := units.RunCLI(os.Getenv(\"GA_ORIGINAL_CLI\"), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {\n" +
		"\t\tos.Exit(code)\n\t}\n"
	out := make([]string, 0, len(lines)+4)
	out = append(out, lines[:openLine]...)
	out = append(out, block)
	out = append(out, lines[openLine:]...)
	return []byte(addImports(strings.Join(out, ""), f, module, []string{"os"})), nil
}

func insertBefore(lines []string, line int, block string) string {
	var out strings.Builder
	for i, l := range lines {
		if i == line-1 {
			out.WriteString(block)
		}
		out.WriteString(l)
	}
	return out.String()
}

// addImports adds the imports the inserted code needs, plus the unit registry. They go into the
// existing import block when there is one, so the file keeps a single sorted list.
func addImports(src string, f *ast.File, module string, extra []string) string {
	have := map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		have[p] = true
	}
	var need []string
	for _, p := range append(append([]string(nil), extra...), module+"/units") {
		if !have[p] {
			need = append(need, p)
		}
	}
	if len(need) == 0 {
		return src
	}
	lines := strings.SplitAfter(src, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "import (" {
			var b strings.Builder
			for _, l2 := range lines[:i+1] {
				b.WriteString(l2)
			}
			for _, p := range need {
				fmt.Fprintf(&b, "\t%q\n", p)
			}
			for _, l2 := range lines[i+1:] {
				b.WriteString(l2)
			}
			return formatSource(b.String())
		}
	}
	var b strings.Builder
	b.WriteString("\nimport (\n")
	for _, p := range need {
		fmt.Fprintf(&b, "\t%q\n", p)
	}
	b.WriteString(")\n")
	pkgStart := strings.Index(src, "package ")
	pkgEnd := pkgStart + strings.Index(src[pkgStart:], "\n")
	return formatSource(src[:pkgEnd+1] + b.String() + src[pkgEnd+1:])
}

// formatSource sorts and groups imports the way gofmt does. On failure it returns the input.
func formatSource(src string) string {
	out, err := format.Source([]byte(src))
	if err != nil {
		return src
	}
	return string(out)
}

func leadingSpace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// buildModule runs go build for the whole module and returns its output on failure.
func buildModule(root string) (string, error) {
	// go build writes a binary into the current directory when ./... matches a single main package.
	// Building into a temp directory keeps the user's module free of build output.
	outDir, err := os.MkdirTemp("", "ga-build-")
	if err != nil {
		return "", fmt.Errorf("go build: temp dir: %w", err)
	}
	defer os.RemoveAll(outDir)
	cmd := exec.Command("go", "build", "-o", outDir+string(os.PathSeparator), "./...")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("go build failed: %w", err)
	}
	return "", nil
}
