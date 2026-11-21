package wiregen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

func recordedCases() []domain.AppCase {
	return []domain.AppCase{{Name: "get-order", Kind: "http", Method: "GET", Path: "/orders/1", Status: 200,
		ResponseBody: "ok", Headers: map[string]string{"Content-Type": "text/plain"}}}
}

// fakeAgent is a stand-in for a coding agent: its first attempt writes a wrong rewrite, its second a right one.
const fakeAgent = `set -e
n=0; [ -f attempts ] && n=$(cat attempts); n=$((n+1)); echo $n > attempts
cat > /dev/null
if [ "$n" -lt 2 ]; then body=wrong; else body=ok; fi
cat > units/orders/rewrite.go <<GO
package orders

import "net/http"

func Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("$body"))
}
GO
`

func setupAgentModule(t *testing.T) (root string, brief func(string) string) {
	t.Helper()
	root = newModule(t)
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	data, err := usecase.CasesTest("orders", recordedCases(), domain.CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCases(root, "orders", data); err != nil {
		t.Fatal(err)
	}
	brief = func(prev string) string {
		return usecase.AgentPrompt("orders", "/orders/", recordedCases(), prev)
	}
	return root, brief
}

func writeFakeAgent(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(p, []byte(fakeAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAgentRetriesWithFailureAndPasses(t *testing.T) {
	root, brief := setupAgentModule(t)
	res, err := RunAgent(root, "orders", "sh "+writeFakeAgent(t), 3, brief)
	if err != nil {
		t.Fatalf("the second attempt is correct and must pass: %v", err)
	}
	if !strings.Contains(res, "attempt 2") {
		t.Fatalf("it must pass on the second attempt, got %q", res)
	}
}

func TestAgentGivesUpAfterAttempts(t *testing.T) {
	root, brief := setupAgentModule(t)
	_, err := RunAgent(root, "orders", "sh "+writeFakeAgent(t), 1, brief)
	if err == nil {
		t.Fatal("one attempt is not enough for this fake agent: it must fail")
	}
	if !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("the failure must say the rewrite did not pass: %v", err)
	}
}

func TestPromptIsCleanRoomAndCarriesCases(t *testing.T) {
	p := usecase.AgentPrompt("orders", "/orders/", recordedCases(), "")
	for _, want := range []string{"Clean room", "Do not look for", "GET /orders/1", "ok", "func Handle"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestCasesFilterByPrefixAndKind(t *testing.T) {
	rec := domain.AppRecording{Cases: []domain.AppCase{
		{Name: "a", Kind: "http", Path: "/orders/1"},
		{Name: "b", Kind: "http", Path: "/users/1"},
		{Name: "c", Kind: "cli", Path: "/orders/1"},
	}}
	got := usecase.CasesFor(rec, "/orders/")
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("only the http case under /orders/ belongs to the unit: %+v", got)
	}
}
