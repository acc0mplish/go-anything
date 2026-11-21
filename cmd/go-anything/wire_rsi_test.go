package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefineExtraInputsFileIsCheckedBeforeAnyRun(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	os.WriteFile(empty, []byte("\n\n"), 0o600)
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`[1, 2]`), 0o600)
	for name, want := range map[string]string{
		filepath.Join(dir, "missing.txt"): "-extra-inputs",
		empty:                             "holds no value",
		bad:                               "not a JSON array of strings",
	} {
		out, err := refineStart(t, t.TempDir(), "-extra-inputs", name)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want an error with %q, got %v", name, want, err)
		}
		if strings.Contains(out, "defect policy") {
			t.Fatalf("%s: a bad extra-inputs file must stop refine before it starts:\n%s", name, out)
		}
	}
	good := filepath.Join(dir, "good.txt")
	os.WriteFile(good, []byte("alice-smith\n007\n"), 0o600)
	if out, _ := refineStart(t, t.TempDir(), "-extra-inputs", good); !strings.Contains(out, "defect policy") {
		t.Fatalf("a valid extra-inputs file must let refine start:\n%s", out)
	}
}

func TestWireGenRunLineForScaffoldedFrontDoor(t *testing.T) {
	cases := filepath.Join(t.TempDir(), "app.json")
	os.WriteFile(cases, []byte(`{"subject":"s","cases":[{"name":"hello","kind":"http","method":"GET","path":"/hello/bob","status":200}]}`), 0o600)
	repo := t.TempDir()
	if err := run([]string{"frontdoor", "scaffold", repo, "-cases", cases, "-original-cmd", "python3 app.py", "-check=false"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := filepath.Join(repo, "go", "app")
	if err := run([]string{"wire", "gen", app, "hello", "/hello/"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("wire gen: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "GA_ORIGINAL_URL=<original service URL>") ||
		!strings.Contains(out.String(), "run the app:      go run "+app+"  (the scaffolded front door starts the original and sets GA_ORIGINAL_URL itself)") {
		t.Fatalf("a scaffolded front door sets GA_ORIGINAL_URL itself; the run line must not ask for it:\n%s", out.String())
	}
}

func TestWireGenWithoutMainStillWritesTheRecordedCases(t *testing.T) {
	cases := filepath.Join(t.TempDir(), "app.json")
	os.WriteFile(cases, []byte(`{"subject":"s","cases":[{"name":"o1","kind":"http","method":"GET","path":"/orders/1","status":200}]}`), 0o600)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/svc\n\ngo 1.27.4\n"), 0o600)
	var out bytes.Buffer
	err := run([]string{"wire", "gen", root, "orders", "/orders/", "-cases", cases}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "main.go") {
		t.Fatalf("a module without main.go must fail naming main.go, got %v", err)
	}
	for _, name := range []string{"cases_test.go", "ga_compare_test.go", "ga_answer_test.go"} {
		if _, statErr := os.Stat(filepath.Join(root, "units", "orders", name)); statErr != nil {
			t.Fatalf("%s must be written even when main.go is missing (wire gen wrote the unit before touching main.go):\n%s", name, out.String())
		}
	}
	if !strings.Contains(out.String(), "1 recorded case(s)") {
		t.Fatalf("the cases write must be reported before the main.go failure:\n%s", out.String())
	}
}
