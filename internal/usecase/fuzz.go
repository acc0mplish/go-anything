package usecase

import (
	"github.com/go-anything/go-anything/internal/domain"
)

// FuzzMismatch is one input where the rewrite and the original disagree.
type FuzzMismatch struct {
	Input    string
	Original string
	Rewrite  string
	// OriginalFailed and RewriteFailed record whether each side returned an error.
	OriginalFailed bool
	RewriteFailed  bool
	Detail         string
}

// FuzzReport is the result of a live differential run.
type FuzzReport struct {
	Seed       uint64
	Total      int
	Mismatches []FuzzMismatch
}

// Passed reports whether the rewrite agreed with the original on every fresh input.
func (r FuzzReport) Passed() bool { return r.Total > 0 && len(r.Mismatches) == 0 }

// Fuzz runs fresh inputs through both programs and reports every disagreement. Failure on one side
// only is a mismatch. Failure on both sides counts as agreement: the error text may differ, but
// both refused the input.
func Fuzz(original, rewrite Subject, inputs []string, seed uint64) FuzzReport {
	rep := FuzzReport{Seed: seed, Total: len(inputs)}
	for _, in := range inputs {
		o, oErr := original.Run(in)
		w, wErr := rewrite.Run(in)
		switch {
		case oErr != nil && wErr != nil:
			continue
		case (oErr != nil) != (wErr != nil):
			detail := "rewrite answered, original failed"
			if wErr != nil {
				detail = "rewrite failed, original answered"
			}
			rep.Mismatches = append(rep.Mismatches, FuzzMismatch{Input: in, Original: o, Rewrite: w,
				OriginalFailed: oErr != nil, RewriteFailed: wErr != nil, Detail: detail})
		case domain.Normalize(o) != domain.Normalize(w):
			rep.Mismatches = append(rep.Mismatches, FuzzMismatch{Input: in, Original: o, Rewrite: w,
				Detail: "outputs differ"})
		}
	}
	return rep
}
