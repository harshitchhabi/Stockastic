//go:build !windows

package store

import "golang.org/x/sys/unix"

// FreeBytes is the disk space still available to this process on the volume holding dir.
func FreeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
