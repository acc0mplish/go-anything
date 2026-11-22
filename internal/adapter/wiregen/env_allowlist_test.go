package wiregen

import (
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/usecase"
)

// TestAllowedParentEnvKeepsGaAndDropsSecrets pins the allowlist (issue #20): the agent-generated
// rewrite sees the GA_* switches and the basics it needs to run, but never the parent's tokens.
func TestAllowedParentEnvKeepsGaAndDropsSecrets(t *testing.T) {
	t.Setenv("GA_UNIT_PAYMENTS", "rewrite")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wxyz")
	t.Setenv("GITHUB_TOKEN", "ghp_x")
	t.Setenv("PATH", "/bin")
	var sawGA, sawPath, sawSecret bool
	for _, kv := range allowedParentEnv() {
		switch {
		case strings.HasPrefix(kv, "GA_UNIT_PAYMENTS=rewrite"):
			sawGA = true
		case kv == "PATH=/bin":
			sawPath = true
		case strings.HasPrefix(kv, "AWS_SECRET") || strings.HasPrefix(kv, "GITHUB_TOKEN"):
			sawSecret = true
		}
	}
	if !sawGA || !sawPath {
		t.Fatalf("GA_* switches and PATH must survive: ga=%v path=%v", sawGA, sawPath)
	}
	if sawSecret {
		t.Fatal("a parent secret must not reach the started program")
	}
}

// TestBuildProbeRejectsBadUnitNames pins the single validator: a traversal, a quoted name and the
// reserved registry name are refused before any file is written.
func TestBuildProbeRejectsBadUnitNames(t *testing.T) {
	for _, name := range []string{"../escape", `q"uote`, "units", "-lead", "Has-Caps"} {
		if _, _, err := BuildProbe(t.TempDir(), name, "cli", "example.com/m"); err == nil {
			t.Fatalf("unit name %q must be rejected", name)
		} else if !strings.Contains(err.Error(), "unit name") {
			t.Fatalf("the refusal must name the unit rule: %v", err)
		}
	}
	if err := usecase.ValidUnitName("orders"); err != nil {
		t.Fatalf("a plain name must pass: %v", err)
	}
}
