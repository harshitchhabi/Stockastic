//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive, non-blocking lock on f. Windows releases it when the process exits.
//
// Windows locks are mandatory: bytes inside a locked range cannot be read through any other handle. So
// the lock is taken on a single byte far beyond the end of the data (2^62), which no record will ever
// reach, and the log stays readable while it is held.
func lockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: 0, OffsetHigh: 0x40000000}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrLocked
		}
		return err
	}
	return nil
}
