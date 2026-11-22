package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// invoiceServer answers GET /invoices/1, which belongs to tenant acme. Tokens identify the caller.
// correct=true enforces the tenant; correct=false is the defect a rewrite might ship: any signed-in
// caller of an allowed role gets the invoice, whatever its tenant.
func invoiceServer(t *testing.T, correct bool) *httptest.Server {
	t.Helper()
	type who struct{ role, tenant string }
	tokens := map[string]who{
		"tok-admin-acme":   {"admin", "acme"},
		"tok-clerk-globex": {"clerk", "globex"},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/invoices/1" {
			http.NotFound(w, r)
			return
		}
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, ok := tokens[auth]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		allowedRole := c.role == "admin" || c.role == "clerk"
		if !allowedRole || (correct && c.tenant != "acme") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"id":1}`))
	}))
}

func rbacFixture(t *testing.T) (cases, principals, policy string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, v any) string {
		data, _ := json.Marshal(v)
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return p
	}
	cases = write("cases.json", []domain.AppCase{{Name: "get-invoice", Kind: "http", Method: "GET", Path: "/invoices/1"}})
	principals = write("principals.json", []domain.Principal{
		{Name: "admin-acme", Role: "admin", Tenant: "acme", TokenEnv: "GA_TEST_TOK_ADMIN"},
		{Name: "clerk-globex", Role: "clerk", Tenant: "globex", TokenEnv: "GA_TEST_TOK_CLERK"},
		{Name: "anon", Role: domain.AnonymousRole},
	})
	policy = write("policy.json", []domain.CasePolicy{{Case: "get-invoice", AllowedRoles: []string{"admin", "clerk"}, OwnerTenant: "acme"}})
	t.Setenv("GA_TEST_TOK_ADMIN", "tok-admin-acme")
	t.Setenv("GA_TEST_TOK_CLERK", "tok-clerk-globex")
	return cases, principals, policy
}

func TestRBACCleanRewritePasses(t *testing.T) {
	orig := invoiceServer(t, true)
	defer orig.Close()
	rew := invoiceServer(t, true)
	defer rew.Close()
	cases, principals, policy := rbacFixture(t)
	var out bytes.Buffer
	err := cmdRBAC([]string{"-cases", cases, "-principals", principals, "-policy", policy,
		"-base", rew.URL, "-original", orig.URL}, &out)
	if err != nil {
		t.Fatalf("a rewrite that matches the original and the policy must pass: %v\n%s", err, out.String())
	}
}

func TestRBACCrossTenantDefectFailsWithCriticalFinding(t *testing.T) {
	orig := invoiceServer(t, true)
	defer orig.Close()
	leaky := invoiceServer(t, false)
	defer leaky.Close()
	cases, principals, policy := rbacFixture(t)
	var out bytes.Buffer
	err := cmdRBAC([]string{"-cases", cases, "-principals", principals, "-policy", policy,
		"-base", leaky.URL, "-original", orig.URL}, &out)
	if err == nil {
		t.Fatalf("a cross-tenant leak must fail the check:\n%s", out.String())
	}
	text := out.String()
	if !strings.Contains(text, "[CRITICAL] get-invoice as clerk-globex") {
		t.Fatalf("the leak must be named by case and principal:\n%s", text)
	}
}

// postServer answers PUT /posts/1, which is authored by alice in tenant acme. correct=true lets only the author
// edit; correct=false is the defect where any signed-in member of the tenant can edit.
func postServer(t *testing.T, correct bool) *httptest.Server {
	t.Helper()
	tokens := map[string]string{"tok-alice": "alice", "tok-bob": "bob"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/posts/1" || r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		user, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if correct && user != "alice" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// writeFixtureFile writes one JSON input file into dir and returns its path.
func writeFixtureFile(t *testing.T, dir, name string, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRBACOwnershipPolicyPassesOnlyForTheAuthor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GA_TEST_TOK_ALICE", "tok-alice")
	t.Setenv("GA_TEST_TOK_BOB", "tok-bob")
	cases := writeFixtureFile(t, dir, "cases.json", []domain.AppCase{{Name: "edit-post", Kind: "http", Method: "PUT", Path: "/posts/1"}})
	principals := writeFixtureFile(t, dir, "principals.json", []domain.Principal{
		{Name: "alice", Role: "member", Tenant: "acme", TokenEnv: "GA_TEST_TOK_ALICE"},
		{Name: "bob", Role: "member", Tenant: "acme", TokenEnv: "GA_TEST_TOK_BOB"},
	})
	policy := writeFixtureFile(t, dir, "policy.json", []domain.CasePolicy{{Case: "edit-post", AllowedPrincipals: []string{"alice"}, OwnerTenant: "acme"}})

	good := postServer(t, true)
	defer good.Close()
	var out bytes.Buffer
	if err := cmdRBAC([]string{"-cases", cases, "-principals", principals, "-policy", policy, "-base", good.URL}, &out); err != nil {
		t.Fatalf("an author-only edit must pass: %v\n%s", err, out.String())
	}

	leaky := postServer(t, false)
	defer leaky.Close()
	out.Reset()
	err := cmdRBAC([]string{"-cases", cases, "-principals", principals, "-policy", policy, "-base", leaky.URL}, &out)
	if err == nil {
		t.Fatalf("a member who can edit another member's post must fail the check:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "[CRITICAL] edit-post as bob") {
		t.Fatalf("the failing member must be named by case and principal:\n%s", out.String())
	}
}

func TestRBACRejectsUnknownPrincipalInPolicy(t *testing.T) {
	cases, principals, _ := rbacFixture(t)
	dir := filepath.Dir(cases)
	typo := writeFixtureFile(t, dir, "typo-policy.json", []domain.CasePolicy{{Case: "get-invoice", AllowedPrincipals: []string{"alise"}, OwnerTenant: "acme"}})
	srv := invoiceServer(t, true)
	defer srv.Close()
	var out bytes.Buffer
	err := cmdRBAC([]string{"-cases", cases, "-principals", principals, "-policy", typo, "-base", srv.URL}, &out)
	if err == nil {
		t.Fatal("a policy naming a principal absent from the principal file must be a configuration error")
	}
	if !strings.Contains(err.Error(), "get-invoice") || !strings.Contains(err.Error(), "alise") {
		t.Fatalf("the error must name the policy case and the unknown principal: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a configuration error must stop before any request is made:\n%s", out.String())
	}
}

func TestWireRefineRejectsUnknownPrincipalInPolicy(t *testing.T) {
	cases, principals, _ := rbacFixture(t)
	typo := writeFixtureFile(t, filepath.Dir(cases), "typo-policy.json", []domain.CasePolicy{{Case: "get-invoice", AllowedPrincipals: []string{"alise"}}})
	_, err := rbacOptionsFromFlags(principals, typo, true)
	if err == nil || !strings.Contains(err.Error(), "alise") {
		t.Fatalf("wire refine must reject an unknown principal in the policy too: %v", err)
	}
}

func TestRBACRejectsMissingFlags(t *testing.T) {
	if err := cmdRBAC([]string{"-cases", "x"}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing required flags must be an error")
	}
}

func TestRBACListsCasesWithoutPolicy(t *testing.T) {
	cases := []domain.AppCase{{Name: "a"}, {Name: "b"}}
	got := casesWithoutPolicy(cases, []domain.CasePolicy{{Case: "a"}})
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("the case with no policy must be listed for review: %v", got)
	}
}

func TestWireRefineRBACFlagsNeedBothFilesAndAnHTTPUnit(t *testing.T) {
	if _, err := rbacOptionsFromFlags("p.json", "", true); err == nil || !strings.Contains(err.Error(), "given together") {
		t.Fatalf("a principal file alone must be refused: %v", err)
	}
	if _, err := rbacOptionsFromFlags("", "q.json", true); err == nil || !strings.Contains(err.Error(), "given together") {
		t.Fatalf("a policy file alone must be refused: %v", err)
	}
	if _, err := rbacOptionsFromFlags("p.json", "q.json", false); err == nil || !strings.Contains(err.Error(), "HTTP units only") {
		t.Fatalf("the check on a command-line unit must be refused: %v", err)
	}
	if got, err := rbacOptionsFromFlags("", "", false); got != nil || err != nil {
		t.Fatalf("no RBAC flags must mean no check, got %+v err=%v", got, err)
	}
}

func TestWireRefineRejectsRBACOnCommandLineUnitBeforeReadingFiles(t *testing.T) {
	var out bytes.Buffer
	err := cmdWireRefine([]string{"no-such-root", "orders", "-cases", "no-such-app.json", "-original-cli", "orig",
		"-rbac-principals", "no-such-principals.json", "-rbac-policy", "no-such-policy.json"}, &out)
	if err == nil || !strings.Contains(err.Error(), "HTTP units only") {
		t.Fatalf("wire refine must refuse the check on a command-line unit with a clear error, before reading files: %v", err)
	}
}

// TestPrintCellsNamesTheDenyReason pins the CLI wiring: the matrix table shows which refusal a deny
// was, so an operator reads 403 (forbidden) differently from 404 (not_found) without opening the JSON.
func TestPrintCellsNamesTheDenyReason(t *testing.T) {
	m := usecase.Matrix{
		usecase.MatrixKey("hidden", "alice"): {Outcome: domain.OutcomeDeny, Status: 404, Reason: domain.DenyNotFound},
		usecase.MatrixKey("list", "anon"):    {Outcome: domain.OutcomeDeny, Status: 401, Reason: domain.DenyUnauthenticated},
		usecase.MatrixKey("list", "alice"):   {Outcome: domain.OutcomeAllow, Status: 200},
	}
	cases := []domain.AppCase{{Name: "list", Kind: "http"}, {Name: "hidden", Kind: "http"}}
	principals := []domain.Principal{{Name: "anon"}, {Name: "alice"}}
	var b bytes.Buffer
	printCells(&b, "rewrite", m, cases, principals)
	out := b.String()
	for _, want := range []string{"deny (not_found)", "deny (unauthenticated)", "allow"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the table must show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "allow (") {
		t.Fatalf("an allow has no refusal reason:\n%s", out)
	}
}
