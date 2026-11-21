package harness

import (
	"testing"

	"github.com/go-anything/go-anything/internal/usecase"
)

func TestGate(t *testing.T) {
	if err := Gate(usecase.Verdict{Total: 2, Matched: 2}); err != nil {
		t.Fatalf("clean verdict should pass: %v", err)
	}
	if err := Gate(usecase.Verdict{Total: 2, Matched: 1}); err == nil {
		t.Fatal("partial match should fail the gate")
	}
	if err := Gate(usecase.Verdict{}); err == nil {
		t.Fatal("empty verdict must not pass")
	}
}
