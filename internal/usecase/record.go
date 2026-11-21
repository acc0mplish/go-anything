package usecase

import (
	"fmt"

	"github.com/go-anything/go-anything/internal/domain"
)

// Record runs the original on every input and captures what it returns or fails with.
// Inputs that the original cannot process are recorded as error cases, not dropped.
func Record(subject string, inputs map[string]string, original Subject) (domain.Recording, error) {
	if len(inputs) == 0 {
		return domain.Recording{}, fmt.Errorf("record: %w", domain.ErrEmptyRecording)
	}
	rec := domain.Recording{Subject: subject}
	// Sorted names keep the output deterministic across runs.
	for _, name := range sortedKeys(inputs) {
		in := inputs[name]
		out, err := original.Run(in)
		c := domain.Case{Name: name, Input: in, Output: out}
		if err != nil {
			c.Error = err.Error()
			c.Output = ""
		}
		rec.Cases = append(rec.Cases, c)
	}
	return rec, rec.Validate()
}
