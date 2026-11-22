package domain

import "strings"

// probeBoundaries are the values programs most often get wrong for an id-like argument.
var probeBoundaries = []string{"", "0", "1", "2", "50", "99", "100", "101", "499", "500",
	"-1", "+5", "007", " 9 ", "1x", "3 extra", "9999999999999999999999", "-0", "1e3", "NaN",
	"٣", "१२", "²", "³", "½"}

// ProbeValues returns up to n deterministic values to try: the boundary values first, then values
// generated from seed. Values never hold newlines or NUL, so they can also be command-line arguments.
//
// When n is small enough that the boundary list could fill every slot (n <= len(probeBoundaries)),
// the boundary pass is capped at max(1, n/2) so the seeded values get the remaining slots and the
// seed still changes the output. The kept boundaries are evenly spaced over the whole list, not its
// head: the special forms late in the list (a signed value, a huge number, a non-ASCII digit) are
// the probes most likely to split the original from the rewrite, and cutting the list at its front
// would drop exactly those.
func ProbeValues(seed uint64, n int) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if len(out) < n && !seen[v] && !strings.ContainsAny(v, "\n\r\x00") && len(v) <= 40 {
			seen[v] = true
			out = append(out, v)
		}
	}
	boundaryCap := n
	if n <= len(probeBoundaries) {
		boundaryCap = max(1, n/2)
	}
	if boundaryCap >= len(probeBoundaries) {
		for _, v := range probeBoundaries {
			add(v)
		}
	} else {
		for i := 0; i < boundaryCap; i++ {
			add(probeBoundaries[i*len(probeBoundaries)/boundaryCap])
		}
	}
	for _, v := range FuzzInputs(seed, n*3) {
		add(v)
	}
	return out
}

// ProbeBoundary reports whether a value is one of the fixed boundary values, as opposed to one generated
// from a seed. Check records use it to say how much of a round came from each kind.
func ProbeBoundary(v string) bool {
	for _, b := range probeBoundaries {
		if v == b {
			return true
		}
	}
	return false
}
