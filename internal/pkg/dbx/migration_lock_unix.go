//go:build unix

package dbx

import (
	"errors"
	"os"
	"syscall"
)

// lockFile tries to take an exclusive non-blocking flock. A held lock is
// reported as (false, nil) so the caller can retry until its deadline.
func lockFile(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
