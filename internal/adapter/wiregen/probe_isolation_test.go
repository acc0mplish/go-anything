package wiregen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// fakeHarness is a stand-in for the generated probe binary: it answers each probe it is handed. A
// probe naming "boom" makes it exit the child the way a unit that calls os.Exit would, and "hang"
// makes it sleep far past any timeout. One child is started per probe, so the exits and hangs below
// can only be survived if the isolation works.
const fakeHarness = `#!/usr/bin/env python3
import json, sys, time
probes = json.load(sys.stdin)
out = []
for p in probes:
    a = p.get("argv") or [""]
    if "boom" in a:
        sys.stderr.write("unit exited the child\n")
        sys.exit(7)
    if "hang" in a:
        time.sleep(30)
    out.append({"exit_code": 0, "stdout": "ok:" + a[-1], "stderr": ""})
json.dump(out, sys.stdout)
`

func writeFakeHarness(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "harness.py")
	if err := os.WriteFile(p, []byte(fakeHarness), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunProbeCLIIolationKeepsAUnitExitInTheChild(t *testing.T) {
	bin := writeFakeHarness(t)
	probes := []domain.AppCase{
		{Kind: "cli", Argv: []string{"orders", "fine"}},
		{Kind: "cli", Argv: []string{"orders", "boom"}},
	}
	got, err := RunProbe(bin, "cli", probes)
	if err != nil {
		t.Fatalf("a unit that exits its child must not fail the probe run: %v", err)
	}
	if got[0].Stdout != "ok:fine" || got[0].ExitCode != 0 {
		t.Fatalf("the normal probe must still be answered: %+v", got[0])
	}
	if got[1].ExitCode != 7 || !strings.Contains(got[1].Stderr, "unit exited the child") {
		t.Fatalf("the exit must be recorded as that probe's answer: %+v", got[1])
	}
}

func TestRunProbeCLITimeoutRecordsExit124(t *testing.T) {
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	defer func() { probeTimeout = old }()
	bin := writeFakeHarness(t)
	got, err := RunProbe(bin, "cli", []domain.AppCase{{Kind: "cli", Argv: []string{"orders", "hang"}}})
	if err != nil {
		t.Fatalf("a hanging unit must time out, not stall the run: %v", err)
	}
	if got[0].ExitCode != 124 || !strings.Contains(got[0].Stderr, "timed out") {
		t.Fatalf("a hung probe must carry exit 124 and say so: %+v", got[0])
	}
}
