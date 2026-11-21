package usecase

import (
	"fmt"
	"sort"

	"github.com/go-anything/go-anything/internal/domain"
)

// unstableFields lists the answer fields that differ between two runs of the same case.
// The names match the ones compareApp uses, so a field recorded as unstable is skipped on replay.
func unstableFields(a, b domain.AppCase) []string {
	var out []string
	diff := func(name, x, y string) {
		if x != y {
			out = append(out, name)
		}
	}
	switch a.Kind {
	case "http":
		diff("status", fmt.Sprint(a.Status), fmt.Sprint(b.Status))
		diff("body", a.ResponseBody, b.ResponseBody)
		for _, h := range unionKeys(a.Headers, b.Headers) {
			diff("header "+h, a.Headers[h], b.Headers[h])
		}
	case "cli":
		diff("exit_code", fmt.Sprint(a.ExitCode), fmt.Sprint(b.ExitCode))
		diff("stdout", domain.Normalize(a.Stdout), domain.Normalize(b.Stdout))
		diff("stderr", domain.Normalize(a.Stderr), domain.Normalize(b.Stderr))
	}
	if len(compareEffects("", a.Effects, b.Effects)) > 0 {
		out = append(out, "effects")
	}
	return out
}

// unstableAcross collects every field that differed between the first run and any later run.
func unstableAcross(runs []domain.AppCase) []string {
	set := map[string]bool{}
	for _, r := range runs[1:] {
		for _, f := range unstableFields(runs[0], r) {
			set[f] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// unionKeys returns the sorted keys present in either map.
func unionKeys(a, b map[string]string) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
