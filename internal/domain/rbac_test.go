package domain

import (
	"encoding/json"
	"testing"
)

func TestClassifyStatusSeparatesAnswersFromErrors(t *testing.T) {
	cases := map[int]Outcome{200: OutcomeAllow, 204: OutcomeAllow, 401: OutcomeDeny, 403: OutcomeDeny,
		404: OutcomeDeny, 400: OutcomeOther, 500: OutcomeOther, 503: OutcomeOther}
	for status, want := range cases {
		if got := ClassifyStatus(status); got != want {
			t.Fatalf("status %d: want %s, got %s", status, want, got)
		}
	}
}

func TestExpectFollowsRoleAndTenant(t *testing.T) {
	p := CasePolicy{Case: "get-invoice", AllowedRoles: []string{"admin", "clerk"}, OwnerTenant: "acme"}
	acmeClerk := Principal{Role: "clerk", Tenant: "acme"}
	globexClerk := Principal{Role: "clerk", Tenant: "globex"}
	anon := Principal{Role: AnonymousRole}
	viewer := Principal{Role: "viewer", Tenant: "acme"}
	if p.Expect(acmeClerk) != OutcomeAllow {
		t.Fatal("an allowed role in the owning tenant must be allowed")
	}
	if p.Expect(globexClerk) != OutcomeDeny {
		t.Fatal("an allowed role in another tenant must be denied")
	}
	if p.Expect(anon) != OutcomeDeny || p.Expect(viewer) != OutcomeDeny {
		t.Fatal("anonymous callers and roles not listed must be denied")
	}
}

func TestExpectCrossTenantRoleIsAllowedEverywhere(t *testing.T) {
	p := CasePolicy{Case: "list-tenants", AllowedRoles: []string{"platform"}, OwnerTenant: "acme", CrossTenantRoles: []string{"platform"}}
	if p.Expect(Principal{Role: "platform", Tenant: "globex"}) != OutcomeAllow {
		t.Fatal("a listed cross-tenant role must be allowed in any tenant")
	}
}

func TestOwnershipAllowsOnlyTheListedAuthor(t *testing.T) {
	p := CasePolicy{Case: "edit-post", AllowedPrincipals: []string{"alice"}, OwnerTenant: "acme"}
	if p.Expect(Principal{Name: "alice", Role: "member", Tenant: "acme"}) != OutcomeAllow {
		t.Fatal("the listed author in the owning tenant must be allowed")
	}
	if p.Expect(Principal{Name: "bob", Role: "member", Tenant: "acme"}) != OutcomeDeny {
		t.Fatal("another member of the same tenant must be denied")
	}
	if p.Expect(Principal{Name: "anon", Role: AnonymousRole}) != OutcomeDeny {
		t.Fatal("an anonymous caller must be denied")
	}
}

func TestOwnershipDeniesSameNameFromAnotherTenant(t *testing.T) {
	p := CasePolicy{Case: "edit-post", AllowedPrincipals: []string{"alice"}, OwnerTenant: "acme"}
	if p.Expect(Principal{Name: "alice", Role: "member", Tenant: "globex"}) != OutcomeDeny {
		t.Fatal("a listed name from another tenant must be denied")
	}
}

func TestEmptyPrincipalListKeepsRoleRule(t *testing.T) {
	for _, list := range [][]string{nil, {}} {
		p := CasePolicy{Case: "delete-post", AllowedRoles: []string{"admin"}, AllowedPrincipals: list, OwnerTenant: "acme"}
		if p.Expect(Principal{Name: "root", Role: "admin", Tenant: "acme"}) != OutcomeAllow {
			t.Fatal("with no principal list, an allowed role in the owning tenant must be allowed")
		}
		if p.Expect(Principal{Name: "bob", Role: "member", Tenant: "acme"}) != OutcomeDeny {
			t.Fatal("with no principal list, a role not listed must be denied")
		}
	}
}

func TestPrincipalAndRoleListsAllowEitherMatch(t *testing.T) {
	p := CasePolicy{Case: "delete-post", AllowedRoles: []string{"admin"}, AllowedPrincipals: []string{"alice"}, OwnerTenant: "acme"}
	if p.Expect(Principal{Name: "alice", Role: "member", Tenant: "acme"}) != OutcomeAllow {
		t.Fatal("a listed name must be allowed when a role list is also set")
	}
	if p.Expect(Principal{Name: "root", Role: "admin", Tenant: "acme"}) != OutcomeAllow {
		t.Fatal("a listed role must be allowed when a name list is also set")
	}
	if p.Expect(Principal{Name: "bob", Role: "member", Tenant: "acme"}) != OutcomeDeny {
		t.Fatal("a principal matching neither list must be denied")
	}
	if p.Expect(Principal{Name: "root", Role: "admin", Tenant: "globex"}) != OutcomeDeny {
		t.Fatal("a role match from another tenant must still be denied")
	}
}

func TestAllowedPrincipalsJSONTag(t *testing.T) {
	var p CasePolicy
	if err := json.Unmarshal([]byte(`{"case":"edit-post","allowed_principals":["alice"]}`), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.AllowedPrincipals) != 1 || p.AllowedPrincipals[0] != "alice" {
		t.Fatalf("allowed_principals must decode into AllowedPrincipals: %+v", p)
	}
}

func TestPublicCaseAllowsAnonymous(t *testing.T) {
	p := CasePolicy{Case: "health", Public: true}
	if p.Expect(Principal{Role: AnonymousRole}) != OutcomeAllow {
		t.Fatal("a public case must be allowed for anonymous callers")
	}
}

func TestDenyReasonKeepsUnauthenticatedForbiddenAndNotFoundApart(t *testing.T) {
	cases := map[int]DenyReason{401: DenyUnauthenticated, 403: DenyForbidden, 404: DenyNotFound,
		200: DenyNone, 400: DenyNone, 500: DenyNone}
	for status, want := range cases {
		if got := DenyReasonOf(status); got != want {
			t.Fatalf("status %d: want reason %q, got %q", status, want, got)
		}
	}
	if ClassifyStatus(401) != OutcomeDeny || ClassifyStatus(403) != OutcomeDeny || ClassifyStatus(404) != OutcomeDeny {
		t.Fatal("the allow/deny decision must not change: 401, 403 and 404 are still denials")
	}
}
