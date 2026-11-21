package domain

import (
	"errors"
	"fmt"
)

// Verdict states for a single replayed case.
type CaseStatus string

const (
	StatusMatch    CaseStatus = "match"
	StatusMismatch CaseStatus = "mismatch"
	StatusError    CaseStatus = "error"
)

// ErrInvalidCase is returned when a case violates its invariants.
var ErrInvalidCase = errors.New("domain: invalid case")

// Describe returns a short, human-readable label for a status.
func (s CaseStatus) Describe() string {
	switch s {
	case StatusMatch:
		return "match"
	case StatusMismatch:
		return "mismatch"
	case StatusError:
		return "error"
	default:
		return fmt.Sprintf("unknown(%s)", string(s))
	}
}
