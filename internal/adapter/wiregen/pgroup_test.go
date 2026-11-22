//go:build !windows

package wiregen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestKillProcessGroupTakesGrandchildrenWithIt pins the cleanup contract (issue #18): a program that
// spawned a background child dies as a group, so the grandchild cannot outlive stop() and hold the
// port for the next run. The shell writes the grandchild's pid before it settles into a long sleep.
func TestKillProcessGroupTakesGrandchildrenWithIt(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and kills real processes")
	}
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "gpid")
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $! > "+gpidFile+"; sleep 300")
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	var gpid string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(gpidFile); err == nil && len(b) > 0 {
			gpid = string(b[:len(b)-1])
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gpid == "" {
		t.Fatal("the grandchild never announced itself")
	}
	killProcessGroup(cmd.Process.Pid)
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("the direct child must die with the group")
	}
	if _, err := os.Stat("/proc/" + gpid); !os.IsNotExist(err) {
		t.Fatalf("the grandchild %s must die with the group; it survived (stat err=%v)", gpid, err)
	}
}
