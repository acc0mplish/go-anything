package usecase

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// Cell is one request made as one principal: the authorization outcome and the status it came from.
type Cell struct {
	Outcome domain.Outcome
	Status  int
	// Reason names which refusal a deny was, so a report can tell an unauthenticated 401 from a
	// forbidden 403 or a hidden 404. The allow/deny decision itself never depends on it.
	Reason domain.DenyReason
}

// Matrix holds one side's answers, keyed by case and principal.
type Matrix map[string]Cell

// MatrixKey is the key of a case and principal in a Matrix.
func MatrixKey(caseName, principal string) string {
	return caseName + "\x00" + principal
}

// Probe makes one request as one principal and returns the HTTP status it got.
type Probe func(c domain.AppCase, p domain.Principal) (int, error)

// RunMatrix asks every case as every principal. A probe error stops the run: a request that could not be
// made, for example because a token variable is unset, is not an authorization answer.
func RunMatrix(cases []domain.AppCase, principals []domain.Principal, probe Probe) (Matrix, error) {
	m := Matrix{}
	for _, c := range cases {
		for _, p := range principals {
			status, err := probe(c, p)
			if err != nil {
				return nil, fmt.Errorf("rbac: case %q as %q: %w", c.Name, p.Name, err)
			}
			m[MatrixKey(c.Name, p.Name)] = Cell{Outcome: domain.ClassifyStatus(status), Status: status,
				Reason: domain.DenyReasonOf(status)}
		}
	}
	return m, nil
}

// Severity ranks a finding. A critical finding is an authorization fault: it can expose data across
// tenants or roles. A high finding is a server error where an authorization answer was expected.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
)

// Finding is one authorization fault, with the case and principal it concerns.
type Finding struct {
	Severity  Severity `json:"severity"`
	Case      string   `json:"case"`
	Principal string   `json:"principal"`
	Detail    string   `json:"detail"`
}

// Audit compares answers with the declared policy and with each other. It reports every mismatch, not just
// the first: in an authorization review the complete list is the evidence. Original and rewrite may be nil
// when only one side was measured.
func Audit(policies []domain.CasePolicy, principals []domain.Principal, original, rewrite Matrix) []Finding {
	byName := map[string]domain.Principal{}
	for _, p := range principals {
		byName[p.Name] = p
	}
	var out []Finding
	for _, side := range []struct {
		name string
		m    Matrix
	}{{"original", original}, {"rewrite", rewrite}} {
		if side.m == nil {
			continue
		}
		for _, pol := range policies {
			for _, pr := range principals {
				cell, ok := side.m[MatrixKey(pol.Case, pr.Name)]
				if !ok {
					out = append(out, Finding{SeverityHigh, pol.Case, pr.Name,
						fmt.Sprintf("%s has no answer for this case", side.name)})
					continue
				}
				expect := pol.Expect(pr)
				switch {
				case cell.Outcome == expect:
				case cell.Outcome == domain.OutcomeOther:
					out = append(out, Finding{SeverityHigh, pol.Case, pr.Name,
						fmt.Sprintf("%s answered %d (a server error) where %s was expected", side.name, cell.Status, expect)})
				default:
					out = append(out, Finding{SeverityCritical, pol.Case, pr.Name,
						fmt.Sprintf("%s %s (status %d) but the declared policy expects %s", side.name, cell.Outcome, cell.Status, expect)})
				}
			}
		}
	}
	if original != nil && rewrite != nil {
		for key, o := range original {
			r, ok := rewrite[key]
			if !ok || o.Outcome == r.Outcome {
				continue
			}
			caseName, principal := splitKey(key)
			if _, known := byName[principal]; !known {
				continue
			}
			out = append(out, Finding{SeverityCritical, caseName, principal,
				fmt.Sprintf("authorization differs: original %s (status %d), rewrite %s (status %d)", o.Outcome, o.Status, r.Outcome, r.Status)})
		}
	}
	sortFindings(out)
	return out
}

// sortFindings puts critical findings first, then orders each severity by case and principal.
func sortFindings(out []Finding) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SeverityCritical
		}
		if out[i].Case != out[j].Case {
			return out[i].Case < out[j].Case
		}
		return out[i].Principal < out[j].Principal
	})
}

// SplitFindings sorts an audit into what the rewrite must fix and what is the original's defect.
//
// Audit does not say which side of a cross-side difference is wrong: its "authorization differs" finding
// fires whether the original or the rewrite is off. The side is therefore decided with Audit's own per-side
// rule, an answer is right when it equals the policy's expectation. A difference is the rewrite's fault
// unless the rewrite agrees with the policy. A case with no declared policy has no expectation to agree with,
// so a difference there is the rewrite's. A policy violation by the original is an original defect.
func SplitFindings(policies []domain.CasePolicy, principals []domain.Principal, original, rewrite Matrix) (rewriteFindings, originalDefects []Finding) {
	rewriteFindings = Audit(policies, principals, nil, rewrite)
	if original != nil {
		originalDefects = Audit(policies, principals, original, nil)
	}
	if original == nil || rewrite == nil {
		return rewriteFindings, originalDefects
	}
	policyOf := map[string]domain.CasePolicy{}
	for _, p := range policies {
		policyOf[p.Case] = p
	}
	byName := map[string]domain.Principal{}
	for _, p := range principals {
		byName[p.Name] = p
	}
	for _, f := range Audit(nil, principals, original, rewrite) {
		pol, hasPolicy := policyOf[f.Case]
		cell := rewrite[MatrixKey(f.Case, f.Principal)]
		if hasPolicy && cell.Outcome == pol.Expect(byName[f.Principal]) {
			// The rewrite matches the policy, so the original is the side that differs from intent. Its
			// violation is already in originalDefects, so this difference is not the rewrite's.
			continue
		}
		rewriteFindings = append(rewriteFindings, f)
	}
	sortFindings(rewriteFindings)
	return rewriteFindings, originalDefects
}

// RBACBrief is the part of the agent's brief that names the rewrite's authorization failures. Each line is
// one concrete request as one principal, with the answer and the expectation. It names principals, never tokens.
func RBACBrief(findings []Finding, cases []domain.AppCase) string {
	if len(findings) == 0 {
		return ""
	}
	byName := map[string]domain.AppCase{}
	for _, c := range cases {
		byName[c.Name] = c
	}
	var b strings.Builder
	b.WriteString("\nThe authorization check found the rewrite answering these requests differently from the declared policy or from the original. Fix every one. Each line is one request as one principal:\n")
	for _, f := range findings {
		request := f.Case
		if c, ok := byName[f.Case]; ok {
			request = c.Method + " " + c.Path
		}
		fmt.Fprintf(&b, "- %s as %s (%s): %s\n", request, f.Principal, f.Severity, f.Detail)
	}
	return b.String()
}

// ValidatePolicies rejects a policy that names a principal the principal file does not contain. Without this
// check a typo in allowed_principals silently denies the real author, and the audit reports the denial as
// the declared expectation.
func ValidatePolicies(policies []domain.CasePolicy, principals []domain.Principal) error {
	known := map[string]bool{}
	for _, p := range principals {
		known[p.Name] = true
	}
	for _, pol := range policies {
		for _, name := range pol.AllowedPrincipals {
			if name == "" {
				return fmt.Errorf("rbac: policy for case %q lists an empty principal name", pol.Case)
			}
			if !known[name] {
				return fmt.Errorf("rbac: policy for case %q names principal %q, which is not in the principal file", pol.Case, name)
			}
		}
	}
	return nil
}

// ValidateCaseNames rejects a policy whose case name matches no recorded case. Before this check, a policy
// whose case name was misspelled or stale was silently dropped: Audit iterates only the recorded cases, so the
// policy produced no finding and the gap went unreported. A case with no policy is not an error here; the
// caller reports it as a warning, because such a case is still compared with the original.
func ValidateCaseNames(policies []domain.CasePolicy, cases []domain.AppCase) error {
	recorded := map[string]bool{}
	for _, c := range cases {
		recorded[c.Name] = true
	}
	seen := map[string]bool{}
	for _, pol := range policies {
		if pol.Case == "" {
			return fmt.Errorf("rbac: a policy has an empty case name")
		}
		if seen[pol.Case] {
			return fmt.Errorf("rbac: case %q has more than one policy", pol.Case)
		}
		seen[pol.Case] = true
		if !recorded[pol.Case] {
			return fmt.Errorf("rbac: policy names case %q, which matches no recorded case, so the policy would never be checked", pol.Case)
		}
	}
	return nil
}

func splitKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// HasCritical reports whether any finding is critical. A critical finding fails the check.
func HasCritical(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityCritical {
			return true
		}
	}
	return false
}
