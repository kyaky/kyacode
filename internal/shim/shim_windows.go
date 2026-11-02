//go:build windows

package shim

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// assignJob owns the spawned engine in a job object with
// kill-on-job-close: when the shim dies for any reason (extension
// reload, editor exit, a kill the signal path never saw), the OS closes
// the shim's job handle and the engine — with every descendant it
// spawned — dies with it. This is the Windows answer to the orphaned
// app-server class: no sweep, no PID bookkeeping, teardown by handle
// lifetime. The process handle for the assignment is opened by pid (the
// stdlib does not expose the child's raw handle) and closed right after
// the assignment — the job keeps its own registration — so the only
// long-lived handle is the job's, whose close is the kill switch. A
// failure anywhere degrades to no job at all (the signal path's
// ForceTree remains the teardown backstop) with one stderr note, never a
// spawn failure: the job is an ownership guarantee, not a prerequisite.
func assignJob(p *os.Process) func() {
	const processSetQuota = 0x0100
	const processTerminate = 0x0001
	proc, err := windows.OpenProcess(processSetQuota|processTerminate, false, uint32(p.Pid))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kyacode shim: process handle unavailable (teardown falls back to the signal path)")
		return func() {}
	}
	defer windows.CloseHandle(proc)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil || job == 0 {
		fmt.Fprintln(os.Stderr, "kyacode shim: job object unavailable (teardown falls back to the signal path)")
		return func() {}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		fmt.Fprintln(os.Stderr, "kyacode shim: job limits rejected (teardown falls back to the signal path)")
		return func() {}
	}
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		fmt.Fprintln(os.Stderr, "kyacode shim: job assignment failed (teardown falls back to the signal path)")
		return func() {}
	}
	return func() { _ = windows.CloseHandle(job) }
}
