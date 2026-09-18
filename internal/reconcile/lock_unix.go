package reconcile

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock, returning the release.
//
// flock rather than a lock file created with O_EXCL: the kernel drops it when
// the process dies, so a crashed spawn cannot wedge every later sweep. A stale
// lock nobody can clear is worse than the race it was protecting against.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
