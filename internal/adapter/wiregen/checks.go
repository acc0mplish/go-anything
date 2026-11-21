package wiregen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// RoundCheck is the record of one probe round: what was tried, what was found, and what was not checked.
// It is written to the project so a later reader can see how much a "converged" result actually covers.
type RoundCheck struct {
	Round           int            `json:"round"`
	Seed            uint64         `json:"seed"`
	Values          int            `json:"values"`
	BoundaryValues  int            `json:"boundary_values"`
	GeneratedValues int            `json:"generated_values"`
	ValueClasses    map[string]int `json:"value_classes"`
	MissingClasses  []string       `json:"missing_classes"`
	Requests        int            `json:"requests"`
	// ShapeRequests is how many of Requests vary the request shape (query string, doubled slash, trailing
	// dot, lower-case method) rather than the value. HTTP units only; 0 for a command-line unit.
	ShapeRequests   int      `json:"shape_requests,omitempty"`
	Disagreements   int      `json:"disagreements"`
	NewCases        []string `json:"new_cases"`
	OriginalDefects int      `json:"original_defects"`
	Converged       bool     `json:"converged"`
	// CleanStreak is the number of consecutive clean rounds ending at this one. A round with a disagreement or
	// a blocking finding is 0. Converged needs 2, or 1 for a single-round run (which also warns).
	CleanStreak int `json:"clean_streak"`
	// RBACChecked is true only when the authorization check ran in this round; it runs where the probes agree.
	RBACChecked    bool              `json:"rbac_checked"`
	RBACPrincipals int               `json:"rbac_principals"`
	RBACFindings   []usecase.Finding `json:"rbac_findings"`
	AgentOutcome   string            `json:"agent_outcome,omitempty"`
	// DefectPolicy is the policy this run applied to original crashes, so a report can say truthfully
	// whether a crash was recorded as behavior or reported as a defect. Empty in records written before
	// the field existed.
	DefectPolicy string `json:"defect_policy,omitempty"`
	// ExtraChecked is the number of -extra-inputs values probed on both sides after the loop converged.
	// 0 means no extra input was given (or the run did not converge, so the check did not run).
	ExtraChecked int `json:"extra_checked,omitempty"`
	// ExtraRequests is the number of requests those values became (same construction as the probes).
	ExtraRequests int `json:"extra_requests,omitempty"`
	// ExtraDisagreements lists the extra inputs where the rewrite and the original disagree. Any entry
	// makes the run not converged.
	ExtraDisagreements []ExtraDisagreement `json:"extra_disagreements,omitempty"`
	Confidence         string              `json:"confidence"`
	NotChecked         []string            `json:"not_checked"`
}

// ExtraDisagreement is one extra input where the answers differ. Want and Got are short, clipped
// descriptions (usecase.AnswerText); crash text in them is masked.
type ExtraDisagreement struct {
	Value   string `json:"value"`
	Request string `json:"request"`
	Want    string `json:"want"`
	Got     string `json:"got"`
}

// notCheckedCommon lists what every round leaves unchecked, whatever it found.
var notCheckedCommon = []string{
	"inputs outside the probe classes (see the generator in internal/domain/probe.go and fuzzgen.go)",
	"original behavior that depends on time, randomness, or state from earlier requests",
	"concurrent requests and their interleavings",
}

// classCoverage counts the values in each input class. Every class in domain.ProbeClasses has a key,
// so a zero count is explicit. missing lists the classes with no value, in ProbeClasses order.
func classCoverage(values []string) (counts map[string]int, missing []string) {
	counts = map[string]int{}
	for _, c := range domain.ProbeClasses() {
		counts[c] = 0
	}
	for _, v := range values {
		counts[domain.ProbeClassOf(v)]++
	}
	missing = []string{}
	for _, c := range domain.ProbeClasses() {
		if counts[c] == 0 {
			missing = append(missing, c)
		}
	}
	return counts, missing
}

// newRoundCheck builds a round record from the counts. Confidence says what a clean round is evidence of.
func newRoundCheck(round int, seed uint64, values []string, requests, disagreements int, newCases []string, defects int, converged bool) RoundCheck {
	boundary := 0
	for _, v := range values {
		if domain.ProbeBoundary(v) {
			boundary++
		}
	}
	classes, missing := classCoverage(values)
	confidence := "sampled agreement: a clean round means these inputs agreed, not that every input does"
	if disagreements > 0 {
		confidence = "disagreement found: the listed cases are recorded and the rewrite must match them"
	}
	return RoundCheck{
		Round: round, Seed: seed, Values: len(values), BoundaryValues: boundary,
		GeneratedValues: len(values) - boundary, ValueClasses: classes, MissingClasses: missing,
		Requests: requests, Disagreements: disagreements,
		NewCases: newCases, OriginalDefects: defects, Converged: converged,
		Confidence: confidence, NotChecked: notCheckedCommon,
	}
}

// WriteRoundCheck saves one round under <root>/.go-anything/checks/<unit>/round-NN.json.
func WriteRoundCheck(root, unit string, c RoundCheck) (string, error) {
	dir := filepath.Join(root, ".go-anything", "checks", unit)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("checks: create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("checks: encode round %d: %w", c.Round, err)
	}
	p := filepath.Join(dir, fmt.Sprintf("round-%02d.json", c.Round))
	if err := os.WriteFile(p, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("checks: write %s: %w", p, err)
	}
	return p, nil
}
