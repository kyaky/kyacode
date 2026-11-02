//go:build !windows

package shim

import "os"

// POSIX has no job-object equivalent in the stdlib or x/sys; a shim
// killed without a signal leaves its engine orphaned until it exits on
// its own (the lease frees immediately, so nothing deadlocks). The
// signal path's ForceTree remains the teardown backstop. Linux's
// PDEATHSIG would close this gap and is reserved for a follow-up rather
// than risked behind a cgo-free build constraint.
func assignJob(p *os.Process) func() {
	return func() {}
}
