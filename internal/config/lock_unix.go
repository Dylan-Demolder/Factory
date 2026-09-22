//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockName is shared with the factory-agents helper so a save from the web
// editor and a save from the CLI cannot interleave.
const lockName = ".factory-agents.lock"

// WithLock runs fn while holding an exclusive cross-process lock on dir.
// An empty dir runs fn unlocked.
func WithLock(dir string, fn func() error) error {
	if dir == "" {
		return fn()
	}
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fn()
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
