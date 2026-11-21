package domain

import (
	"strings"
	"testing"
)

const sampleTop = `File: domain.test
Type: cpu
Showing nodes accounting for 2.73s, 99.64% of 2.74s total
      flat  flat%   sum%        cum   cum%
     0.01s  0.36%  0.36%      2.72s 99.27%  example.com/x.BenchmarkProbe
     0.02s  0.73%  0.73%      0.02s  0.73%  runtime.futex
     2.70s 98.54% 98.91%      2.71s 98.91%  example.com/x.burn
     0.01s  0.36% 99.27%      0.01s  0.36%  example.com/x.small
`

func TestParseTopRanksByCumulativeAndDropsRuntime(t *testing.T) {
	got, err := ParseTop(strings.NewReader(sampleTop))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range got {
		if strings.HasPrefix(h.Name, "runtime.") {
			t.Fatalf("runtime frame must be dropped: %+v", h)
		}
	}
	if len(got) != 2 {
		t.Fatalf("want 2 portable frames (burn, small), got %d: %+v", len(got), got)
	}
	if got[0].Name != "example.com/x.burn" || got[0].FlatPct != 98.54 {
		t.Fatalf("burn should be first with 98.54%% flat: %+v", got[0])
	}
}

func TestParseTopDropsBenchmarkDriver(t *testing.T) {
	got, err := ParseTop(strings.NewReader(sampleTop))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range got {
		if strings.Contains(h.Name, ".Benchmark") {
			t.Fatalf("benchmark driver is not portable code, must be dropped: %+v", h)
		}
	}
}

func TestParseTopRejectsNonPprofInput(t *testing.T) {
	if _, err := ParseTop(strings.NewReader("hello world")); err == nil {
		t.Fatal("non-pprof input must be an error")
	}
}
