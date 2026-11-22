package domain

// Principal is one caller the authorization check runs as: a role inside a tenant, with the name of the
// environment variable that holds its token. The token itself is never stored in the principal file.
type Principal struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Tenant   string `json:"tenant,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

// Anonymous is the role of a caller that sends no credential.
const AnonymousRole = "anonymous"

// Outcome is what an authorization check concluded for one request. Only Allow and Deny are authorization
// answers. Other covers everything else (a server error, a bad request): it is never read as an answer.
type Outcome string

const (
	OutcomeAllow Outcome = "allow"
	OutcomeDeny  Outcome = "deny"
	OutcomeOther Outcome = "other"
)

// DenyReason says which kind of refusal a deny outcome was. The allow/deny decision does not depend on it;
// it keeps the three refusals apart in reports, so a 401 (no valid credential) is not read as a 403 (a valid
// credential without permission) or a 404 (the resource is hidden from this caller).
type DenyReason string

const (
	// DenyNone is the reason for an outcome that is not a denial.
	DenyNone DenyReason = ""
	// DenyUnauthenticated is an HTTP 401: the caller sent no valid credential.
	DenyUnauthenticated DenyReason = "unauthenticated"
	// DenyForbidden is an HTTP 403: the caller is known but may not use the case.
	DenyForbidden DenyReason = "forbidden"
	// DenyNotFound is an HTTP 404: the resource is hidden from this caller, often on purpose, so that
	// another tenant's resource does not reveal that it exists.
	DenyNotFound DenyReason = "not_found"
)

// ClassifyStatus maps an HTTP status to an authorization outcome. 2xx is allowed. 401, 403 and 404 are
// denials: a resource in another tenant is usually answered 404 so its existence is not revealed.
// A 5xx is Other, because a crash while checking permissions is a defect, not a denial.
func ClassifyStatus(status int) Outcome {
	switch {
	case status >= 200 && status < 300:
		return OutcomeAllow
	case status == 401, status == 403, status == 404:
		return OutcomeDeny
	default:
		return OutcomeOther
	}
}

// DenyReasonOf names the refusal behind a status. It returns DenyNone for any status that is not a denial.
func DenyReasonOf(status int) DenyReason {
	switch status {
	case 401:
		return DenyUnauthenticated
	case 403:
		return DenyForbidden
	case 404:
		return DenyNotFound
	default:
		return DenyNone
	}
}

// CasePolicy is the declared access rule for one recorded case. It is written by a person from the API's
// intent, not from the code, so the check can compare behavior against intent.
type CasePolicy struct {
	Case string `json:"case"`
	// Public cases are allowed for every principal, including anonymous ones.
	Public bool `json:"public,omitempty"`
	// AllowedRoles are the roles that may use the case, inside the owning tenant.
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	// AllowedPrincipals are the principal names that may use the case, for ownership rules such as "only the
	// author may edit this post". When it is set, only these names are allowed. When it is empty, the role rule
	// applies unchanged. When AllowedRoles is also set, a principal is allowed if it matches either list. The
	// tenant rule below applies to a name match too, so a listed name from another tenant is denied, unless its
	// role is in CrossTenantRoles.
	AllowedPrincipals []string `json:"allowed_principals,omitempty"`
	// OwnerTenant is the tenant that owns the resource. Principals of other tenants are denied, unless
	// their role is listed in CrossTenantRoles.
	OwnerTenant      string   `json:"owner_tenant,omitempty"`
	CrossTenantRoles []string `json:"cross_tenant_roles,omitempty"`
}

// Expect says whether a principal should be allowed by the declared policy.
func (p CasePolicy) Expect(pr Principal) Outcome {
	if p.Public {
		return OutcomeAllow
	}
	if !p.listed(pr) {
		return OutcomeDeny
	}
	if p.OwnerTenant == "" || pr.Tenant == p.OwnerTenant {
		return OutcomeAllow
	}
	if inList(p.CrossTenantRoles, pr.Role) {
		return OutcomeAllow
	}
	return OutcomeDeny
}

// listed reports whether a principal is named by the policy's lists, before the tenant rule is applied.
func (p CasePolicy) listed(pr Principal) bool {
	if len(p.AllowedPrincipals) == 0 {
		return inList(p.AllowedRoles, pr.Role)
	}
	return inList(p.AllowedPrincipals, pr.Name) || inList(p.AllowedRoles, pr.Role)
}

func inList(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
