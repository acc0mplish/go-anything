package usecase

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func cliRecording() domain.AppRecording {
	return domain.AppRecording{Cases: []domain.AppCase{
		{Name: "one", Kind: "cli", Argv: []string{"orders", "1"}, ExitCode: 0, Stdout: `{"id": 1, "total": 100}`},
		{Name: "bad", Kind: "cli", Argv: []string{"orders", "abc"}, ExitCode: 2, Stderr: "bad order id"},
		{Name: "other", Kind: "cli", Argv: []string{"users", "1"}, ExitCode: 0},
		{Name: "stdin", Kind: "cli", Argv: []string{"orders"}, Stdin: "7\n", ExitCode: 0, Stdout: `{"id": 7, "total": 700}`},
	}}
}

func TestCasesForCommandSelectsByFirstArgument(t *testing.T) {
	got := CasesForCommand(cliRecording(), "orders")
	if len(got) != 3 {
		t.Fatalf("want the three orders cases, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.Argv[0] != "orders" {
			t.Fatalf("a case for another command slipped in: %+v", c)
		}
	}
}

func TestCLICasesTestIsValidGoAndCarriesEveryCase(t *testing.T) {
	src, err := CLICasesTest("orders", CasesForCommand(cliRecording(), "orders"), domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "cases_test.go", src, parser.AllErrors); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, src)
	}
	s := string(src)
	for _, want := range []string{`[]string{"orders", "1"}`, `"stdin"`, `"bad order id"`, "Handle(c.args"} {
		if !strings.Contains(s, want) {
			t.Fatalf("cases test missing %q:\n%s", want, s)
		}
	}
}

func TestAgentPromptCLIStatesContractAndCleanRoom(t *testing.T) {
	p := AgentPromptCLI("orders", CasesForCommand(cliRecording(), "orders"), "")
	for _, want := range []string{"func Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int", "Do not look for", `args=["orders" "1"]`, "exit 2"} {
		if !strings.Contains(p, want) {
			t.Fatalf("CLI brief missing %q:\n%s", want, p)
		}
	}
}

func TestHTTPProbesIncludeTrailingSlashShape(t *testing.T) {
	probes := ProbeCases("http", "orders", "/orders/", []string{"3"})
	var plain, slash bool
	for _, p := range probes {
		if p.Path == "/orders/3" {
			plain = true
		}
		if p.Path == "/orders/3/" {
			slash = true
		}
	}
	if !plain || !slash {
		t.Fatalf("both the plain and the trailing-slash shape must be probed: %+v", probes)
	}
}
