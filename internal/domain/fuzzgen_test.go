package domain

import (
	"strings"
	"testing"
)

func TestFuzzInputsAreReproducibleFromSeed(t *testing.T) {
	a := FuzzInputs(42, 50)
	b := FuzzInputs(42, 50)
	c := FuzzInputs(43, 50)
	if len(a) != 50 {
		t.Fatalf("want 50 inputs, got %d", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("the same seed must give the same inputs")
		}
	}
	same := 0
	for i := range a {
		if a[i] == c[i] {
			same++
		}
	}
	if same == len(a) {
		t.Fatal("a different seed must give different inputs")
	}
}

func TestFuzzInputsAreSingleLines(t *testing.T) {
	for _, in := range FuzzInputs(7, 200) {
		if strings.ContainsAny(in, "\n\r") {
			t.Fatalf("an input must be one line: %q", in)
		}
	}
}

func TestFuzzInputsCoverLongAndUnicode(t *testing.T) {
	long, unicode := false, false
	for _, in := range FuzzInputs(1, 400) {
		if len(in) > 1000 {
			long = true
		}
		if strings.ContainsAny(in, "é日😀") {
			unicode = true
		}
	}
	if !long || !unicode {
		t.Fatalf("400 inputs should include long and non-ASCII cases (long=%v unicode=%v)", long, unicode)
	}
}
