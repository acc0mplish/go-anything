package usecase

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestValidUnitName(t *testing.T) {
	for _, ok := range []string{"orders", "user_api", "v2"} {
		if err := ValidUnitName(ok); err != nil {
			t.Fatalf("%s must be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Orders", "9lives", "units", "a-b", "a.b", "../x"} {
		if err := ValidUnitName(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestValidPrefix(t *testing.T) {
	if ValidPrefix("/orders/") != nil {
		t.Fatal("/orders/ must be valid")
	}
	for _, bad := range []string{"/orders", "orders/", "/a b/", "/x/../y/", ""} {
		if ValidPrefix(bad) == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestUnitFilesAreValidGo(t *testing.T) {
	files, err := UnitFiles("orders", "/orders/")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("want unit.go, rewrite.go, original.go; got %d", len(files))
	}
	for name, src := range files {
		if _, err := parser.ParseFile(token.NewFileSet(), name, src, parser.AllErrors); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, src)
		}
	}
	if !strings.Contains(string(files["unit.go"]), `const Prefix = "/orders/"`) {
		t.Fatalf("prefix not written: %s", files["unit.go"])
	}
}

func TestRewritePlaceholderIsDetectableByGate(t *testing.T) {
	files, _ := UnitFiles("orders", "/orders/")
	if !strings.Contains(string(files["rewrite.go"]), `panic("not implemented")`) {
		t.Fatal("the generated rewrite must be a placeholder the gate refuses")
	}
}

func TestRegistryMountsEveryUnitAndHasMarker(t *testing.T) {
	src, err := Registry("example.com/app", []UnitRef{{Name: "users", Kind: "http"}, {Name: "orders", Kind: "http"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.HasPrefix(s, RegistryMarker) {
		t.Fatal("registry must start with the ownership marker")
	}
	for _, want := range []string{`"example.com/app/units/orders"`, `"example.com/app/units/users"`,
		`choose(mux, "orders", orders.Prefix`, `GA_UNIT_`, "func RunCLI("} {
		if !strings.Contains(s, want) {
			t.Fatalf("registry missing %q:\n%s", want, s)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "units.go", src, parser.AllErrors); err != nil {
		t.Fatalf("registry does not parse: %v", err)
	}
}

func TestRegistryWithNoUnitsStillCompiles(t *testing.T) {
	src, err := Registry("example.com/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "_ = base") {
		t.Fatal("with no units the base URL must still be used, or the file will not compile")
	}
}

func TestCLIUnitFilesAreValidGo(t *testing.T) {
	files, err := CLIUnitFiles("orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["original.go"]; ok {
		t.Fatal("a command-line unit has no original.go: the original runs as a program")
	}
	for name, src := range files {
		if _, err := parser.ParseFile(token.NewFileSet(), name, src, parser.AllErrors); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, src)
		}
	}
	if !strings.Contains(string(files["rewrite.go"]), "func Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int") {
		t.Fatalf("the CLI contract must be written into rewrite.go:\n%s", files["rewrite.go"])
	}
}

func TestRegistryRoutesCLIUnitsThroughRunCLI(t *testing.T) {
	src, err := Registry("example.com/app", []UnitRef{{Name: "orders", Kind: "cli"}, {Name: "users", Kind: "http"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `case "orders":`) || !strings.Contains(s, `cliChoose("orders", originalBin, orders.Handle`) {
		t.Fatalf("the CLI unit must be dispatched in RunCLI:\n%s", s)
	}
	if strings.Contains(s, `choose(mux, "orders"`) {
		t.Fatal("a CLI unit must not be mounted on the HTTP mux")
	}
	if !strings.Contains(s, `choose(mux, "users", users.Prefix`) {
		t.Fatal("the HTTP unit must still be mounted")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "units.go", src, parser.AllErrors); err != nil {
		t.Fatalf("registry does not parse: %v", err)
	}
}

func TestRegistryHasShadowMode(t *testing.T) {
	src, err := Registry("example.com/app", []UnitRef{{Name: "orders", Kind: "cli"}, {Name: "users", Kind: "http"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{`case "shadow":`, "cliShadow(", "shadowHTTP(", "GA_UNIT_"} {
		if !strings.Contains(s, want) {
			t.Fatalf("shadow mode missing %q", want)
		}
	}
}
