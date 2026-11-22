//go:build !windows

package wiregen

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts the child in its own process group, so cleanup can signal the group and take
// grandchildren with it. Unix only: Windows builds a different mechanism in pgroup_windows.go.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the whole group the child leads, including anything it spawned that
// inherited the group (issue #18).
func killProcessGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGKILL)
}
