package domain

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// HotFunc is a function the CPU profile charged time to, with its share of the run.
type HotFunc struct {
	Name    string  `json:"name"`
	FlatPct float64 `json:"flat_pct"` // time spent in the function itself
	CumPct  float64 `json:"cum_pct"`  // time spent in it and everything it calls
}

// ParseTop reads the text output of `go tool pprof -top` and returns the functions sorted by
// cumulative share, hottest first. Runtime and testing frames are dropped: they are not code
// a rewrite could replace.
func ParseTop(r io.Reader) ([]HotFunc, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inTable := false
	var out []HotFunc
	for sc.Scan() {
		line := sc.Text()
		if !inTable {
			// The table header names the columns; everything before it is preamble.
			if strings.Contains(line, "flat%") && strings.Contains(line, "cum%") {
				inTable = true
			}
			continue
		}
		fields := strings.Fields(line)
		// flat flat% sum% cum cum% name
		if len(fields) < 6 {
			continue
		}
		flat, err1 := parsePct(fields[1])
		cum, err2 := parsePct(fields[4])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("profile: unreadable row %q", line)
		}
		name := strings.Join(fields[5:], " ")
		if isRuntimeFrame(name) || isHarnessFrame(name) {
			continue
		}
		out = append(out, HotFunc{Name: name, FlatPct: flat, CumPct: cum})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("profile: read: %w", err)
	}
	if !inTable {
		return nil, fmt.Errorf("profile: no pprof table found; pass `go tool pprof -top` output")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CumPct > out[j].CumPct })
	return out, nil
}

func parsePct(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
}

// isRuntimeFrame reports frames that belong to the Go runtime or the test harness.
func isRuntimeFrame(name string) bool {
	for _, p := range []string{"runtime.", "testing.", "internal/runtime"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// isHarnessFrame reports the benchmark driver itself, which is never code to port.
func isHarnessFrame(name string) bool {
	return strings.Contains(name, ".Benchmark")
}
