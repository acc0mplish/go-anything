package wiregen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.27.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGenWritesUnitAndRegistryThatBuild(t *testing.T) {
	root := newModule(t)
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	if _, err := Gen(root, "users", "/users/"); err != nil {
		t.Fatal(err)
	}
	units, _ := ListUnits(root)
	if len(units) != 2 || units[0].Name != "orders" || units[1].Prefix != "/users/" {
		t.Fatalf("both units must be listed with prefixes: %+v", units)
	}
	// the generated module must compile as it is: the placeholders are valid Go
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated module does not build: %v\n%s", err, out)
	}
}

func TestGenRefusesExistingUnit(t *testing.T) {
	root := newModule(t)
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	if _, err := Gen(root, "orders", "/other/"); err == nil {
		t.Fatal("a second wire of the same unit must be refused, never overwritten")
	}
}

func TestGenRefusesDuplicatePrefix(t *testing.T) {
	root := newModule(t)
	Gen(root, "orders", "/api/")
	if _, err := Gen(root, "users", "/api/"); err == nil {
		t.Fatal("two units on one prefix must be refused")
	}
}

func TestGenRefusesHandWrittenRegistry(t *testing.T) {
	root := newModule(t)
	os.MkdirAll(filepath.Join(root, "units"), 0o755)
	os.WriteFile(filepath.Join(root, "units", "units.go"), []byte("package units\n// mine\n"), 0o644)
	if _, err := Gen(root, "orders", "/orders/"); err == nil || !strings.Contains(err.Error(), "not written by wire gen") {
		t.Fatalf("a hand-written registry must be left alone: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "units", "units.go"))
	if !strings.Contains(string(data), "// mine") {
		t.Fatal("the hand-written registry must be untouched")
	}
}

func TestGenRefusesBadNames(t *testing.T) {
	root := newModule(t)
	if _, err := Gen(root, "../evil", "/x/"); err == nil {
		t.Fatal("a path-like unit name must be refused")
	}
	if _, err := Gen(root, "orders", "orders"); err == nil {
		t.Fatal("a prefix without slashes must be refused")
	}
}
