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
	// ConcurrentChecked is the number of the unit's recorded cases the concurrent-request check asked
	// (-concurrency, HTTP units only). 0 means the check did not run, so the record keeps its old shape.
	ConcurrentChecked int `json:"concurrent_checked,omitempty"`
	// ConcurrentDiverges is how many of those cases the rewrite answered inconsistently under the
	// concurrent load. Any entry makes the round not converged. That blocking rule is a go-anything
	// extension: the ported original treats a divergent rewrite answer as a report item outside the
	// convergence loop.
	ConcurrentDiverges int `json:"concurrent_diverges,omitempty"`
	// ConcurrentPending lists the cases whose verdict stayed uncertain (a failed send, or a
	// nondeterministic original). Pending does not block convergence; the names stay on the record so a
	// pending case cannot quietly disappear.
	ConcurrentPending []string `json:"concurrent_pending,omitempty"`
	// ChainsChecked is the number of collection targets the consecutive-request check exercised
	// (-chains, HTTP units only). 0 means the check did not run, so the record keeps its old shape.
	ChainsChecked int `json:"chains_checked,omitempty"`
	// ChainRequests is the total number of chain steps the check sent to each side in this round.
	ChainRequests int `json:"chain_requests,omitempty"`
	// ChainDiverges is how many chains diverged. Any divergence makes the round not converged, per the
	// ported contract.
	ChainDiverges int `json:"chain_diverges,omitempty"`
	// ChainFirstDivergence is the step index, counted from 1, of the first divergence in the first
	// diverging chain; 0 records nothing (which is why the count is 1-based: an omitempty field must
	// stay out of records that did not diverge, and divergence at step 0 would otherwise vanish).
	ChainFirstDivergence int `json:"chain_first_divergence,omitempty"`
	// ChainPending lists the collection targets whose chain the check could not judge (a failed send,
	// or a 5xx or step-count surprise from the original). An uncertain chain verdict does block
	// convergence, unlike a concurrent one — that is the ported contract — and the names stay on the
	// record so a pending target cannot quietly disappear.
	ChainPending []string `json:"chain_pending,omitempty"`
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

const (
	// concurrentInterleavingsNote is the line of notCheckedCommon the concurrent check narrows.
	concurrentInterleavingsNote = "concurrent requests and their interleavings"
	// notCheckedConcurrentOn replaces that line for a round that ran the concurrent check: identical-request
	// consistency was checked for this unit's recorded cases, so only interleavings of distinct requests
	// remain unchecked. A round without the check keeps the common line, so its record stays byte-identical.
	notCheckedConcurrentOn = "identical-request consistency checked for this unit's recorded cases; distinct-request interleavings remain unexplored"
	// chainStateNote is the line of notCheckedCommon the chain check narrows.
	chainStateNote = "original behavior that depends on time, randomness, or state from earlier requests"
	// notCheckedChainOn replaces that line for a round that ran the chain check: state built from
	// earlier requests was exercised, but only through the check's own reserved-name resources on
	// recorded collections, so the honest claim is "partially covered".
	notCheckedChainOn = "state from earlier requests only partially covered — chain probes touch their own reserved-name resources on recorded collections; verbs not seen in the recording are not exercised; list reads and cross-resource interactions against accumulated original state are not checked"
)

// notCheckedList returns the not_checked list for one round. A round that ran a check narrows only
// that check's line; every other line keeps the common wording.
func notCheckedList(concurrentOn, chainOn bool) []string {
	if !concurrentOn && !chainOn {
		return notCheckedCommon
	}
	out := make([]string, 0, len(notCheckedCommon))
	for _, s := range notCheckedCommon {
		if concurrentOn && s == concurrentInterleavingsNote {
			s = notCheckedConcurrentOn
		}
		if chainOn && s == chainStateNote {
			s = notCheckedChainOn
		}
		out = append(out, s)
	}
	return out
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
