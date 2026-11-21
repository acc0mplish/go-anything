package usecase

import (
	"sort"

	"github.com/go-anything/go-anything/internal/domain"
)

// Unit is one package found by analysis, with the measurements that rank it for porting.
type Unit struct {
	ImportPath string
	Dir        string
	Files      int
	Lines      int
	Exported   int
	// Recorded is true when some recording covers this package.
	Recorded bool
}

// UnportedUnits lists packages that no recording covers yet, most valuable first.
//
// Ranking: larger units first (more to gain or lose), then units with fewer exported
// functions (a narrow API is easier to verify with recorded cases). Test-only and
// generated-looking files are already excluded by the analyzer.
func UnportedUnits(units []Unit) []Unit {
	var out []Unit
	for _, u := range units {
		if !u.Recorded {
			out = append(out, u)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		if out[i].Exported != out[j].Exported {
			return out[i].Exported < out[j].Exported
		}
		return out[i].ImportPath < out[j].ImportPath
	})
	return out
}

// MarkRecorded sets Recorded on each unit whose import path matches a recording's subject.
// A recording's subject is the package import path it was recorded for; recordings made
// from a command line (no import path) cover nothing here and are ignored.
func MarkRecorded(units []Unit, recorded []domain.Recording) []Unit {
	covered := map[string]bool{}
	for _, r := range recorded {
		if r.Subject != "" {
			covered[r.Subject] = true
		}
	}
	out := make([]Unit, len(units))
	copy(out, units)
	for i := range out {
		out[i].Recorded = covered[out[i].ImportPath]
	}
	return out
}
