package ladder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"testing"
	"time"

	"kyacode/internal/procctl"
	"kyacode/internal/processtest"
)

// TestLadderInterruptSentinel is the -test.run child body for the
// group-wiring seam: armed os.Interrupt, exit 42 on intake (a signal
// death would read -1, so 42 proves the soft rung was DELIVERED). It
// announces handler arming on stdout BEFORE accepting anything — the
// parent gates on that marker through the shared processtest watcher
// (never a fixed sleep), so a slow-starting child that has not yet
// installed its handler can never take the interrupt under the default
// disposition and false-red the test as a non-42 exit.
func TestLadderInterruptSentinel(t *testing.T) {
	if os.Getenv("KYA_SENTINEL") != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	fmt.Fprintln(os.Stdout, processtest.ArmingMarker)
	<-ch
	os.Exit(42)
}

// TestStartChildChildTakesTheGracefulRung drives the REAL start mechanics
// (StartChild + Handoff) and then interrupts the published process the
// way Run's soft rung does. On Windows the CTRL_BREAK rung can only be
// delivered to a process created with CREATE_NEW_PROCESS_GROUP, so a
// StartChild that forgets procctl.ConfigureGroup reds here — the wiring
// is pinned at its real effect. The AC-4 ORDERING pin
// (TestStartChildRunsConfigureGroupBeforeStart) already asserts the
// elbow-before-Start sequence headlessly; this test pins the concrete
// spawn-time effect.
func TestStartChildChildTakesTheGracefulRung(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shape: the CTRL_BREAK rung requires the spawn-time group")
	}
	if !procctlTestConsoleAvailable() {
		t.Skip("no attached console; CTRL_BREAK cannot be generated here")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	cmd := exec.Command(bin, "-test.run", "^TestLadderInterruptSentinel$", "-test.v=false")
	cmd.Env = append(os.Environ(), "KYA_SENTINEL=1")
	var watcher processtest.ReadyWatch
	cmd.Stdout = &watcher

	// The published child is polled off the joint record: StartChild
	// publishes exactly once, after Start.
	h := NewHandoff()
	errCh := make(chan error, 1)
	go func() { errCh <- StartChild(cmd, nil, h) }()

	var proc *os.Process
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, _ := h.Latest(); p != nil {
			proc = p
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("StartChild never published the child process")
		}
		select {
		case err := <-errCh:
			t.Fatalf("StartChild exited before publishing the child: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Wait for the child's arming marker — never a fixed sleep (the
	// shared poll; an early child exit reds through the same errCh).
	processtest.AwaitReady(t, &watcher, errCh, func() { _ = cmd.Process.Kill() })

	if err := procctl.GracefulInterrupt(proc); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("graceful rung undeliverable to the StartChild-spawned child (group wiring forgotten?): %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("child exited 0; the soft rung must produce an intake exit (42)")
		}
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("wait error: %v", err)
		}
		if ee.ExitCode() != 42 {
			t.Fatalf("child exit code = %d, want 42 (intake, not signal death)", ee.ExitCode())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child never took the graceful rung")
	}
}

// TestQuietChildBody is the -test.run child body for the soft-grace
// yield pin: a TRAP-INT-IMMUNE child — it arms os.Interrupt, announces
// readiness with the shared marker, and exits 0 cleanly ON the rung-1
// signal. That is the shape whose reaped-child TERM probe printed the
// false-loud diagnostic under the blind-sleep grace: the child is
// already reaped when the sleep ends, so POSIX Terminate returns
// os.ErrProcessDone. Run standalone it does nothing.
func TestQuietChildBody(t *testing.T) {
	if os.Getenv("KYA_QUIET_CHILD") != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	fmt.Fprintln(os.Stdout, processtest.ArmingMarker)
	<-ch
	os.Exit(0)
}

// TestRunSoftGraceYieldsToAReapedChild pins the soft grace as a YIELDING
// window: a trap-INT-immune child exits cleanly DURING the grace (the
// result racing the interrupt wins), and the yielding select must
// deliver that result INSTEAD of letting the blind sleep end and fire
// the TERM rung at the reaped child — POSIX Terminate on which returns
// os.ErrProcessDone and printed the false-loud "TERM rung not delivered"
// diagnostic under the old time.Sleep shape (empirically reproduced
// before the fix). The asserted exit is the honest arm itself:
// Exit{0,nil}, the child's own clean exit. The discriminator is c.ErrOut.
// POSIX-only: on Windows Terminate is a silent no-rung, so the
// false-loud class cannot occur.
func TestRunSoftGraceYieldsToAReapedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the false-loud diagnostic is the POSIX p.Signal os.ErrProcessDone shape; Windows Terminate logs nothing")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	cmd := exec.Command(bin, "-test.run", "^TestQuietChildBody$", "-test.v=false")
	cmd.Env = append(os.Environ(), "KYA_QUIET_CHILD=1")
	var watcher processtest.ReadyWatch
	cmd.Stdout = &watcher

	var diag bytes.Buffer
	cfg := DefaultConfig()
	cfg.ErrOut = &diag
	// The grace is stretched to 5s for this pin: the yield is
	// timing-independent (the child exits ~instantly after the rung, and
	// the yielding select takes the errCh result whenever it lands), but
	// the -race child's exit is slower than the default 500ms window on a
	// loaded runner — a long window makes the premise (the child exits
	// DURING the grace) hold under any load, while the mutated blind
	// sleep still prints the false-loud diagnostic at its (now 5s) end.
	cfg.SoftGrace = 5 * time.Second

	h := NewHandoff()
	ctx, stop := context.WithCancel(context.Background())
	// The interrupt fires only after StartChild has published AND the
	// child has armed its handler — a slow-starting child must trap the
	// rung, not die under the default disposition (the processtest
	// protocol; a deadline kill reds through the work's error instead of
	// deadlocking).
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			p, _ := h.Latest()
			if p != nil && watcher.Ready() {
				time.Sleep(50 * time.Millisecond) // let the child settle into <-ch
				stop()
				return
			}
			if time.Now().After(deadline) {
				if p != nil {
					_ = p.Kill()
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	exit := Run(ctx, stop, func() error {
		return StartChild(cmd, nil, h)
	}, h, &cfg)

	if exit.Code != 0 || exit.Fail != nil {
		t.Fatalf("exit = %d/%v, want the clean-exit result winning the race (Exit{0,nil} — the trap-INT child exited cleanly during the grace)", exit.Code, exit.Fail)
	}
	if strings.Contains(diag.String(), "TERM rung not delivered") {
		t.Errorf("the TERM rung fired at a reaped child under the yielding grace: %q", diag.String())
	}
	if strings.Contains(diag.String(), "graceful interrupt not delivered") {
		t.Errorf("rung 1 printed a delivery failure for an armed child: %q", diag.String())
	}
	if strings.Contains(diag.String(), "forcing exit") {
		t.Errorf("the force path fired for a child that exited cleanly: %q", diag.String())
	}
}
