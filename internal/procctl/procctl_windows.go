//go:build windows

package procctl

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGenerateCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procOpenProcess       = kernel32.NewProc("OpenProcess")
	procCloseHandle       = kernel32.NewProc("CloseHandle")
)

// ctrlBreakEvent is CTRL_BREAK_EVENT: the only console control event that
// can target one process group (CTRL_C_EVENT is console-wide and would
// signal kyacode itself).
const ctrlBreakEvent = 1

// processTerminate is the OpenProcess access right ForceTree implies.
const processTerminate = 0x0001

// errNoSuchProcess is ERROR_INVALID_PARAMETER (87): the OpenProcess errno
// class Windows uses to say a pid does not name any existing process —
// the only "gone" answer pidAlive may give. (The stdlib syscall package
// does not carry this constant; x/sys was ruled out for this file pair.)
const errNoSuchProcess = syscall.Errno(87)

// ConfigureGroup makes cmd the leader of its own console process group,
// which is what lets GracefulInterrupt later address the whole tree by
// pid. Documented trade: members of a new process group no longer receive
// the console's Ctrl-C broadcast — kyacode is the sole distributor (first
// Ctrl-C ends the session via the ladder; the force rung covers children
// that ignore the soft rung). Raw-mode TUIs still see ^C as a keystroke.
func ConfigureGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// GracefulInterrupt delivers the soft rung to pid's whole console process
// group — the .cmd shim AND every descendant it spawned (cmd shims spawn
// node/bun children into the same group). Returns an error when delivery
// fails (e.g. no attached console), so the caller can decide loudness.
func GracefulInterrupt(p *os.Process) error {
	r1, _, callErr := procGenerateCtrlEvent.Call(ctrlBreakEvent, uintptr(p.Pid))
	if r1 == 0 {
		return fmt.Errorf("ctrl-break to pid %d: %v", p.Pid, callErr)
	}
	return nil
}

// Terminate is the TERM rung — and Windows HAS none: Signal(SIGTERM)
// returns EWINDOWS, which main used to suppress with a GOOS branch that
// also printed nothing on windows. Terminate carries that platform fact
// structurally: nil here means "no TERM rung exists on this platform",
// never success at delivering one, and it must stay silent — a fake
// "TERM rung not delivered" diagnostic on every Windows interrupt would
// be a byte-delta on a stable stderr surface. (Pinned in the ladder
// suite's windows-tagged pin.)
func Terminate(p *os.Process) error {
	return nil
}

// pidVerdict is the errno-aware liveness class of a pid probe. The old
// bool collapsed every OpenProcess failure into "gone", which let a
// live-but-protected child read as dead and made ForceTree report
// success without ever invoking taskkill — bookkeeping believed cleanup
// delivered while the tree went on holding the gateway key.
type pidVerdict int

const (
	pidLive pidVerdict = iota
	// pidGone means the OS itself says no such process exists; it is the
	// ONLY "gone" answer (double-cleanup idempotency rides on it).
	pidGone
)

// pidAlive classifies the pid's liveness by the OpenProcess errno class:
// an open PROCESS_TERMINATE handle is alive; ERROR_INVALID_PARAMETER is
// Windows' no-such-process account for a pid probe and the only "gone"
// verdict; ERROR_ACCESS_DENIED is proof of LIFE (a process the caller
// cannot open for terminate — elevated or protected) and any other
// unexpected class is treated as alive too, so the kill attempt happens
// and its failure is reported honestly. taskkill's message text is
// locale-dependent, so liveness — never output parsing — decides the
// gone-pid and post-kill states.
//
// Known window (review-flagged, deliberately accepted): a process that
// dies while a handle to it is still open (ours, from a pending Wait)
// can still be OPENED, so a re-probe in that instant classifies pidLive
// and ForceTree reports the taskkill attempt's error for an actually-
// reaped pid — a false LOUD, the preferred side of the trade here.
// Terminal-state probing (GetExitCodeProcess/wait-zero-timeout) would
// close it but is a bigger change with its own races; reserved for a
// follow-up rather than risked alongside the errno triage.
func pidAlive(pid int) pidVerdict {
	h, _, callErr := procOpenProcess.Call(uintptr(processTerminate), uintptr(0), uintptr(pid))
	if h != 0 {
		_, _, _ = procCloseHandle.Call(h)
		return pidLive
	}
	if callErr == errNoSuchProcess {
		return pidGone
	}
	return pidLive
}

// ForceTree terminates pid and every descendant AS OF THIS INSTANT
// (taskkill /T walks the tree at kill time — descendants that daemonized
// away earlier are still caught). Only a gone-by-errno pid skips taskkill,
// and a "failed" kill is re-probed before it is an error — the re-probe
// accepts either a gone verdict (the pid really is reaped) or, for an
// access-denied live pid, the honest taskkill error — so double-cleanup
// stays idempotent for genuinely reaped pids without ever faking success
// on a tree that still runs.
func ForceTree(pid int) error {
	if pidAlive(pid) == pidGone {
		return nil
	}
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err != nil && pidAlive(pid) == pidLive {
		return fmt.Errorf("taskkill /PID %d /T /F: %w (%s)", pid, err, out)
	}
	return nil
}
