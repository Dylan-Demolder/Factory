//go:build !unix

package config

// WithLock runs fn without a cross-process lock on platforms that have no
// flock. Atomic file replacement still keeps a single writer's output intact.
func WithLock(dir string, fn func() error) error {
	return fn()
}
