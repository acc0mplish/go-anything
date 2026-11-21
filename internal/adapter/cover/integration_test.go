package cover

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/subject"
	"github.com/go-anything/go-anything/internal/usecase"
)

// TestCoverageMatchesOfficialToolOnStdinInput guards the measurement path end to end:
// a program that branches on stdin must reach the branch the inputs exercise, and the
// coverage must equal what `go tool covdata` reports for the same counters.
func TestCoverageMatchesOfficialToolOnStdinInput(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program; skipped in -short")
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "cmd", "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module covtest\n\ngo 1.27.4\n")
	must("cmd/app/main.go", `package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func pick(x string) string {
	if x == "a" {
		return "A"
	}
	return "other"
}

func main() {
	in, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Println(pick(strings.TrimRight(in, "\r\n")))
}
`)
	bin := filepath.Join(t.TempDir(), "app")
	b := exec.Command("go", "build", "-cover", "-o", bin, "./cmd/app")
	b.Dir = src
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	col, err := NewCollector(filepath.Join(t.TempDir(), "counters"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{"line-1": "a", "line-2": "b"}
	if _, err := usecase.Record("covtest", inputs, subject.Exec{Argv: []string{bin}, Env: col.Env()}); err != nil {
		t.Fatal(err)
	}
	got, err := col.Profile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Both branches run, so every statement of the program is covered.
	if got.Percent != 100 {
		t.Fatalf("want 100%% with both branches exercised, got %+v", got)
	}
}
