package wiregen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// rbacMainFile is the program's main file, where wire gen puts the mount by default.
const rbacMainFile = "main.go"

// RBACOptions is the authorization check that refine adds to convergence: every recorded HTTP case asked as
// every principal, on the rewrite and on the original, judged against the declared policy.
type RBACOptions struct {
	Principals []domain.Principal
	Policies   []domain.CasePolicy
}

// RBACReport is the outcome of one authorization check. Findings are the rewrite's faults. OriginalDefects
// are the original's own policy violations: they are reported, and they do not block convergence.
type RBACReport struct {
	Principals      int
	Findings        []usecase.Finding
	OriginalDefects []usecase.Finding
}

// RBACRequest builds the request for one case and principal. The Authorization header comes from the
// principal's token variable, expanded by the app runner at send time; the token itself is never stored.
func RBACRequest(c domain.AppCase, p domain.Principal) domain.AppCase {
	headers := map[string]string{}
	for k, v := range c.RequestHeaders {
		// A case must never carry its own credential: the principal decides who is asking.
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		headers[k] = v
	}
	if p.TokenEnv != "" {
		headers["Authorization"] = "Bearer ${" + p.TokenEnv + "}"
	}
	c.RequestHeaders = nil
	if len(headers) > 0 {
		c.RequestHeaders = headers
	}
	return c
}

// ProbeWith returns a usecase.Probe that sends each request to one base URL and reports the status it got.
func ProbeWith(build func(domain.AppCase, domain.Principal) domain.AppCase, base string) usecase.Probe {
	runner := app.Runner{BaseURL: base}
	return func(c domain.AppCase, p domain.Principal) (int, error) {
		got, err := runner.Do(build(c, p))
		if err != nil {
			return 0, err
		}
		return got.Status, nil
	}
}

// CheckRBAC starts the rewrite the way wire verify starts it, asks every case as every principal on the
// rewrite and on the original, and splits the findings. A probe that cannot be made is an error, never an
// answer: a check that did not run must not look like a check that passed.
func CheckRBAC(root, unit string, cases []domain.AppCase, rb RBACOptions, originalURL string) (RBACReport, error) {
	// A policy naming no recorded case would never be checked; refuse before anything starts.
	if err := usecase.ValidateCaseNames(rb.Policies, cases); err != nil {
		return RBACReport{}, err
	}

	if len(cases) == 0 {
		return RBACReport{}, fmt.Errorf("rbac: unit %s has no recorded HTTP case, so there is nothing to check", unit)
	}
	dir, err := os.MkdirTemp("", "go-anything-rbac-*")
	if err != nil {
		return RBACReport{}, err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "app")
	if err := BuildApp(root, bin); err != nil {
		return RBACReport{}, err
	}
	addr, err := ListenAddress(root, rbacMainFile)
	if err != nil {
		return RBACReport{}, err
	}
	// No free-port pre-check: another process could take the port in the window between such a check
	// and the start. The caller owns the process it started; a program that exits during the check
	// fails it, because after the exit any listener on the address belongs to someone else.
	stop, exited, err := StartAppOwned(bin, UnitEnv(unit, "", originalURL), addr)
	if err != nil {
		return RBACReport{}, err
	}
	defer stop()
	rewriteM, err := usecase.RunMatrix(cases, rb.Principals, ProbeWith(RBACRequest, "http://"+addr))
	if err != nil {
		return RBACReport{}, err
	}
	select {
	case <-exited:
		return RBACReport{}, fmt.Errorf("rbac: the program exited while it was being checked, so its answers do not come from the rewrite")
	default:
	}
	originalM, err := usecase.RunMatrix(cases, rb.Principals, ProbeWith(RBACRequest, originalURL))
	if err != nil {
		return RBACReport{}, err
	}
	findings, defects := usecase.SplitFindings(rb.Policies, rb.Principals, originalM, rewriteM)
	return RBACReport{Principals: len(rb.Principals), Findings: findings, OriginalDefects: defects}, nil
}

// roundConverged is the convergence decision for one round. A round converges only when the probes agree
// with the original and no critical authorization finding blocks it. High findings are recorded, not blocking.
func roundConverged(disagreements int, findings []usecase.Finding) bool {
	return disagreements == 0 && !usecase.HasCritical(findings)
}

// criticalCount counts the critical findings, for the messages that say why a round did not converge.
func criticalCount(findings []usecase.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == usecase.SeverityCritical {
			n++
		}
	}
	return n
}
