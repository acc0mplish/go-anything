package usecase

import (
	"fmt"
)

// DefectPolicy is the operator's choice of what a crash of the original program means.
//
// PolicyDefect (the default) treats a crash as an original defect: it is reported and never recorded,
// because the rewrite is not expected to copy it. PolicyExpected treats a crash as the behavior to match
// for compatibility: it is recorded like any other answer.
type DefectPolicy string

const (
	// PolicyDefect reports a crash of the original as a defect and never records it.
	PolicyDefect DefectPolicy = "defect"
	// PolicyExpected records a crash of the original as the behavior the rewrite must reproduce.
	PolicyExpected DefectPolicy = "expected"
)

// ParseDefectPolicy maps an operator-supplied name to a DefectPolicy. The empty string selects
// PolicyDefect, the default. Any other value must match a valid name exactly; otherwise an error is
// returned that lists the valid names.
func ParseDefectPolicy(s string) (DefectPolicy, error) {
	switch DefectPolicy(s) {
	case "", PolicyDefect:
		return PolicyDefect, nil
	case PolicyExpected:
		return PolicyExpected, nil
	}
	return "", fmt.Errorf("unknown defect policy %q: valid values are %q and %q",
		s, string(PolicyDefect), string(PolicyExpected))
}

// PolicySource names the place that decided an effective defect policy, strongest first.
type PolicySource string

const (
	// SourceFlag means the operator gave -defect-policy for this run.
	SourceFlag PolicySource = "flag"
	// SourceUnit means the settings file has an entry for this unit in unit_defect_policy.
	SourceUnit PolicySource = "unit"
	// SourceFile means the settings file's defect_policy applies to every unit without its own entry.
	SourceFile PolicySource = "file"
	// SourceDefault means nothing was set, so PolicyDefect applies.
	SourceDefault PolicySource = "default"
)

// PolicySettings is what a settings file declares about the defect policy. A nil Default and a unit
// missing from Units mean the file says nothing for that case.
type PolicySettings struct {
	Default *DefectPolicy
	Units   map[string]DefectPolicy
}

// ResolveDefectPolicy returns the policy for one unit and the source that decided it. Precedence, strongest
// first: the -defect-policy flag (flag is nil when the operator did not give it), the unit's own entry, the
// file's default, then PolicyDefect. An entry for another unit never applies to this one.
func ResolveDefectPolicy(unit string, flag *DefectPolicy, s PolicySettings) (DefectPolicy, PolicySource) {
	if flag != nil {
		return *flag, SourceFlag
	}
	if p, ok := s.Units[unit]; ok {
		return p, SourceUnit
	}
	if s.Default != nil {
		return *s.Default, SourceFile
	}
	return PolicyDefect, SourceDefault
}

// RecordsCrash reports whether a crash of the original is recorded as behavior to match. It is true only
// for PolicyExpected. Any other value, including one built without ParseDefectPolicy, returns false, so an
// unrecognised policy never causes a crash to be recorded.
func (p DefectPolicy) RecordsCrash() bool {
	return p == PolicyExpected
}
