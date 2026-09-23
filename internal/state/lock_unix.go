//go:build unix

package state

import (
	"errors"
	"os"
	"syscall"
)

// Lock takes an advisory lock on path (created 0600 when absent) without waiting: shared, or
// exclusive. ErrLocked when another process holds a conflicting lock. The lock lives as long as
// the returned file is open; the kernel drops it when the process exits, so a crash never leaves
// a stale lock behind.
func Lock(path string, exclusive bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
