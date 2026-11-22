package usecase

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

var (
	adminAcme   = domain.Principal{Name: "admin-acme", Role: "admin", Tenant: "acme"}
	clerkGlobex = domain.Principal{Name: "clerk-globex", Role: "clerk", Tenant: "globex"}
	anon        = domain.Principal{Name: "anon", Role: domain.AnonymousRole}
	principals  = []domain.Principal{adminAcme, clerkGlobex, anon}
	policy      = []domain.CasePolicy{{Case: "get-invoice", AllowedRoles: []string{"admin", "clerk"}, OwnerTenant: "acme"}}
	invoice     = []domain.AppCase{{Name: "get-invoice", Kind: "http", Method: "GET", Path: "/invoices/1"}}
)

// matrixWith builds a Matrix that answers the same status for every principal of the one case.
func matrixWith(statusFor func(domain.Principal) int) Matrix {
	m := Matrix{}
	for _, p := range principals {
		s := statusFor(p)
		m[MatrixKey("get-invoice", p.Name)] = Cell{Outcome: domain.ClassifyStatus(s), Status: s}
	}
	return m
}

func TestCleanPairHasNoFindings(t *testing.T) {
	good := matrixWith(func(p domain.Principal) int {
		if p == adminAcme {
			return 200
		}
		return 404
	})
	if f := Audit(policy, principals, good, good); len(f) != 0 {
		t.Fatalf("a matrix that matches the policy on both sides must be clean: %+v", f)
	}
}

func TestCrossTenantLeakIsCritical(t *testing.T) {
	leaky := matrixWith(func(p domain.Principal) int { return 200 }) // every principal gets 200
	f := Audit(policy, principals, nil, leaky)
	if !HasCritical(f) {
		t.Fatalf("a cross-tenant allow must be a critical finding: %+v", f)
	}
	if !strings.Contains(f[0].Detail, "policy expects deny") {
		t.Fatalf("the finding must name the declared expectation: %+v", f[0])
	}
}

func TestDivergenceBetweenOriginalAndRewriteIsCritical(t *testing.T) {
	orig := matrixWith(func(p domain.Principal) int {
		if p == adminAcme {
			return 200
		}
		return 404
	})
	rew := matrixWith(func(p domain.Principal) int {
		if p == adminAcme || p == clerkGlobex {
			return 200
		}
		return 404
	})
	f := Audit(nil, principals, orig, rew)
	if len(f) != 1 || f[0].Severity != SeverityCritical || f[0].Principal != "clerk-globex" {
		t.Fatalf("exactly one critical divergence must be reported for clerk-globex: %+v", f)
	}
}

func TestServerErrorWhereAnswerExpectedIsHighNotCritical(t *testing.T) {
	crash := matrixWith(func(p domain.Principal) int {
		if p == adminAcme {
			return 500
		}
		return 404
	})
	f := Audit(policy, principals, nil, crash)
	if HasCritical(f) {
		t.Fatalf("a 500 must not be read as an authorization answer: %+v", f)
	}
	if len(f) != 1 || f[0].Severity != SeverityHigh {
		t.Fatalf("a 500 where allow was expected must be one high finding: %+v", f)
	}
}

func TestRunMatrixStopsOnProbeError(t *testing.T) {
	_, err := RunMatrix(invoice, principals, func(c domain.AppCase, p domain.Principal) (int, error) {
		if p.Name == "clerk-globex" {
			return 0, errors.New("environment variable TOKEN is not set")
		}
		return 200, nil
	})
	if err == nil || !strings.Contains(err.Error(), "clerk-globex") {
		t.Fatalf("a request that could not be made must stop the run and name the principal: %v", err)
	}
}

func TestRunMatrixClassifiesEachCell(t *testing.T) {
	m, err := RunMatrix(invoice, principals, func(c domain.AppCase, p domain.Principal) (int, error) {
		if p == anon {
			return 401, nil
		}
		return 200, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m[MatrixKey("get-invoice", "anon")].Outcome != domain.OutcomeDeny {
		t.Fatal("a 401 must be recorded as a denial")
	}
	if m[MatrixKey("get-invoice", "admin-acme")].Outcome != domain.OutcomeAllow {
		t.Fatal("a 200 must be recorded as an allow")
	}
}

// leakFor answers 200 for the principals in allowed and 404 for the rest.
func leakFor(allowed ...domain.Principal) Matrix {
	return matrixWith(func(p domain.Principal) int {
		for _, a := range allowed {
			if a == p {
				return 200
			}
		}
		return 404
	})
}

func TestSplitRewriteLeakBlocksAsRewriteFinding(t *testing.T) {
	orig := leakFor(adminAcme)
	rew := leakFor(adminAcme, clerkGlobex) // the rewrite lets clerk-globex in; the original does not
	rewriteF, originalD := SplitFindings(policy, principals, orig, rew)
	if !HasCritical(rewriteF) {
		t.Fatalf("a rewrite that changes who is allowed in must block: %+v", rewriteF)
	}
	for _, f := range rewriteF {
		if f.Principal != "clerk-globex" {
			t.Fatalf("only clerk-globex differs, so only that principal may be named: %+v", f)
		}
	}
	if len(originalD) != 0 {
		t.Fatalf("the original is correct here, so it has no defect: %+v", originalD)
	}
}

func TestOriginalLeakThatRewriteFixesIsOriginalDefectNotBlocking(t *testing.T) {
	orig := leakFor(adminAcme, clerkGlobex) // the original lets clerk-globex in, which the policy forbids
	rew := leakFor(adminAcme)               // the rewrite matches the policy
	rewriteF, originalD := SplitFindings(policy, principals, orig, rew)
	if HasCritical(rewriteF) || len(rewriteF) != 0 {
		t.Fatalf("a rewrite that matches the policy must not be blamed for the original's leak: %+v", rewriteF)
	}
	if len(originalD) == 0 || originalD[0].Principal != "clerk-globex" || !strings.HasPrefix(originalD[0].Detail, "original ") {
		t.Fatalf("the original's leak must be reported as an original defect: %+v", originalD)
	}
}

func TestSplitWithoutPolicyBlamesRewriteForAnyDifference(t *testing.T) {
	// No policy: nothing says which side is right, so a difference is the rewrite's until a person decides.
	orig := leakFor(adminAcme, clerkGlobex)
	rew := leakFor(adminAcme)
	rewriteF, originalD := SplitFindings(nil, principals, orig, rew)
	if !HasCritical(rewriteF) {
		t.Fatalf("a difference in a case with no policy must block: %+v", rewriteF)
	}
	if len(originalD) != 0 {
		t.Fatalf("without a policy the original cannot be shown to be wrong: %+v", originalD)
	}
}

func TestSplitIdenticalSidesAreClean(t *testing.T) {
	good := leakFor(adminAcme)
	rewriteF, originalD := SplitFindings(policy, principals, good, good)
	if len(rewriteF) != 0 || len(originalD) != 0 {
		t.Fatalf("identical sides that match the policy must produce no findings: %+v %+v", rewriteF, originalD)
	}
}

func TestOwnershipAuditFlagsSameTenantMemberWhoCanEdit(t *testing.T) {
	author := domain.Principal{Name: "alice", Role: "member", Tenant: "acme"}
	member := domain.Principal{Name: "bob", Role: "member", Tenant: "acme"}
	ps := []domain.Principal{author, member}
	pol := []domain.CasePolicy{{Case: "edit-post", AllowedPrincipals: []string{"alice"}, OwnerTenant: "acme"}}
	leaky := Matrix{
		MatrixKey("edit-post", "alice"): {Outcome: domain.OutcomeAllow, Status: 200},
		MatrixKey("edit-post", "bob"):   {Outcome: domain.OutcomeAllow, Status: 200},
	}
	f := Audit(pol, ps, nil, leaky)
	if len(f) != 1 || f[0].Principal != "bob" || f[0].Severity != SeverityCritical {
		t.Fatalf("a same-tenant member allowed to edit must be one critical finding for that member: %+v", f)
	}
	clean := Matrix{
		MatrixKey("edit-post", "alice"): {Outcome: domain.OutcomeAllow, Status: 200},
		MatrixKey("edit-post", "bob"):   {Outcome: domain.OutcomeDeny, Status: 403},
	}
	if f := Audit(pol, ps, nil, clean); len(f) != 0 {
		t.Fatalf("an author-only edit must produce no findings: %+v", f)
	}
}

func TestValidatePoliciesNamesUnknownPrincipal(t *testing.T) {
	typo := []domain.CasePolicy{{Case: "edit-post", AllowedPrincipals: []string{"admin-acm"}}}
	err := ValidatePolicies(typo, principals)
	if err == nil || !strings.Contains(err.Error(), "edit-post") || !strings.Contains(err.Error(), "admin-acm") {
		t.Fatalf("an unknown principal must be rejected, naming the case and the name: %v", err)
	}
	known := []domain.CasePolicy{{Case: "get-invoice", AllowedPrincipals: []string{"admin-acme"}}}
	if err := ValidatePolicies(known, principals); err != nil {
		t.Fatalf("a listed principal from the principal file must be accepted: %v", err)
	}
	empty := []domain.CasePolicy{{Case: "get-invoice", AllowedPrincipals: []string{""}}}
	if err := ValidatePolicies(empty, principals); err == nil {
		t.Fatal("an empty principal name must be rejected")
	}
}

func TestRBACBriefNamesFailingRequestsWithoutTokens(t *testing.T) {
	if RBACBrief(nil, invoice) != "" {
		t.Fatal("no findings must give no brief text")
	}
	f := []Finding{{SeverityCritical, "get-invoice", "clerk-globex", "rewrite allow (status 200) but the declared policy expects deny"}}
	text := RBACBrief(f, invoice)
	if !strings.Contains(text, "- GET /invoices/1 as clerk-globex (critical): rewrite allow (status 200)") {
		t.Fatalf("the brief must give the concrete failing request and principal:\n%s", text)
	}
}

// TestRunMatrixRecordsTheDenyReason pins the wiring: every cell carries which refusal its deny was,
// so reports can tell an unauthenticated 401 from a forbidden 403 or a hidden 404 without changing
// the allow/deny decision.
func TestRunMatrixRecordsTheDenyReason(t *testing.T) {
	probe := func(c domain.AppCase, p domain.Principal) (int, error) {
		switch {
		case c.Name == "pub" && p.Name == "anon":
			return 200, nil
		case c.Name == "hidden":
			return 404, nil
		case p.Name == "anon":
			return 401, nil
		default:
			return 403, nil
		}
	}
	cases := []domain.AppCase{{Name: "pub", Kind: "http"}, {Name: "hidden", Kind: "http"}}
	principals := []domain.Principal{{Name: "anon"}, {Name: "alice"}}
	m, err := RunMatrix(cases, principals, probe)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		caseName, principal string
		outcome             domain.Outcome
		reason              domain.DenyReason
	}{
		{"pub", "anon", domain.OutcomeAllow, domain.DenyNone},
		{"hidden", "alice", domain.OutcomeDeny, domain.DenyNotFound},
		{"pub", "alice", domain.OutcomeDeny, domain.DenyForbidden},
	} {
		cell := m[MatrixKey(tc.caseName, tc.principal)]
		if cell.Outcome != tc.outcome || cell.Reason != tc.reason {
			t.Fatalf("%s/%s: want %s/%q, got %s/%q", tc.caseName, tc.principal, tc.outcome, tc.reason, cell.Outcome, cell.Reason)
		}
	}
}
