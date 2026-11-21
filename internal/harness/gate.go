// Package harness wires the use cases into the record -> verify -> gate loop.
package harness

import (
	"fmt"

	"github.com/go-anything/go-anything/internal/usecase"
)

// Gate returns nil only when the verdict passed. Anything else fails the build.
func Gate(v usecase.Verdict) error {
	if !v.Passed() {
		return fmt.Errorf("gate: %d of %d cases did not match (subject %q)", v.Total-v.Matched, v.Total, v.Subject)
	}
	return nil
}
