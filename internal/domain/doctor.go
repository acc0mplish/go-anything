package domain

import (
	"strings"
	"unicode"
)

// Level is the outcome of one health check.
type Level string

const (
	LevelOK   Level = "ok"   // the check passed
	LevelWarn Level = "warn" // works, but something is missing or suboptimal
	LevelFail Level = "fail" // a required piece is broken
)

// Check is one health check and its result.
type Check struct {
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	// Fix tells the user what to do when the level is not ok. Empty when ok.
	Fix string `json:"fix,omitempty"`
}

// DoctorReport is the full set of checks.
type DoctorReport struct {
	Checks []Check `json:"checks"`
}

// Healthy reports whether nothing failed. Warnings do not make the setup unhealthy.
func (r DoctorReport) Healthy() bool {
	for _, c := range r.Checks {
		if c.Level == LevelFail {
			return false
		}
	}
	return true
}

// Counts returns how many checks ended at each level.
func (r DoctorReport) Counts() map[Level]int {
	out := map[Level]int{}
	for _, c := range r.Checks {
		out[c.Level]++
	}
	return out
}

// GoVersionAtLeast reports whether an installed Go version such as "go1.27.4" satisfies a
// required version such as "1.27.4". Comparison is numeric per component, so 1.10 > 1.9.
func GoVersionAtLeast(installed, required string) bool {
	have := parseVersion(installed)
	want := parseVersion(required)
	if have == nil || want == nil {
		return false
	}
	for i := 0; i < len(want); i++ {
		h := 0
		if i < len(have) {
			h = have[i]
		}
		if h != want[i] {
			return h > want[i]
		}
	}
	return true
}

// parseVersion extracts the numeric components from "go1.27.4" or "1.27.4". It returns nil when
// the text has no numeric version in it.
func parseVersion(s string) []int {
	// Start at the first digit: "go" and any other prefix are skipped.
	first := strings.IndexFunc(s, unicode.IsDigit)
	if first < 0 {
		return nil
	}
	var parts []int
	n, seen := 0, false
	for _, r := range s[first:] {
		switch {
		case unicode.IsDigit(r):
			n = n*10 + int(r-'0')
			seen = true
		case r == '.':
			if !seen {
				return nil
			}
			parts = append(parts, n)
			n, seen = 0, false
		default:
			// Anything after the numbers (for example "linux/amd64") ends the version.
			if seen {
				parts = append(parts, n)
			}
			return parts
		}
	}
	if seen {
		parts = append(parts, n)
	}
	return parts
}
