package usecase

import (
	"strings"
	"testing"
)

func TestAppReportNamesEveryCaseIncludingMatched(t *testing.T) {
	v := AppVerdict{Subject: "svc", Total: 2, Matched: 1, Names: []string{"order", "refund"},
		Mismatches: []AppMismatch{{Case: "order", What: "effect 1", Want: "POST /hook a", Got: "POST /hook b"}}}
	r := AppReport(v, "app-http")
	names := map[string]bool{}
	for _, it := range r.Items {
		names[it.Name] = true
	}
	if !names["order"] || !names["refund"] {
		t.Fatalf("both cases must be named, not positions: %+v", r.Items)
	}
}

func TestAppReportShowsEffectDifferenceInTable(t *testing.T) {
	v := AppVerdict{Subject: "svc", Total: 1, Matched: 0, Names: []string{"order"},
		Mismatches: []AppMismatch{{Case: "order", What: "effect 1", Want: `POST /hook {"tag":"v1"}`, Got: `POST /hook {"tag":"v2"}`}}}
	r := AppReport(v, "app-http")
	md := r.Markdown()
	if !strings.Contains(md, "effect 1") || !strings.Contains(md, `"tag":"v2"`) {
		t.Fatalf("the effect difference must appear in the report:\n%s", md)
	}
	if r.Passed {
		t.Fatal("a changed effect must fail the report")
	}
}

func TestAppReportMatchedCaseSaysEffectsWereChecked(t *testing.T) {
	v := AppVerdict{Subject: "svc", Total: 1, Matched: 1, Names: []string{"order"}}
	r := AppReport(v, "app-http")
	if len(r.Items) != 1 || r.Items[0].Detail != "answer and effects match" {
		t.Fatalf("a matched case must state that effects were compared: %+v", r.Items)
	}
}
