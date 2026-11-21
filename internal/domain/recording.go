// Package domain holds the core rules. It imports nothing outside the standard library.
package domain

import (
	"errors"
	"fmt"
)

// ErrEmptyRecording is returned when a recording has no cases to verify against.
var ErrEmptyRecording = errors.New("domain: recording has no cases")

// Case is one recorded input and the output the original produced for it.
type Case struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output"`
	// Error is non-empty when the original failed for this input; the rewrite must fail too.
	Error string `json:"error,omitempty"`
}

// Recording is the set of cases captured from the original implementation.
type Recording struct {
	// Subject names the unit this recording belongs to (for reports and fingerprints).
	Subject string `json:"subject,omitempty"`
	// Source fingerprints the original's source at record time. Nil when it was not captured.
	Source *Fingerprint `json:"source,omitempty"`
	// Coverage is the statement coverage the cases reached in the original, when measured.
	Coverage *Coverage `json:"coverage,omitempty"`
	Cases    []Case    `json:"cases"`
}

// Validate enforces the invariants a recording must satisfy before it can gate anything.
func (r Recording) Validate() error {
	if len(r.Cases) == 0 {
		return ErrEmptyRecording
	}
	seen := make(map[string]struct{}, len(r.Cases))
	for i, c := range r.Cases {
		if c.Name == "" {
			return fmt.Errorf("%w: case %d has empty name", ErrInvalidCase, i)
		}
		if _, dup := seen[c.Name]; dup {
			return fmt.Errorf("%w: duplicate case name %q", ErrInvalidCase, c.Name)
		}
		seen[c.Name] = struct{}{}
	}
	return nil
}
