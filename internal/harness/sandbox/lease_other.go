//go:build !windows

package sandbox

import (
	"os"
	"syscall"
)

// The POSIX lease is an flock(2) exclusive non-blocking lock on the
// lease file. The kernel releases the lock when the holder exits or the
// fd closes — the crash-recovery contract needs no sweep.

func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
