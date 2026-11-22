//go:build windows

package wiregen

import "os/exec"

// ownProcessGroup is the Unix half of the cleanup story; on Windows the child stays in the caller's
// group and stop() kills the direct child. WaitDelay still bounds the wait when a grandchild holds
// the output pipes.
func ownProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills the direct child on Windows; sweeping the whole tree needs taskkill, which
// is not wired here yet (issue #18 tracks it).
func killProcessGroup(pid int) {}
