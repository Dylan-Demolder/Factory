//go:build unix

// Package proc wraps the small amount of OS-specific process handling
// factory needs: killing agent process trees, detaching runs, and pid checks.
package proc

import (
	"os/exec"
	"syscall"
)

// Group makes cmd lead its own process group and, when its context is
// cancelled, kills the whole group so agent sub-processes don't leak.
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Detach starts cmd in a new session so it survives the terminal closing.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Alive reports whether a process with this pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Terminate asks a process to stop gracefully.
func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
