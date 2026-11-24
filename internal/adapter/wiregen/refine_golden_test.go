package wiregen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRefineNoFlagOutputIsStable is the preservation evidence for the new options: a refine run that
// gives none of them must produce exactly the stdout and the round-NN.json bytes it produced before the
// options existed. The golden files under testdata/golden were fixed from the pre-change code; a failure
// here means a change broke a no-option run and must not merge. Temp paths (the module root, the scratch
// directory holding the recording) differ on every run, so they are replaced by fixed markers before the
// byte comparison.
func TestRefineNoFlagOutputIsStable(t *testing.T) {
	root, orig, casesPath := rsiSetup(t, rsiRewrite(false, false))
	var log bytes.Buffer
	ok, err := Refine(RefineOptions{Root: root, Unit: "orders", CasesPath: casesPath, OriginalCLI: orig,
		Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &log})
	if err != nil || !ok {
		t.Fatalf("an exact rewrite must converge with no options: ok=%v err=%v\n%s", ok, err, log.String())
	}
	work := filepath.Dir(casesPath)
	scrub := func(s string) string {
		s = strings.ReplaceAll(s, root, "<root>")
		return strings.ReplaceAll(s, work, "<work>")
	}
	outs := map[string]string{"stdout": scrub(log.String())}
	for _, round := range []int{1, 2} {
		p := filepath.Join(root, ".go-anything", "checks", "orders", fmt.Sprintf("round-%02d.json", round))
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("round %d record must exist after a converging run: %v", round, err)
		}
		outs[fmt.Sprintf("round-%02d.json", round)] = scrub(string(raw))
	}
	for name, got := range outs {
		golden := filepath.Join("testdata", "golden", name+".golden")
		want, err := os.ReadFile(golden)
		if os.IsNotExist(err) {
			// Fresh checkout: fix the golden from this run. The files shipped in the repository were
			// generated from the pre-change code, so a golden goes missing only before that.
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			// A regenerated golden proves nothing about this run: fail once so the self-heal cannot
			// slip by, and let the next run (against the new golden) pass or name the drift.
			t.Errorf("golden files were missing and have been regenerated; rerun to compare against the new golden")
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != got {
			t.Errorf("output %s drifted from the no-option golden (a no-option run must not change):\n--- golden\n+++ got\n%s",
				name, firstDiff(string(want), got))
		}
	}
}

// firstDiff shows the first differing line pair, so a failure names the drift instead of dumping bytes.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		w, g := "", ""
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return "line " + strconv.Itoa(i+1) + ":\n- " + w + "\n+ " + g
		}
	}
	return "outputs are equal"
}
