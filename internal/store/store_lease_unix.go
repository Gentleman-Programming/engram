//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

func lockStoreLease(file *os.File, exclusive, nonBlocking bool) error {
	op := syscall.LOCK_SH
	if exclusive {
		op = syscall.LOCK_EX
	}
	if nonBlocking {
		op |= syscall.LOCK_NB
	}
	return syscall.Flock(int(file.Fd()), op)
}

func unlockStoreLease(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func isStoreLeaseBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
