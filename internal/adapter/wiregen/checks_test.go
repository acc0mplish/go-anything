package wiregen

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestRoundCheckSplitsBoundaryAndGeneratedValues(t *testing.T) {
	values := []string{"", "0", "100", "abc", "xyz"}
	c := newRoundCheck(2, 9, values, 10, 0, nil, 1, true)
	if c.BoundaryValues != 3 || c.GeneratedValues != 2 {
		t.Fatalf("boundary and generated counts wrong: %+v", c)
	}
	if len(c.NotChecked) == 0 || !strings.Contains(strings.Join(c.NotChecked, " "), "outside the probe classes") {
		t.Fatalf("a round must say what it did not check: %+v", c.NotChecked)
	}
	if !strings.Contains(c.Confidence, "not that every input does") {
		t.Fatalf("a clean round must not claim every input agreed: %q", c.Confidence)
	}
}

func TestRoundCheckRecordsCoveredAndMissingClasses(t *testing.T) {
	c := newRoundCheck(1, 1, []string{"0", "1", "x"}, 3, 0, nil, 0, true)
	if c.ValueClasses["ascii-digits"] != 2 || c.ValueClasses["non-numeric"] != 1 {
		t.Fatalf("per-class counts wrong: %v", c.ValueClasses)
	}
	if len(c.ValueClasses) != len(domain.ProbeClasses()) {
		t.Fatalf("every class must have a key, zero or not: %v", c.ValueClasses)
	}
	wantMissing := []string{"empty", "padded", "signed", "overflow", "leading-zero", "unicode-digit", "numeric-symbol"}
	if strings.Join(c.MissingClasses, ",") != strings.Join(wantMissing, ",") {
		t.Fatalf("missing classes wrong: %v", c.MissingClasses)
	}
}

func TestRoundCheckCoveringEveryClassHasNoMissing(t *testing.T) {
	values := []string{"", " 1", "-1", "9999999999999999999999", "007", "0", "\u0663", "\u00b2", "x"}
	c := newRoundCheck(1, 1, values, 9, 0, nil, 0, true)
	if c.MissingClasses == nil || len(c.MissingClasses) != 0 {
		t.Fatalf("no class is missing, want an empty list: %#v", c.MissingClasses)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"missing_classes":[]`) || !strings.Contains(string(data), `"value_classes":{`) {
		t.Fatalf("class fields must serialize under their JSON names as [] and {}: %s", data)
	}
}

func TestDisagreementChangesConfidenceWording(t *testing.T) {
	c := newRoundCheck(1, 1, []string{"1"}, 2, 1, []string{"probe-round1-0"}, 0, false)
	if !strings.Contains(c.Confidence, "disagreement found") {
		t.Fatalf("a round with disagreement must say so: %q", c.Confidence)
	}
}

func TestWriteRoundCheckSavesUnderProject(t *testing.T) {
	root := t.TempDir()
	c := newRoundCheck(3, 5, []string{"", "7"}, 4, 0, nil, 0, true)
	p, err := WriteRoundCheck(root, "orders", c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "orders/round-03.json") && !strings.Contains(p, "round-03.json") {
		t.Fatalf("round file name wrong: %s", p)
	}
	data, _ := os.ReadFile(p)
	var back RoundCheck
	if err := json.Unmarshal(data, &back); err != nil || back.Round != 3 || back.Seed != 5 {
		t.Fatalf("round record must round-trip: %+v err=%v", back, err)
	}
}
