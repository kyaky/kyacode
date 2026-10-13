//go:build windows

package procctl

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kyacode/internal/processtest"
)

var procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")

// These tests exercise the ladder's real rungs against REAL process trees:
// the PID kyacode holds is the .cmd shim, so force-kill semantics must
// reach descendants (taskkill /T walks the tree at kill time), and the
// graceful rung must be deliverable to a group child (CreateGroup was
// required at spawn time).

// TestProcctlInterruptSentinel is the -test.run sentinel body. As a child
// (KYA_PROCCTL_SENTINEL=1) it arms os.Interrupt and exits 42 on intake —
// a 42 exit proves the soft rung arrived as a signaled INTAKE (signal
// death would read -1); KYA_PROCCTL_SENTINEL=swallow absorbs it and keeps
// sleeping, so only the force rung can end it. Both roles announce
// handler arming on stdout BEFORE the signal could arrive — the parent
// waits on that marker instead of a fixed sleep, so a slow-starting
// child cannot take the interrupt under the default disposition and
// false-red the intake test.
func TestProcctlInterruptSentinel(t *testing.T) {
	role := os.Getenv("KYA_PROCCTL_SENTINEL")
	switch role {
	case "1":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		fmt.Fprintln(os.Stdout, processtest.ArmingMarker)
		<-ch
		os.Exit(42)
	case "swallow":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		fmt.Fprintln(os.Stdout, processtest.ArmingMarker)
		<-ch
		fmt.Fprintln(os.Stderr, "swallowed") // never printed to a clean parent's console output
		time.Sleep(60 * time.Second)         // only ForceTree ends this
	}
	// Parent side: fall through as a no-op test.
}

// TestProcctlTreeSentinel is the -test.run sentinel that builds the
// two-level tree: it spawns a cmd.exe grandchild (ping = a real sleeping
// descendant) and writes the grandchild PID to the marker file named by
// KYA_PROCCTL_TREE_MARKER, then holds the process open.
func TestProcctlTreeSentinel(t *testing.T) {
	if os.Getenv("KYA_PROCCTL_TREE") != "1" {
		return
	}
	marker := os.Getenv("KYA_PROCCTL_TREE_MARKER")
	if marker == "" {
		os.Exit(3)
	}
	cmd := exec.Command("cmd", "/c", "ping", "-n", "61", "127.0.0.1", ">", "nul")
	if err := cmd.Start(); err != nil {
		os.Exit(4)
	}
	_ = os.WriteFile(marker, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	time.Sleep(60 * time.Second)
}

// sentinelCmd spawns the re-exec'd sentinel in the given role and returns
// it plus the shared arming-marker watcher (internal/processtest is the
// one home of the watcher/poll protocol the ladder's wiring tests ride
// too) — the caller gates every rung on AwaitReady, never a fixed sleep.
func sentinelCmd(t *testing.T, role string) (*exec.Cmd, *processtest.ReadyWatch) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	cmd := exec.Command(bin, "-test.run", "^TestProcctlInterruptSentinel$", "-test.v=false")
	cmd.Env = append(os.Environ(), "KYA_PROCCTL_SENTINEL="+role)
	w := &processtest.ReadyWatch{}
	cmd.Stdout = w
	return cmd, w
}

// consoleAvailable reports whether this process has an attached console:
// CTRL_BREAK delivery is console-backed, so tests skip rather than
// false-red in headless contexts.
func consoleAvailable() bool {
	h, _, _ := procGetConsoleWindow.Call()
	return h != 0
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("child exited 0; expected a signal-intake exit")
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("wait error: %v", err)
	}
	return ee.ExitCode()
}

// consoleInitFailed reports the 0xc0000142 (STATUS_DLL_INIT_FAILED)
// shape: in hosts with no real window station the grouped child cannot
// initialize its console at all — that is a HOST limitation, not a
// procctl defect, so tests skip instead of false-red.
func consoleInitFailed(t *testing.T, err error) bool {
	t.Helper()
	return err != nil && strings.Contains(err.Error(), "0xc0000142")
}

// TestGracefulInterruptEndsAGroupChild: ConfigureGroup at spawn time is
// what makes the graceful rung addressable — without the group flag,
// CTRL_BREAK cannot be delivered to the pid and the child survives.
func TestGracefulInterruptEndsAGroupChild(t *testing.T) {
	if !consoleAvailable() {
		t.Skip("no attached console; CTRL_BREAK cannot be generated here")
	}
	cmd, ready := sentinelCmd(t, "1")
	ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = ForceTree(cmd.Process.Pid) })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	processtest.AwaitReady(t, ready, done, func() { _ = cmd.Process.Kill() }) // handler armed before any rung may arrive

	if err := GracefulInterrupt(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("graceful interrupt failed to deliver (group wiring?): %v", err)
	}

	select {
	case err := <-done:
		if consoleInitFailed(t, err) {
			t.Skipf("host cannot init a grouped child's console (not a procctl defect): %v", err)
		}
		if code := exitCodeOf(t, err); code != 42 {
			t.Fatalf("child exit code = %d, want 42 (the soft rung must be an intake, not a signal death)", code)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child never took the graceful rung (were we group-configured?)")
	}
}

// TestChildThatSwallowsTheGracefulRungIsStillKilledByTheForceRung pins the
// escalation composition: a child absorbing the soft rung survives it and
// is still ended by ForceTree (and only by ForceTree).
func TestChildThatSwallowsTheGracefulRungIsStillKilledByTheForceRung(t *testing.T) {
	if !consoleAvailable() {
		t.Skip("no attached console; CTRL_BREAK cannot be generated here")
	}
	cmd, ready := sentinelCmd(t, "swallow")
	ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = ForceTree(cmd.Process.Pid) })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// The swallowing child must have ARMED its handler before the soft
	// rung lands: an un-armed child takes CTRL_BREAK under the default
	// disposition and false-reds "must NOT end" — the rung's delivery is
	// gated on the same readiness marker the intake test uses.
	processtest.AwaitReady(t, ready, done, func() { _ = cmd.Process.Kill() })

	if err := GracefulInterrupt(cmd.Process); err != nil {
		t.Fatalf("graceful interrupt: %v", err)
	}
	select {
	case err := <-done:
		if consoleInitFailed(t, err) {
			t.Skipf("host cannot init a grouped child's console (not a procctl defect): %v", err)
		}
		t.Fatalf("the soft rung must NOT end a swallowing child (wait: %v)", err)
	case <-time.After(700 * time.Millisecond):
	}

	if err := ForceTree(cmd.Process.Pid); err != nil {
		t.Fatalf("force tree: %v", err)
	}
	select {
	case <-done:
		// terminated — the force rung won
	case <-time.After(8 * time.Second):
		t.Fatal("force rung did not end a swallowing child")
	}
}

// TestForceTreeKillsTheWholeSpawnedTree: the force rung must reach the
// shim's DESCENDANTS (grandchild ping still sleeping), not just the pid
// kyacode held — a Kill-only stopgap leaves the grandchild running.
func TestForceTreeKillsTheWholeSpawnedTree(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command(bin, "-test.run", "^TestProcctlTreeSentinel$", "-test.v=false")
	cmd.Env = append(os.Environ(), "KYA_PROCCTL_TREE=1", "KYA_PROCCTL_TREE_MARKER="+marker)
	ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Covers readGrandchildPid failures too: the /T walk reaches the ping
	// grandchild from the shim pid without needing its marker.
	t.Cleanup(func() { _ = ForceTree(cmd.Process.Pid) })

	grandchild := readGrandchildPid(t, marker)
	assertPidAlive(t, cmd.Process.Pid)
	assertPidAlive(t, grandchild)

	if err := ForceTree(cmd.Process.Pid); err != nil {
		t.Fatalf("ForceTree: %v", err)
	}

	waitForDead(t, "child shim after force", cmd.Process.Pid)
	waitForDead(t, "grandchild after force", grandchild)
}

// TestForceTreeTreatsAnAlreadyGonePIDAsSuccess keeps double-cleanup
// idempotent: force-killing a reaped pid is a non-error.
func TestForceTreeTreatsAnAlreadyGonePIDAsSuccess(t *testing.T) {
	if err := ForceTree(4194303); err != nil {
		t.Fatalf("ForceTree on a certainly-dead pid = %v, want nil (already-gone is success)", err)
	}
	// And the probe itself: a certainly-dead pid must classify gone
	// (OpenProcess fails with ERROR_INVALID_PARAMETER, the no-such-process
	// class — the only "gone" answer).
	if v := pidAlive(4194303); v != pidGone {
		t.Fatalf("certainly-dead pid probed %d, want pidGone", v)
	}
}

// TestForceTreeOnAnAccessDeniedButLivePidNeverFakesSuccess pins the
// errno-aware liveness split: a live process the caller cannot open for
// terminate must classify ALIVE, drive taskkill, and report the kill
// failure honestly — the old zero-handle-means-dead probe made ForceTree
// return success with taskkill never invoked, so a protected child kept
// holding the gateway key while the bookkeeping believed cleanup
// delivered. Pid 4 (the System process) is Windows' canonical
// by-construction access-denied target: OpenProcess(PROCESS_TERMINATE)
// fails with ERROR_ACCESS_DENIED for every user-mode caller, and taskkill
// refuses it the same way — nothing on the machine is harmed by the
// attempt the ladder must make.
func TestForceTreeOnAnAccessDeniedButLivePidNeverFakesSuccess(t *testing.T) {
	if v := pidAlive(4); v != pidLive {
		t.Fatalf("pid 4 (System) probed %d, want pidLive (access-denied is proof of life)", v)
	}
	err := ForceTree(4)
	if err == nil {
		t.Fatal("ForceTree on pid 4 returned nil — a live-but-unopenable tree must not be reported as cleaned")
	}
	if !strings.Contains(err.Error(), "taskkill") {
		t.Errorf("the error must name the failed kill, got: %v", err)
	}
}

func readGrandchildPid(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("grandchild never reported its pid")
	return 0
}

// assertPidAlive asserts the pid is STILL running (tasklist is the probe
// the kill target itself cannot fake — distinct from pidAlive, the
// production OpenProcess check).
func assertPidAlive(t *testing.T, pid int) {
	t.Helper()
	if !tasklistHasPid(t, pid) {
		t.Fatalf("pid %d expected alive but tasklist does not list it", pid)
	}
}

// waitForDead polls until the pid leaves tasklist (tree termination is a
// short walk; 10s is generous and covers CI latency).
func waitForDead(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !tasklistHasPid(t, pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s (pid %d) still alive after the force rung", what, pid)
}

func tasklistHasPid(t *testing.T, pid int) bool {
	t.Helper()
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").CombinedOutput()
	if err != nil {
		t.Fatalf("tasklist: %v (%s)", err, out)
	}
	return strings.Contains(string(out), fmt.Sprintf(`,"%d",`, pid))
}
