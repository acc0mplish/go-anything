package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/wiregen"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// cmdRBAC asks every case as every principal, against the rewrite and optionally the original, and audits
// the answers. Authorization is checked on the allow or deny outcome only, never on response bodies.
// Tokens are read from the environment variables the principal file names; they are not printed or stored.
func cmdRBAC(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rbac", flag.ContinueOnError)
	casesPath := fs.String("cases", "", "app cases file (JSON array of http cases), required")
	principalsPath := fs.String("principals", "", "principal file (JSON array), required")
	policyPath := fs.String("policy", "", "declared access policy file (JSON array), required; a case may list allowed_principals by name")
	base := fs.String("base", "", "URL of the rewrite, required")
	original := fs.String("original", "", "URL of the original; enables the divergence check")
	verbose := fs.Bool("v", false, "print every request's status and outcome, not only the findings")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *casesPath == "" || *principalsPath == "" || *policyPath == "" || *base == "" {
		return errors.New("rbac: -cases, -principals, -policy and -base are required")
	}
	var cases []domain.AppCase
	var principals []domain.Principal
	var policies []domain.CasePolicy
	if err := readJSONFile(*casesPath, &cases); err != nil {
		return err
	}
	if err := readJSONFile(*principalsPath, &principals); err != nil {
		return err
	}
	if err := readJSONFile(*policyPath, &policies); err != nil {
		return err
	}
	if err := usecase.ValidatePolicies(policies, principals); err != nil {
		return err
	}
	rewriteM, err := usecase.RunMatrix(cases, principals, wiregen.ProbeWith(wiregen.RBACRequest, *base))
	if err != nil {
		return err
	}
	var originalM usecase.Matrix
	if *original != "" {
		originalM, err = usecase.RunMatrix(cases, principals, wiregen.ProbeWith(wiregen.RBACRequest, *original))
		if err != nil {
			return err
		}
	}
	findings := usecase.Audit(policies, principals, originalM, rewriteM)
	if *verbose {
		printCells(stdout, "rewrite", rewriteM, cases, principals)
		if *original != "" {
			printCells(stdout, "original", originalM, cases, principals)
		}
	}
	printFindings(stdout, findings, len(cases)*len(principals))
	if unreviewed := casesWithoutPolicy(cases, policies); len(unreviewed) > 0 {
		fmt.Fprintf(stdout, "rbac: %d case(s) have no declared policy and were compared only with each other: %s\n",
			len(unreviewed), strings.Join(unreviewed, ", "))
	}
	if usecase.HasCritical(findings) {
		return fmt.Errorf("rbac: %d critical authorization finding(s)", countSeverity(findings, usecase.SeverityCritical))
	}
	return nil
}

// rbacOptionsFromFlags loads the authorization check that wire refine runs at convergence. The two files go
// together, and the check applies to HTTP units only: a command-line unit has no URL to probe with a token.
// It returns nil when neither flag is set, so a round without the check is recorded as such.
func rbacOptionsFromFlags(principalsPath, policyPath string, httpUnit bool) (*wiregen.RBACOptions, error) {
	if principalsPath == "" && policyPath == "" {
		return nil, nil
	}
	if principalsPath == "" || policyPath == "" {
		return nil, errors.New("wire refine: -rbac-principals and -rbac-policy must be given together")
	}
	if !httpUnit {
		return nil, errors.New("wire refine: -rbac-principals and -rbac-policy apply to HTTP units only; this unit is a command-line unit")
	}
	var principals []domain.Principal
	var policies []domain.CasePolicy
	if err := readJSONFile(principalsPath, &principals); err != nil {
		return nil, err
	}
	if err := readJSONFile(policyPath, &policies); err != nil {
		return nil, err
	}
	if len(principals) == 0 {
		return nil, fmt.Errorf("wire refine: %s lists no principals", principalsPath)
	}
	if err := usecase.ValidatePolicies(policies, principals); err != nil {
		return nil, err
	}
	return &wiregen.RBACOptions{Principals: principals, Policies: policies}, nil
}

// readJSONFile loads one input file in a fixed order, so the first failure reported is always the same.
func readJSONFile(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("rbac: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("rbac: parse %s: %w", path, err)
	}
	return nil
}

func printCells(w io.Writer, side string, m usecase.Matrix, cases []domain.AppCase, principals []domain.Principal) {
	for _, c := range cases {
		for _, p := range principals {
			cell, ok := m[usecase.MatrixKey(c.Name, p.Name)]
			if !ok {
				fmt.Fprintf(w, "  %-8s %-24s %-16s (no answer)\n", side, c.Name, p.Name)
				continue
			}
			reason := ""
			if cell.Outcome == domain.OutcomeDeny && cell.Reason != "" {
				reason = " (" + string(cell.Reason) + ")"
			}
			fmt.Fprintf(w, "  %-8s %-24s %-16s status %-4d %s%s\n", side, c.Name, p.Name, cell.Status, cell.Outcome, reason)
		}
	}
}

func printFindings(w io.Writer, findings []usecase.Finding, requests int) {
	fmt.Fprintf(w, "rbac: %d request(s) checked, %d finding(s)\n", requests, len(findings))
	for _, f := range findings {
		fmt.Fprintf(w, "  [%s] %s as %s: %s\n", strings.ToUpper(string(f.Severity)), f.Case, f.Principal, f.Detail)
	}
	if len(findings) == 0 {
		fmt.Fprintln(w, "rbac: no authorization differences from the declared policy")
	}
}

// casesWithoutPolicy lists cases no policy describes. Their answers can only be compared with the original,
// never with the intended access rule, so a person must review them.
func casesWithoutPolicy(cases []domain.AppCase, policies []domain.CasePolicy) []string {
	covered := map[string]bool{}
	for _, p := range policies {
		covered[p.Case] = true
	}
	var out []string
	for _, c := range cases {
		if !covered[c.Name] {
			out = append(out, c.Name)
		}
	}
	return out
}

func countSeverity(findings []usecase.Finding, s usecase.Severity) int {
	n := 0
	for _, f := range findings {
		if f.Severity == s {
			n++
		}
	}
	return n
}
