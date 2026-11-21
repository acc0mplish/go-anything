package usecase

import (
	"fmt"
	"sort"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// BenchResult compares the original and the rewrite on the same cases.
type BenchResult struct {
	Cases        int
	Runs         int
	OriginalMed  time.Duration
	RewriteMed   time.Duration
	Speedup      float64 // original / rewrite; > 1 means the rewrite is faster
	OutputsMatch bool    // timing is only meaningful when this is true
}

// Bench times each recorded case on the original and the rewrite (median of runs).
// If any output differs, the timing is reported but OutputsMatch is false: a fast wrong answer
// is not a result.
func Bench(rec domain.Recording, original, rewrite Subject, runs int) (BenchResult, error) {
	if err := rec.Validate(); err != nil {
		return BenchResult{}, fmt.Errorf("bench: %w", err)
	}
	if runs < 1 {
		return BenchResult{}, fmt.Errorf("bench: runs must be >= 1")
	}
	var origTotal, rewTotal []time.Duration
	match := true
	for _, c := range rec.Cases {
		if c.Error != "" {
			continue // error cases have no output to compare; they are verified by Verify
		}
		for r := 0; r < runs; r++ {
			t0 := time.Now()
			oOut, oErr := original.Run(c.Input)
			origTotal = append(origTotal, time.Since(t0))
			t1 := time.Now()
			rOut, rErr := rewrite.Run(c.Input)
			rewTotal = append(rewTotal, time.Since(t1))
			if oErr != nil || rErr != nil || domain.Normalize(oOut) != domain.Normalize(rOut) {
				match = false
			}
		}
	}
	res := BenchResult{Cases: len(rec.Cases), Runs: runs, OriginalMed: median(origTotal), RewriteMed: median(rewTotal), OutputsMatch: match}
	if res.RewriteMed > 0 {
		res.Speedup = float64(res.OriginalMed) / float64(res.RewriteMed)
	}
	return res, nil
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}
