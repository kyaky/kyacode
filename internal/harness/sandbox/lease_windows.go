//go:build windows

package sandbox

import (
	"os"
	"syscall"
	"unsafe"
)

// The Windows lease is a byte-range lock via LockFileEx: exclusive and
// fail-immediately, so a second holder refuses instead of queuing. The
// OS releases the lock when the holder's handle dies, which is the whole
// crash-recovery contract — no sweep, no PID bookkeeping.

const (
	lockfileExclusiveLock  = 0x2
	lockfileFailImmediately = 0x1
)

var (
	modkernel32    = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

func lockExclusive(f *os.File) error {
	ol := new(syscall.Overlapped)
	r1, _, callErr := procLockFileEx.Call(f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, 1, 0, uintptr(unsafe.Pointer(ol)))
	if r1 == 0 {
		return callErr
	}
	return nil
}

func unlockExclusive(f *os.File) error {
	ol := new(syscall.Overlapped)
	r1, _, callErr := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(ol)))
	if r1 == 0 {
		return callErr
	}
	return nil
}
