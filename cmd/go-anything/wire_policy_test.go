package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProjectConfig puts a settings file at <root>/.go-anything/config.json and returns the root.
func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".go-anything")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// refineStart runs `wire refine` up to its start line. The module root has no go.mod, so Refine fails right
// after the start line is printed, which keeps the test off the Go toolchain.
func refineStart(t *testing.T, root string, extra ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"wire", "refine", root, "orders", "-cases", "app.json", "-original-cli", "orig"}, extra...)
	err := run(args, strings.NewReader(""), &out, io.Discard)
	return out.String(), err
}

func TestRefineStartLineShowsFlagOverFile(t *testing.T) {
	root := writeProjectConfig(t, `{"defect_policy":"expected"}`)
	out, _ := refineStart(t, root, "-defect-policy", "defect")
	want := "wire refine: orders defect policy defect (from -defect-policy flag)"
	if !strings.Contains(out, want) {
		t.Fatalf("the flag must win over the file; want %q in:\n%s", want, out)
	}
}

func TestRefineStartLineShowsFileWhenNoFlag(t *testing.T) {
	root := writeProjectConfig(t, `{"defect_policy":"expected"}`)
	out, _ := refineStart(t, root)
	if !strings.Contains(out, "wire refine: orders defect policy expected (from defect_policy in ") {
		t.Fatalf("the file default must apply without a flag, got:\n%s", out)
	}
}

func TestRefineStartLineShowsUnitEntry(t *testing.T) {
	root := writeProjectConfig(t, `{"defect_policy":"defect","unit_defect_policy":{"orders":"expected"}}`)
	out, _ := refineStart(t, root)
	if !strings.Contains(out, "wire refine: orders defect policy expected (from unit_defect_policy in ") {
		t.Fatalf("the unit entry must apply, got:\n%s", out)
	}
}

func TestRefineStartLineShowsBuiltInDefault(t *testing.T) {
	out, _ := refineStart(t, t.TempDir())
	if !strings.Contains(out, "wire refine: orders defect policy defect (from built-in default)") {
		t.Fatalf("a missing file must give the built-in default, got:\n%s", out)
	}
}

func TestRefineInvalidSettingsFailsBeforeAnyRun(t *testing.T) {
	root := writeProjectConfig(t, `{"defect_polcy":"expected"}`)
	out, err := refineStart(t, root)
	if err == nil || !strings.Contains(err.Error(), `"defect_polcy"`) {
		t.Fatalf("a misspelt key must stop refine with an error naming it, got: %v", err)
	}
	if strings.Contains(out, "defect policy") {
		t.Fatalf("no start line may be printed when the settings are invalid, got:\n%s", out)
	}
}

func TestRefineEmptyFlagIsRejected(t *testing.T) {
	_, err := refineStart(t, t.TempDir(), "-defect-policy=")
	if err == nil || !strings.Contains(err.Error(), "needs defect or expected") {
		t.Fatalf("an empty -defect-policy must be rejected, got: %v", err)
	}
}

func TestWireStatusPrintsEffectivePolicyPerUnit(t *testing.T) {
	routes := filepath.Join(t.TempDir(), "routes.json")
	body := `{"routes":[
 {"unit":"orders","method":"*","path_prefix":"/orders","target":"original"},
 {"unit":"users","method":"*","path_prefix":"/users","target":"rewrite"}]}`
	if err := os.WriteFile(routes, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root := writeProjectConfig(t, `{"defect_policy":"defect","unit_defect_policy":{"orders":"expected"}}`)
	var out bytes.Buffer
	if err := run([]string{"wire", "status", routes, "-root", root}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "orders") || !strings.Contains(s, "policy expected (unit_defect_policy in ") {
		t.Fatalf("orders must show its unit entry, got:\n%s", s)
	}
	if !strings.Contains(s, "users") || !strings.Contains(s, "policy defect (defect_policy in ") {
		t.Fatalf("users must show the file default, got:\n%s", s)
	}
}

func TestWireStatusWithoutRootKeepsOldOutput(t *testing.T) {
	routes := filepath.Join(t.TempDir(), "routes.json")
	body := `{"routes":[{"unit":"orders","method":"*","path_prefix":"/orders","target":"original"}]}`
	if err := os.WriteFile(routes, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"wire", "status", routes}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if out.String() != fmt.Sprintf("%-20s %s\n", "orders", "original") {
		t.Fatalf("without -root the output must stay as before, got %q", out.String())
	}
}
