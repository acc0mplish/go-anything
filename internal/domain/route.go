package domain

import (
	"strconv"
	"strings"
)

// Route decides, for one HTTP request, whether the rewrite answers it or the original does.
// Rules are checked in order; the first match wins. No match means the original answers, so an
// unlisted request is never silently handled by unverified code.
type Route struct {
	// Unit names the unit this route serves, so `wire` can switch a whole unit at once.
	Unit       string `json:"unit,omitempty"`
	Method     string `json:"method"` // "" or "*" matches any method
	PathPrefix string `json:"path_prefix"`
	Target     string `json:"target"` // "rewrite" or "original"
}

// Target values.
const (
	TargetRewrite  = "rewrite"
	TargetOriginal = "original"
)

// Decide returns the target for a method and path. It is pure: no I/O, no state.
func Decide(rules []Route, method, path string) string {
	for _, r := range rules {
		if r.Method != "" && r.Method != "*" && !strings.EqualFold(r.Method, method) {
			continue
		}
		if strings.HasPrefix(path, r.PathPrefix) {
			return r.Target
		}
	}
	return TargetOriginal
}

// ValidRoutes rejects rules that would send traffic somewhere undefined.
func ValidRoutes(rules []Route) error {
	for i, r := range rules {
		if r.Target != TargetRewrite && r.Target != TargetOriginal {
			return &RouteError{Index: i, Reason: "target must be rewrite or original, got " + r.Target}
		}
		if !strings.HasPrefix(r.PathPrefix, "/") {
			return &RouteError{Index: i, Reason: "path_prefix must start with /"}
		}
	}
	return nil
}

// RouteError points at the rule that is invalid.
type RouteError struct {
	Index  int
	Reason string
}

func (e *RouteError) Error() string {
	return "route rule " + strconv.Itoa(e.Index) + ": " + e.Reason
}
