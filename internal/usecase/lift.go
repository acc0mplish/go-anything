package usecase

import "fmt"

// LiftUnit is the outcome of verifying one unit against its recording.
type LiftUnit struct {
	Name     string
	Passed   bool // the recording still matches the rewrite and the source has not drifted
	Stale    bool // the source changed since the recording was made
	Mismatch int  // cases that do not match
}

// LiftInput is everything the lift decision looks at.
type LiftInput struct {
	ReplayTotal   int
	ReplayMatched int
	Units         []LiftUnit
	GatePassed    bool
}

// LiftBlockers lists every reason the program cannot be lifted yet. An empty result means ready.
// Every blocker is reported, not just the first, so one run shows all the work left.
func LiftBlockers(in LiftInput) []string {
	var out []string
	if in.ReplayTotal == 0 {
		out = append(out, "replay: no recorded app cases to replay")
	} else if in.ReplayMatched != in.ReplayTotal {
		out = append(out, fmt.Sprintf("replay: %d of %d recorded cases do not match", in.ReplayTotal-in.ReplayMatched, in.ReplayTotal))
	}
	for _, u := range in.Units {
		switch {
		case u.Stale:
			out = append(out, fmt.Sprintf("unit %s: its source changed since the recording; record it again", u.Name))
		case !u.Passed:
			out = append(out, fmt.Sprintf("unit %s: recorded behavior does not match (%d case(s))", u.Name, u.Mismatch))
		}
	}
	if !in.GatePassed {
		out = append(out, "gate: the completion gate did not pass")
	}
	return out
}
