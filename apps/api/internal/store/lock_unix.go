//go:build !windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, non-blocking lock on f. The operating system drops it automatically when
// the process exits, however it exits, so a crash never leaves a stale lock behind.
func lockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return ErrLocked
		}
		return err
	}
	return nil
}
