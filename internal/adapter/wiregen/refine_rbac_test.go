package wiregen

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

func TestRoundConvergenceNeedsNoCriticalAuthorizationFinding(t *testing.T) {
	critical := []usecase.Finding{{Severity: usecase.SeverityCritical, Case: "get-order", Principal: "clerk-globex", Detail: "rewrite allow"}}
	high := []usecase.Finding{{Severity: usecase.SeverityHigh, Case: "get-order", Principal: "anon", Detail: "rewrite answered 500"}}
	cases := []struct {
		name        string
		disagree    int
		findings    []usecase.Finding
		diverges    int
		chainDiv    int
		chainPend   int
		wantConverg bool
	}{
		{"0 disagreements and 1 critical RBAC finding is not converged", 0, critical, 0, 0, 0, false},
		{"0 disagreements and 0 RBAC findings is converged", 0, nil, 0, 0, 0, true},
		{"0 disagreements and a high finding alone still converges", 0, high, 0, 0, 0, true},
		{"a probe disagreement is never converged", 1, nil, 0, 0, 0, false},
		{"1 concurrent divergence is never converged", 0, nil, 1, 0, 0, false},
		{"1 diverging chain is never converged", 0, nil, 0, 1, 0, false},
		{"1 unjudgeable chain is never converged", 0, nil, 0, 0, 1, false},
	}
	for _, tc := range cases {
		if got := roundConverged(tc.disagree, tc.findings, tc.diverges, tc.chainDiv, tc.chainPend); got != tc.wantConverg {
			t.Errorf("%s: got converged=%v", tc.name, got)
		}
	}
}

func TestRefineRBACRejectsCommandLineUnit(t *testing.T) {
	_, err := Refine(RefineOptions{
		Root: "unused", Unit: "orders", CasesPath: "unused.json", OriginalCLI: "orig",
		RBAC: &RBACOptions{Principals: []domain.Principal{{Name: "anon", Role: domain.AnonymousRole}}},
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP units only") {
		t.Fatalf("the authorization check on a command-line unit must be refused with a clear error: %v", err)
	}
}

// rbacTokens maps each token to the role and tenant it identifies. The tokens are test values only.
var rbacTokens = map[string]struct{ role, tenant string }{
	"tok-admin-acme":   {"admin", "acme"},
	"tok-clerk-globex": {"clerk", "globex"},
}

// rbacOrderService is the original in these tests. GET /orders/1 belongs to tenant acme, and the admin and
// clerk roles may read it. Without a token the answer is 401. enforceTenant=false is the leak: any clerk
// reads the order, whatever its tenant.
func rbacOrderService(enforceTenant bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := rbacTokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/orders/1" || (c.role != "admin" && c.role != "clerk") || (enforceTenant && c.tenant != "acme") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("{\"id\":1}"))
	}
}

// rbacOrderRewrite is the rewrite source. It must answer like rbacOrderService; enforceTenant is set per test.
const rbacOrderRewrite = `package orders

import (
	"net/http"
	"strings"
)

// enforceTenant is set when the test writes the unit: true enforces the owner tenant.
const enforceTenant = %t

func Handle(w http.ResponseWriter, r *http.Request) {
	type who struct{ role, tenant string }
	tokens := map[string]who{
		"tok-admin-acme":   {"admin", "acme"},
		"tok-clerk-globex": {"clerk", "globex"},
	}
	c, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/orders/1" || (c.role != "admin" && c.role != "clerk") || (enforceTenant && c.tenant != "acme") {
		http.NotFound(w, r)
		return
	}
	w.Write([]byte("{\"id\":1}"))
}
`

const rbacMainSrc = `package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	log.Fatal(http.ListenAndServe("%s", mux))
}
`

// rbacModule writes a module whose HTTP unit "orders" is wired and holds the rewrite under test.
func rbacModule(t *testing.T, enforceTenant bool) string {
	t.Helper()
	root := newModule(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(fmt.Sprintf(rbacMainSrc, addr)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	if _, err := WireMain(root, "main.go", "example.com/app", "http"); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(rbacOrderRewrite, enforceTenant)
	if err := os.WriteFile(filepath.Join(root, "units", "orders", "rewrite.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestRefineRBACGateOnHTTPUnit runs the whole round for real: the probes ask the rewrite anonymously, the
// authorization check starts the rewrite and asks it as each principal, and the original is asked the same way.
func TestRefineRBACGateOnHTTPUnit(t *testing.T) {
	t.Setenv("GA_RBAC_E2E_ADMIN", "tok-admin-acme")
	t.Setenv("GA_RBAC_E2E_CLERK", "tok-clerk-globex")
	principals := []domain.Principal{
		{Name: "admin-acme", Role: "admin", Tenant: "acme", TokenEnv: "GA_RBAC_E2E_ADMIN"},
		{Name: "clerk-globex", Role: "clerk", Tenant: "globex", TokenEnv: "GA_RBAC_E2E_CLERK"},
		{Name: "anon", Role: domain.AnonymousRole},
	}
	policies := []domain.CasePolicy{{Case: "get-order", AllowedRoles: []string{"admin", "clerk"}, OwnerTenant: "acme"}}
	cases := []struct {
		name           string
		origEnforce    bool
		rewEnforce     bool
		converged      bool
		wantCritical   bool
		wantOrigDefect int
	}{
		// The probes all agree (everything is anonymous, 401). Only the authorization check can tell these apart.
		{"rewrite lets another tenant in: not converged", true, false, false, true, 0},
		{"original lets another tenant in, rewrite is right: converged, original defect", false, true, true, false, 1},
		{"both enforce the tenant: converged", true, true, true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := httptest.NewServer(rbacOrderService(tc.origEnforce))
			defer orig.Close()
			root := rbacModule(t, tc.rewEnforce)
			dir := t.TempDir()
			casesPath := filepath.Join(dir, "app.json")
			if err := store.SaveApp(casesPath, domain.AppRecording{Subject: "orders", Cases: []domain.AppCase{{
				Name: "get-order", Kind: "http", Method: "GET", Path: "/orders/1",
				Status: 401, ResponseBody: "unauthorized\n", Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
			}}}); err != nil {
				t.Fatal(err)
			}
			var logBuf bytes.Buffer
			ok, err := Refine(RefineOptions{
				Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
				OriginalURL: orig.URL, Rounds: 2, Probes: 8, MaxNew: 5, Seed: 1, Log: &logBuf,
				RBAC: &RBACOptions{Principals: principals, Policies: policies},
			})
			if tc.converged != (err == nil && ok) {
				t.Fatalf("converged: want %v, got ok=%v err=%v\n%s", tc.converged, ok, err, logBuf.String())
			}
			if !tc.converged && (err == nil || !strings.Contains(err.Error(), "critical authorization finding")) {
				t.Fatalf("a blocked round must say why: %v", err)
			}

			// Converging takes two clean rounds on fresh seeds, so a converged run ends on round 2 and round 1 is
			// recorded as the first clean round. A blocked run stops on round 1.
			lastRound, wantStreak := "round-01.json", 0
			if tc.converged {
				lastRound, wantStreak = "round-02.json", 2
				first := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", "round-01.json"))
				if first.Converged || first.CleanStreak != 1 {
					t.Fatalf("round 1 must be the first clean round, not converged: converged=%v clean_streak=%d", first.Converged, first.CleanStreak)
				}
			}
			check := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", lastRound))
			if check.CleanStreak != wantStreak {
				t.Fatalf("%s: clean_streak want %d, got %d", lastRound, wantStreak, check.CleanStreak)
			}
			if !check.RBACChecked || check.RBACPrincipals != len(principals) {
				t.Fatalf("the round must record that the check ran with %d principals: checked=%v principals=%d",
					len(principals), check.RBACChecked, check.RBACPrincipals)
			}
			if check.Converged != tc.converged || check.Disagreements != 0 {
				t.Fatalf("round record: converged=%v disagreements=%d", check.Converged, check.Disagreements)
			}
			if check.OriginalDefects != tc.wantOrigDefect {
				t.Fatalf("original defects: want %d, got %d", tc.wantOrigDefect, check.OriginalDefects)
			}
			gotCritical := false
			for _, f := range check.RBACFindings {
				if f.Severity == usecase.SeverityCritical && f.Case == "get-order" && f.Principal == "clerk-globex" {
					gotCritical = true
				}
			}
			if gotCritical != tc.wantCritical {
				t.Fatalf("critical finding for clerk-globex: want %v, findings %+v", tc.wantCritical, check.RBACFindings)
			}
			if tc.wantOrigDefect > 0 {
				defects, err := os.ReadFile(casesPath + ".defects.json")
				if err != nil || !strings.Contains(string(defects), "get-order as clerk-globex") {
					t.Fatalf("the original's leak must be listed as an original defect: %v\n%s", err, defects)
				}
			}
		})
	}
}
