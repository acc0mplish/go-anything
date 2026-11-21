package usecase

import "github.com/go-anything/go-anything/internal/domain"

// ShadowResult is the outcome of running the rewrite alongside the original.
type ShadowResult struct {
	// Output is always the original's answer: production behavior never changes during a shadow.
	Output      string
	OriginalErr error
	Mismatches  []string // input names where the rewrite disagreed
}

// Shadow runs both implementations on each input, returns the original's result, and records
// where the rewrite disagrees. The rewrite's failures never reach the caller.
func Shadow(rec domain.Recording, original, rewrite Subject) (map[string]ShadowResult, error) {
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	out := make(map[string]ShadowResult, len(rec.Cases))
	for _, c := range rec.Cases {
		oOut, oErr := original.Run(c.Input)
		rOut, rErr := rewrite.Run(c.Input)
		res := ShadowResult{Output: oOut, OriginalErr: oErr}
		sameErr := (oErr == nil) == (rErr == nil)
		if !sameErr || domain.Normalize(oOut) != domain.Normalize(rOut) {
			res.Mismatches = append(res.Mismatches, c.Name)
		}
		out[c.Name] = res
	}
	return out, nil
}
