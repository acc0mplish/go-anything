package usecase

import (
	"strings"
	"testing"
	"unicode"

	"github.com/go-anything/go-anything/internal/domain"
)

// asciiCases are recorded cases whose data carries no non-ASCII bytes, so any non-ASCII in the
// generated output must come from the fixed prose: that prose is English only.
var asciiCases = []domain.AppCase{
	{Name: "u", Kind: "cli", Argv: []string{"orders", "7"}, Stdin: "in\n", ExitCode: 0, Stdout: "out\n", Stderr: "err\n"},
	{Name: "h", Kind: "http", Method: "GET", Path: "/orders/1", Status: 200, ResponseBody: `{"id": 1}`,
		Headers: map[string]string{"Content-Type": "application/json"}},
}

// unicodeCases carry non-ASCII data; the porting loop must replay the recorded bytes verbatim.
var unicodeCases = []domain.AppCase{
	{Name: "u", Kind: "cli", Argv: []string{"orders", "숫자"}, Stdin: "입력\n", ExitCode: 0, Stdout: "출력\n"},
	{Name: "h", Kind: "http", Method: "GET", Path: "/주문/1", Status: 200, ResponseBody: `{"합계": 3}`},
}

// TestGeneratedProseIsEnglish guards the language rule for everything go-anything writes for another
// engineer to read: with ASCII-only recorded data, the generated test code, the comparison copies and
// the agent briefs are pure ASCII English, so they read the same for every operator.
func TestGeneratedProseIsEnglish(t *testing.T) {
	cli, err := CLICasesTest("orders", asciiCases[:1], domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	httpTest, err := CasesTest("orders", asciiCases, domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	helpers, err := ComparisonTestFiles("orders")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{
		"cli cases test":   string(cli),
		"http cases test":  string(httpTest),
		"agent prompt":     AgentPrompt("orders", "/orders/", asciiCases, ""),
		"agent cli prompt": AgentPromptCLI("orders", asciiCases[:1], ""),
	}
	for name, src := range helpers {
		out["comparison copy "+name] = string(src)
	}
	for name, src := range out {
		for i, r := range src {
			if r > unicode.MaxASCII {
				t.Errorf("%s carries non-ASCII %q at byte %d; fixed prose must be English", name, string(r), i)
				break
			}
		}
	}
}

// TestGeneratedTestsCarryRecordedDataVerbatim pins the other half of the rule: the data is never
// rewritten, translated or transliterated on its way into a generated test.
func TestGeneratedTestsCarryRecordedDataVerbatim(t *testing.T) {
	cli, err := CLICasesTest("orders", unicodeCases[:1], domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	httpTest, err := CasesTest("orders", unicodeCases, domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		in   string
		what string
	}{
		{string(cli), `"숫자"`},
		{string(cli), `"출력\n"`},
		{string(httpTest), "/주문/1"},
		{string(httpTest), "합계"},
	} {
		if !strings.Contains(want.in, want.what) {
			t.Errorf("generated test lost the recorded data %s", want.what)
		}
	}
}
