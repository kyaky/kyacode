//go:build !windows

package procctl

import (
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"kyacode/internal/processtest"
)

// The POSIX halves of procctl's primitives, exercised against a REAL
// child: the package doc promises equal primitive strength on both
// platform families, and until now only the windows file carried test
// bodies (this leg was compile-checked only). Sentinel children re-exec
// the test binary behind an env-selected role — the convention the
// windows tests establish — and keep its exit-code-as-intake convention
// (42 = the soft rung taken as a HANDLED signal; a signal death would
// exit 130/-1-shaped). Ready-then-deliver: every sentinel signals
// handler arming over a pipe (never a wall-clock sleep), so a
// slow-starting child cannot take the interrupt under the default
// disposition and false-red the test.

// roleIntake arms os.Interrupt, signals readiness, and exits 42 on
// intake. roleSwallow arms it, signals readiness, absorbs the signal,
// and keeps sleeping — only ForceTree can end it.
const (
	posixSentinelEnv = "KYA_PROCCTL_POSIX_SENTINEL"
	roleIntake       = "intake"
	roleSwallow      = "swallow"
)

// TestProcctlPosixSentinel is only ever meaningful as a re-exec'd child
// (-test.run body selection); in the parent run it falls through as a
// no-op test.
func TestProcctlPosixSentinel(t *testing.T) {
	switch os.Getenv(posixSentinelEnv) {
	case roleIntake:
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		signalPosixReady()
		<-ch
		os.Exit(42)
	case roleSwallow:
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		signalPosixReady()
		<-ch // absorbed — the test pins that only the force rung ends us
		time.Sleep(60 * time.Second)
	}
}

// signalPosixReady writes one line to the readiness pipe the parent
// handed over as ExtraFiles[0] (fd 3).
func signalPosixReady() {
	if f := os.NewFile(uintptr(3), "ready-pipe"); f != nil {
		_, _ = f.WriteString("ready\n")
	}
}

// startPosixSentinel spawns the re-exec'd test binary in the given role
// and blocks until the child signals that its interrupt handler is
// armed — the parent delivers nothing before that.
func startPosixSentinel(t *testing.T, role string) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("readiness pipe: %v", err)
	}
	cmd := exec.Command(bin, "-test.run", "^TestProcctlPosixSentinel$", "-test.v=false")
	cmd.Env = append(os.Environ(), posixSentinelEnv+"="+role)
	cmd.ExtraFiles = []*os.File{pw}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sentinel: %v", err)
	}
	if err := pw.Close(); err != nil { // parent must drop its write end
		t.Fatalf("close write end: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitForPosixReady(t, cmd, pr)
	return cmd
}

func waitForPosixReady(t *testing.T, cmd *exec.Cmd, pr *os.File) {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 32)
		_, rerr := pr.Read(buf)
		pr.Close()
		if rerr == nil || (rerr != nil && strings.Contains(rerr.Error(), "closed")) {
			close(ready)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		// The pipe mechanism is this leg's own (no stdout marker here);
		// only the verdict sentence is shared, so every readiness failure
		// reads the same across the platform families.
		t.Fatal(processtest.ReadyVerdict)
	}
}

// posixExitCodeOf unwraps a Wait error into the child's real exit code.
func posixExitCodeOf(t *testing.T, err error) int {
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

// TestGracefulInterruptIsTakenAsAHandledSignal pins the soft rung's
// intake contract: a sentinel with an armed handler exits 42 after the
// graceful rung — an exit CODE, never a signal death (the OS default
// disposition would kill it without the handler, which is exactly the
// false-red the readiness pipe prevents).
func TestGracefulInterruptIsTakenAsAHandledSignal(t *testing.T) {
	cmd := startPosixSentinel(t, roleIntake)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := GracefulInterrupt(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("graceful interrupt not delivered: %v", err)
	}

	select {
	case err := <-done:
		if code := posixExitCodeOf(t, err); code != 42 {
			t.Fatalf("child exit code = %d, want 42 (handled intake, not signal death)", code)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child never took the graceful rung")
	}
}

// TestSwallowingChildIsEndedOnlyByTheForceRung pins the escalation
// composition: a child that absorbs the soft rung survives it and is
// still ended by ForceTree — and by nothing less.
func TestSwallowingChildIsEndedOnlyByTheForceRung(t *testing.T) {
	cmd := startPosixSentinel(t, roleSwallow)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := GracefulInterrupt(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("graceful interrupt not delivered: %v", err)
	}
	select {
	case err := <-done:
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

// TestForceTreeTreatsAnAlreadyGonePIDAsSuccess keeps double-cleanup
// idempotent on this platform family too: force-killing a reaped-or-
// beyond-pidmax pid is a non-error, exercising the os.ErrProcessDone
// tolerance in the POSIX ForceTree. The pid is beyond every
// /proc/sys/kernel/pid_max default (up to 4194303), so no live process
// is ever targeted and no pid-reuse race exists.
func TestForceTreeTreatsAnAlreadyGonePIDAsSuccess(t *testing.T) {
	if err := ForceTree(4194303); err != nil {
		t.Fatalf("ForceTree on a certainly-dead pid = %v, want nil (already-gone is success)", err)
	}
}

// TestConfigureGroupIsADeliberateNoOp pins the no-setpgid decision with
// teeth: a child spawned through ConfigureGroup must share OUR process
// group — the terminal's foreground-group broadcast is what rung 1 of
// main's ladder relies on, and a Setpgid creep here would both detach
// the child from the broadcast and put a negative-pid group kill in
// reach of the ladder.
func TestConfigureGroupIsADeliberateNoOp(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	ConfigureGroup(cmd) // same pre-Start ordering production uses
	if err := cmd.Start(); err != nil {
		t.Skipf("could not spawn a probe child (no sleep binary?): %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	childGroup, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid(child): %v", err)
	}
	if own := syscall.Getpgrp(); childGroup != own {
		t.Fatalf("child process group %d != our foreground group %d — the no-setpgid no-op was diverted", childGroup, own)
	}
}
