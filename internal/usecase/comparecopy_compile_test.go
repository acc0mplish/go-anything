package usecase

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestComparisonCopyCompilesInAFreshModule is the copy-machine gate: the copied comparison files are
// written into a throwaway module and must pass go vet there, which type-checks the test files. A new
// top-level identifier in internal/domain/compare.go or answer.go that the prefixer mishandles shows up
// here as a compile error, not as a collision in a generated unit test after the fact.
func TestComparisonCopyCompilesInAFreshModule(t *testing.T) {
	if testing.Short() {
		t.Skip("go vet in a fresh module is slow; skipped with -short")
	}
	files, err := ComparisonTestFiles("orders")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.27.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.go"), []byte("package orders\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the copied comparison files must compile in a fresh module: %v\n%s", err, out)
	}
	// The copy must not leave an unprefixed top-level name behind: a rewrite declaring the same name in
	// the same package would collide with it.
	fset := token.NewFileSet()
	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("generated %s must parse: %v", name, err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && !strings.HasPrefix(d.Name.Name, ComparisonPrefix) {
					t.Errorf("generated %s declares unprefixed top-level function %s", name, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if !strings.HasPrefix(s.Name.Name, ComparisonPrefix) {
							t.Errorf("generated %s declares unprefixed type %s", name, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if !strings.HasPrefix(n.Name, ComparisonPrefix) {
								t.Errorf("generated %s declares unprefixed value %s", name, n.Name)
							}
						}
					}
				}
			}
		}
	}
}
