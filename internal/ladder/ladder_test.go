package ladder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"kyacode/internal/procctl"
)

// fastConfig is the deterministic timetable: every rung fires in
// milliseconds so the suite drives the FLOW of the escalation, not its
// production pacing. The production pacing itself is pinned separately
// (TestDefaultConfigIsTheDocumentedTimetable).
func fastConfig() *Config {
	return &Config{
		SoftGrace: 10 * time.Millisecond,
		TermWait:  30 * time.Millisecond,
		ExitGrace: 30 * time.Millisecond,
	}
}

// world is one test's recorded state: seam recordings (rung order and
// targets at the production call sites) plus the module's stderr. Reads
// happen after Run returns (happens-before via the return); the mutex is
// belt against cross-test seam reuse patterns.
type world struct {
	mu     sync.Mutex
	order  []string // "disarm", "graceful:<pid>", "terminate:<pid>", "force:<pid>", "cleanup"
	stderr bytes.Buffer
}

func (w *world) record(e string) {
	w.mu.Lock()
	w.order = append(w.order, e)
	w.mu.Unlock()
}

// arm swaps every primitive seam for a recorder (restored on cleanup) and
// points the config's ErrOut at the recorded buffer.
func (w *world) arm(tb *testing.T) *Config {
	tb.Helper()
	gracefulOrig, terminateOrig, forceOrig := gracefulInterrupt, terminate, forceTree
	gracefulInterrupt = func(p *os.Process) error {
		w.record(fmt.Sprintf("graceful:%d", p.Pid))
		return nil
	}
	terminate = func(p *os.Process) error {
		w.record(fmt.Sprintf("terminate:%d", p.Pid))
		return nil
	}
	forceTree = func(pid int) error {
		w.record(fmt.Sprintf("force:%d", pid))
		return nil
	}
	tb.Cleanup(func() {
		gracefulInterrupt, terminate, forceTree = gracefulOrig, terminateOrig, forceOrig
	})
	cfg := fastConfig()
	cfg.ErrOut = &w.stderr
	return cfg
}

// fakeChild is the published-but-pretend child: 4242 is arbitrary - the
// seams record targets, no real process exists.
func fakeChild() *os.Process {
	return &os.Process{Pid: 4242}
}

// order returns a snapshot of the recorded sequence.
func (w *world) events() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.order...)
}

// waitEvent polls the recorded events until the expected entry appears
// (the deterministic shape for the escalation tests: expiry-removal of the
// forced path, not a fixed sleep). It fails loudly if the event never
// lands within d.
func waitEvent(t *testing.T, w *world, event string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, e := range w.events() {
			if e == event {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("event %q never appeared within %v (events %v)", event, d, w.events())
}

// TestRunCleanExitIsZeroWithoutBanner pins Exit{0,nil}: no escalation, no
// banner, Fail never set, disarm run exactly once on the entered arm
// (both arms disarm synchronously on entry).
func TestRunCleanExitIsZeroWithoutBanner(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	disarms := 0
	exit := Run(context.Background(), func() { disarms++ }, func() error { return nil }, NewHandoff(), cfg)
	if exit.Code != 0 || exit.Fail != nil {
		t.Fatalf("exit = %+v, want Exit{0,nil}", exit)
	}
	if disarms != 1 {
		t.Errorf("disarm ran %d times on the clean arm, want exactly 1", disarms)
	}
	if w.stderr.Len() != 0 {
		t.Errorf("clean path wrote %q; must stay silent", w.stderr.String())
	}
}

// TestRunWorkErrorIsTheRealErrorChannel pins Exit{1,err}: main prints the
// one "error:" line; no banner (the classes are disjoint).
func TestRunWorkErrorIsTheRealErrorChannel(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	want := errors.New("launch exploded")
	exit := Run(context.Background(), func() {}, func() error { return want }, NewHandoff(), cfg)
	if exit.Code != 1 || !errors.Is(exit.Fail, want) {
		t.Fatalf("exit = %+v, want Exit{1, the work error}", exit)
	}
	if w.stderr.Len() != 0 {
		t.Errorf("error path wrote %q; the banner is forced-path only", w.stderr.String())
	}
}

// TestInterruptedLaunchExitsHonestly pins the M-5/M-12 half of the
// honesty contract with no published record: after an interrupt with no
// child (a model-less library-style launch, or a launch still mid-Start),
// no rung may run - and a cleanly returning child is Exit{0,nil}, never a
// fake failure or a banner.
func TestInterruptedLaunchExitsHonestly(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	ctx, cancel := context.WithCancel(context.Background())
	work := func() error {
		<-ctx.Done()
		time.Sleep(5 * time.Millisecond) // returns inside the ExitGrace window
		return nil
	}
	exitCh := make(chan Exit, 1)
	go func() { exitCh <- Run(ctx, func() {}, work, NewHandoff(), cfg) }()
	cancel()
	exit := <-exitCh
	cancel()
	if exit.Code != 0 || exit.Fail != nil {
		t.Fatalf("exit = %+v, want the honest clean exit", exit)
	}
	if w.stderr.Len() != 0 {
		t.Errorf("honest exit wrote %q; the banner must not print", w.stderr.String())
	}
	for _, e := range w.events() {
		switch e {
		case "cleanup", "force:4242":
			t.Fatalf("rung %q ran with no published child (order %v)", e, w.order)
		}
	}
}

// TestRunFailingChildExitDuringGraceCarriesTheRealError pins the other
// honesty half: a child failing during an interrupt's grace reports ITS
// error, Exit{1,err}, no banner.
func TestRunFailingChildExitDuringGraceCarriesTheRealError(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	ctx, cancel := context.WithCancel(context.Background())
	want := errors.New("child failed during grace")
	work := func() error {
		<-ctx.Done()
		return want
	}
	exitCh := make(chan Exit, 1)
	go func() { exitCh <- Run(ctx, func() {}, work, NewHandoff(), cfg) }()
	cancel()
	exit := <-exitCh
	cancel()
	if exit.Code != 1 || !errors.Is(exit.Fail, want) {
		t.Fatalf("exit = %+v, want Exit{1, the real error}", exit)
	}
	if w.stderr.Len() != 0 {
		t.Errorf("grace exit wrote %q; the banner must not print", w.stderr.String())
	}
}

// TestRunGraceExpiryForcesThenReclaimsThenBanners pins the expired path:
// the re-read target is force-killed BEFORE the reclaim runs, then the
// "interrupt: forcing exit" banner, then Exit{1,nil} - and Fail is nil
// (the forced path never prints "error:").
func TestRunGraceExpiryForcesThenReclaimsThenBanners(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := NewHandoff()
	block := make(chan struct{})
	work := func() error {
		<-ctx.Done()
		<-block // outlasts both grace windows
		return nil
	}
	exitCh := make(chan Exit, 1)
	go func() { exitCh <- Run(ctx, func() {}, work, h, cfg) }()
	h.Publish(fakeChild(), func() { w.record("cleanup") })
	cancel()
	waitEvent(t, w, "force:4242", 5*time.Second) // both windows elapsed while the child stayed blocked
	// Release the child only after the ladder already decided: a sooner
	// return would take the honesty arm (its own pinned test), not expiry.
	time.AfterFunc(200*time.Millisecond, func() { close(block) })
	exit := <-exitCh
	if exit.Code != 1 || exit.Fail != nil {
		t.Fatalf("exit = %+v, want Exit{1,nil} (the forced path never carries Fail)", exit)
	}
	if out := w.stderr.String(); !bytes.Contains([]byte(out), []byte("interrupt: forcing exit")) {
		t.Fatalf("forced path stderr = %q, want the forced banner", out)
	}
	forceSeen, cleanupSeen := false, false
	for _, e := range w.events() {
		if e == "force:4242" {
			forceSeen = true
		}
		if e == "cleanup" {
			if !forceSeen {
				t.Fatalf("cleanup ran before the force (order: %v); the H-3 ordering guard must hold", e)
			}
			cleanupSeen = true
		}
	}
	if !forceSeen || !cleanupSeen {
		t.Fatalf("recorded order %v lacks force-then-cleanup (stderr %q)", w.events(), w.stderr.String())
	}
}

// TestRunRungOrderOnAnInterruptedChild pins the escalation FLOW through
// the production seams: disarm FIRST (the second-Ctrl-C disposition),
// then graceful -> terminate -> force, all against the interrupt-time
// snapshot pid.
func TestRunRungOrderOnAnInterruptedChild(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := NewHandoff()
	block := make(chan struct{})
	work := func() error {
		<-ctx.Done()
		<-block
		return nil
	}
	exitCh := make(chan Exit, 1)
	go func() { exitCh <- Run(ctx, func() { w.record("disarm") }, work, h, cfg) }()
	h.Publish(fakeChild(), func() { w.record("cleanup") })
	cancel()
	waitEvent(t, w, "force:4242", 5*time.Second)
	exit := <-exitCh
	close(block) // late drain; the forced decision is already in hand

	wantPrefix := []string{"disarm", "graceful:4242", "terminate:4242", "force:4242"}
	if exit.Code != 1 || exit.Fail != nil {
		t.Fatalf("forced exit = %+v, want Exit{1,nil}", exit)
	}
	got := w.events()
	if len(got) < len(wantPrefix) {
		t.Fatalf("order %v lacks the rung prefix %v (exit %+v, stderr %q)", got, wantPrefix, exit, w.stderr.String())
	}
	for i, want := range wantPrefix {
		if got[i] != want {
			t.Fatalf("order = %v, want the prefix %v (disarm first: the second-Ctrl-C disposition)", got, wantPrefix)
		}
	}
}

// TestRunForceTargetsTheReReadChildAfterALateStart pins the H-3 guard
// behaviorally: nothing published at interrupt time (the launch was still
// mid-Start), the record lands during the grace, and the exit-grace
// re-read - not the nil interrupt-time snapshot - decides the force
// target and the reclaim.
func TestRunForceTargetsTheReReadChildAfterALateStart(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := NewHandoff()
	block := make(chan struct{})
	work := func() error {
		<-ctx.Done()
		<-block
		return nil
	}
	exitCh := make(chan Exit, 1)
	go func() { exitCh <- Run(ctx, func() { w.record("disarm") }, work, h, cfg) }()
	cancel()
	// The disarm event proves the interrupt arm entered and took its
	// snapshot (nil — no publish yet); this is the deterministic shape for
	// "the record lands after the interrupt was already taken", with no
	// sleep racing the ExitGrace expiry.
	waitEvent(t, w, "disarm", 5*time.Second)
	h.Publish(fakeChild(), func() { w.record("cleanup") })
	waitEvent(t, w, "force:4242", 5*time.Second)
	exit := <-exitCh
	close(block) // late drain; the decision is already in hand
	if exit.Code != 1 || exit.Fail != nil {
		t.Fatalf("exit = %+v, want Exit{1,nil}", exit)
	}
	sawForce := false
	for _, e := range w.events() {
		if e == "force:4242" {
			sawForce = true
		}
		if e == "cleanup" && !sawForce {
			t.Fatalf("cleanup before force (order %v) - the H-3 ordering guard must hold", e)
		}
	}
	if !sawForce {
		t.Fatalf("a late-published child was never force-killed - the H-3 re-read must decide")
	}
	// The nil snapshot skipped the rungs: the joint record's child was
	// force-killed WITHOUT a graceful/terminate pair first (the pair ran
	// against nothing at interrupt time).
	for _, e := range w.events() {
		if e == "graceful:4242" || e == "terminate:4242" {
			t.Fatalf("rung %q ran against the late-published child; the interrupt-time snapshot was nil", e)
		}
	}
}

// TestDefaultConfigIsTheDocumentedTimetable pins the production timetable
// and error sink as constants (the values that moved out of main.go and
// live in one place now).
func TestDefaultConfigIsTheDocumentedTimetable(t *testing.T) {
	c := DefaultConfig()
	if c.SoftGrace != 500*time.Millisecond || c.TermWait != 1500*time.Millisecond || c.ExitGrace != 2*time.Second {
		t.Errorf("default timetable = %v/%v/%v, want the documented 500ms/1500ms/2s", c.SoftGrace, c.TermWait, c.ExitGrace)
	}
	if c.ErrOut != os.Stderr {
		t.Errorf("default ErrOut = %v, want os.Stderr", c.ErrOut)
	}
}

// TestWindowsTerminateIsTheNoRungSilence pins procctl.Terminate's
// platform fact where it is consumed: on Windows nil means "no TERM rung
// exists here", never a delivery success — a refactor symmetrizing the
// stub back into p.Signal would print a fake "TERM rung not delivered"
// diagnostic on every Windows interrupt via Run's nil==delivered
// encoding, and this pin reds it.
func TestWindowsTerminateIsTheNoRungSilence(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shape: the no-rung fact")
	}
	if err := procctl.Terminate(&os.Process{Pid: 4242}); err != nil {
		t.Fatalf("Terminate(fake) = %v, want nil (no TERM rung exists; nil is silence, not delivery)", err)
	}
}

// TestAnInstantCleanResultStaysQuietThroughAnInterrupt pins the honesty
// fast-path: when the child's clean result is in hand at the same instant
// as the interrupt, no rung — not even a diagnostic — may fire; the
// Run arm re-checks the buffered result non-blockingly before spending
// the escalation on a reaped child.
func TestAnInstantCleanResultStaysQuietThroughAnInterrupt(t *testing.T) {
	w := &world{}
	cfg := w.arm(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()                            // armed AND fired before Run's select: the race arm
	work := func() error { return nil } // result in hand immediately
	h := NewHandoff()
	exit := Run(ctx, func() {}, work, h, cfg)
	_ = cancel
	if exit.Code != 0 || exit.Fail != nil {
		t.Fatalf("exit = %+v, want the honest clean exit", exit)
	}
	if w.stderr.Len() != 0 {
		t.Errorf("quiet exit wrote %q; no diagnostic may print on the honesty path", w.stderr.String())
	}
	for _, e := range w.events() {
		switch e {
		case "graceful:4242", "terminate:4242", "force:4242", "cleanup":
			t.Fatalf("rung %q fired for an already-clean child (events %v)", e, w.events())
		}
	}
}

// TestRunNilConfigAndNilHandoffAreValid pins the library shape: nil cfg
// (defaults, real stderr unused here) and a nil *Handoff are both valid.
func TestRunNilConfigAndNilHandoffAreValid(t *testing.T) {
	exit := Run(context.Background(), func() {}, func() error { return nil }, nil, nil)
	if exit.Code != 0 || exit.Fail != nil {
		t.Fatalf("nil cfg/handoff exit = %+v, want clean", exit)
	}
}
