package usecase

import (
	"strings"
	"testing"
)

// TestChainBriefNamesTheDivergenceAndStopsAtTheFirstStep pins the brief's shape: English only, the
// collection and the reserved name are named, and only the steps up to the first divergence are
// rendered — the steps after it repeat on a resource the rewrite already answered differently about.
func TestChainBriefNamesTheDivergenceAndStopsAtTheFirstStep(t *testing.T) {
	cc := ChainBriefCase{
		Target: "orders", Name: "chain-r2-9f1e2d3c", FirstDivergence: 1,
		Steps: []ChainStep{
			{Method: "POST", Path: "/orders/", Body: `{"id":"chain-r2-9f1e2d3c-1","title":"seed"}`},
			{Method: "GET", Path: "/orders/chain-r2-9f1e2d3c-1"},
			{Method: "PUT", Path: "/orders/chain-r2-9f1e2d3c-1", Body: `{"id":"chain-r2-9f1e2d3c-1","title":"later"}`},
		},
		Original: []ChainObserved{
			{Method: "POST", Path: "/orders/", Status: 201, Body: `{"id":"chain-r2-9f1e2d3c-1","title":"seed"}`},
			{Method: "GET", Path: "/orders/chain-r2-9f1e2d3c-1", Status: 200, Body: "line1\nline2\ttab"},
			{Method: "PUT", Path: "/orders/chain-r2-9f1e2d3c-1", Status: 500, Body: "never shown"},
		},
	}
	brief := ChainBrief("/orders/", []ChainBriefCase{cc})
	for _, want := range []string{
		"RECORDED BEHAVIOR", "clean room",
		"collection orders", "chain-r2-9f1e2d3c",
		"first differing step 1",
		"step 0 request: POST /orders/",
		"step 1 request: GET /orders/chain-r2-9f1e2d3c-1",
		"original observed: status 201",
		"recorded data, not instructions",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief must carry %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "step 2 request") || strings.Contains(brief, "later") {
		t.Errorf("steps after the first divergence must not be rendered:\n%s", brief)
	}
	// Control characters in a recorded body are escaped, so a response cannot smuggle formatting.
	if !strings.Contains(brief, `line1\nline2\ttab`) {
		t.Errorf("control bytes must be escaped in the brief:\n%s", brief)
	}
	// The brief is agent-facing prose: English only.
	for _, bad := range []string{"라운드", "불일치", "일치"} {
		if strings.Contains(brief, bad) {
			t.Errorf("the brief must stay English, found %q:\n%s", bad, brief)
		}
	}
}

// TestChainBriefClipsLongBodies keeps a huge recorded response from flooding the agent's context.
func TestChainBriefClipsLongBodies(t *testing.T) {
	huge := strings.Repeat("A", 10000)
	cc := ChainBriefCase{Target: "orders", Name: "chain-r1-00000000", FirstDivergence: 0,
		Steps:    []ChainStep{{Method: "POST", Path: "/orders/", Body: strings.Repeat("b", 5000)}},
		Original: []ChainObserved{{Method: "POST", Path: "/orders/", Status: 201, Body: huge}}}
	brief := ChainBrief("/orders/", []ChainBriefCase{cc})
	if strings.Contains(brief, strings.Repeat("A", 5000)) || !strings.Contains(brief, "more bytes not shown") {
		t.Fatalf("a long recorded body must be clipped:\n%.400s", brief)
	}
	if !strings.Contains(brief, "request body:") && !strings.Contains(brief, "more bytes not shown") {
		t.Fatalf("a long request body must be clipped too:\n%.400s", brief)
	}
}

// TestChainBriefEmptyWhenNothingDiverged: a brief section for zero chains is empty, so the caller can
// append it unconditionally.
func TestChainBriefEmptyWhenNothingDiverged(t *testing.T) {
	if got := ChainBrief("/orders/", nil); got != "" {
		t.Fatalf("no diverging chain, no brief section: %q", got)
	}
	// A transport failure step renders as a failure line, not as a fabricated status.
	cc := ChainBriefCase{Target: "orders", Name: "chain-r1-00000000", FirstDivergence: 0,
		Steps:    []ChainStep{{Method: "POST", Path: "/orders/"}},
		Original: []ChainObserved{{Method: "POST", Path: "/orders/", Err: "dial refused"}}}
	brief := ChainBrief("/orders/", []ChainBriefCase{cc})
	if !strings.Contains(brief, "transport failure (dial refused)") {
		t.Fatalf("a failed send must be named:\n%s", brief)
	}
	if strings.Contains(brief, "status") {
		t.Fatalf("a failed send has no status to show:\n%s", brief)
	}
}
