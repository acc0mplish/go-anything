package wiregen

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// origScript stands in for the original program. printf and read -r keep the output exact.
const origScript = `#!/bin/sh
if [ "$1" = orders ]; then
  if [ -n "$2" ]; then
    printf 'order:%s\n' "$2"
  else
    read -r x
    printf 'order:%s\n' "$x"
  fi
  exit 0
fi
echo "usage: orig orders <id>" >&2
exit 1
`

// fixScript is a fake agent: it writes the correct rewrite, so the test checks the loop, not an LLM.
const fixScript = `set -e
cat > /dev/null
cat > units/orders/rewrite.go <<'GO'
package orders

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	v := ""
	if len(args) > 1 && args[1] != "" {
		v = args[1]
	} else {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		v = strings.TrimRight(line, "\r\n")
	}
	fmt.Fprintf(stdout, "order:%s\n", v)
	return 0
}
GO
`

func TestRefineRecordsDisagreementsAndConverges(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.sh")
	fix := filepath.Join(dir, "fix.sh")
	os.WriteFile(orig, []byte(origScript), 0o755)
	os.WriteFile(fix, []byte(fixScript), 0o644)

	root := newModule(t)
	if _, err := Gen(root, "orders", ""); err != nil {
		t.Fatal(err)
	}
	casesPath := filepath.Join(dir, "app.json")
	rec := domain.AppRecording{Subject: "orig", Cases: []domain.AppCase{
		{Name: "one", Kind: "cli", Argv: []string{"orders", "1"}, Stdout: "order:1\n"},
	}}
	if err := store.SaveApp(casesPath, rec); err != nil {
		t.Fatal(err)
	}
	data, _ := usecase.CLICasesTest("orders", usecase.CasesForCommand(rec, "orders"), domain.CompareOptions{})
	WriteCases(root, "orders", data)

	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 3, Probes: 8, MaxNew: 20, Seed: 3, Agent: "sh " + fix, Attempts: 1, Log: io.Discard,
	})
	if err != nil || !ok {
		t.Fatalf("refine must converge once the agent fixes the rewrite: ok=%v err=%v", ok, err)
	}
	after, err := store.LoadApp(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Cases) <= 1 {
		t.Fatal("the disagreements found by probing must be recorded")
	}
	// the probe program must not be left behind in the unit
	if _, err := os.Stat(filepath.Join(root, "units", "orders", "ga_probe")); !os.IsNotExist(err) {
		t.Fatal("the probe program must be removed after use")
	}
}

func TestRefineWithoutAgentStopsAndSaysWhatToDo(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.sh")
	os.WriteFile(orig, []byte(origScript), 0o755)
	root := newModule(t)
	Gen(root, "orders", "")
	casesPath := filepath.Join(dir, "app.json")
	store.SaveApp(casesPath, domain.AppRecording{Subject: "orig", Cases: []domain.AppCase{
		{Name: "one", Kind: "cli", Argv: []string{"orders", "1"}, Stdout: "order:1\n"},
	}})
	_, err := Refine(RefineOptions{Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: io.Discard})
	if err == nil {
		t.Fatal("a placeholder rewrite without an agent must not converge")
	}
}
