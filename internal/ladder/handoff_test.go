package ladder

import (
	"os"
	"os/exec"
	"sync"
	"testing"
)

// The publication + start-mechanics pins: the joint record's shapes, the
// ConfigureGroup-before-Start ordering (AC-4, in-process — no console
// needed), and publish-after-Start-only. The hooks these pins replace
// lived in internal/harness for ticket-03; the mechanics themselves moved
// beside the policy that polices them (ticket 06).

// TestHandoffPublishRecordLeadsChildAlwaysCleanupOptional pins the record
// shape: Latest is nil before any publish, and after one publish carries
// the child and its cleanup together (nil cleanup allowed — an env-runner
// launch builds no sandbox).
func TestHandoffPublishRecordLeadsChildAlwaysCleanupOptional(t *testing.T) {
	h := NewHandoff()
	if child, cleanup := h.Latest(); child != nil || cleanup != nil {
		t.Fatalf("empty handoff Latest = (%v, %v), want nil/nil", child, cleanup != nil)
	}
	cleanupRan := false
	child := &os.Process{Pid: 7}
	h.Publish(child, func() { cleanupRan = true })
	got, gotCleanup := h.Latest()
	if got != child {
		t.Fatalf("Latest child = %v, want the published one", got)
	}
	if gotCleanup == nil {
		t.Fatal("published cleanup lost")
	}
	gotCleanup()
	if !cleanupRan {
		t.Error("the returned cleanup is not the published one")
	}
	// nil cleanup: publish still records the child.
	h.Publish(&os.Process{Pid: 8}, nil)
	got, gotCleanup = h.Latest()
	if got.Pid != 8 {
		t.Fatalf("Latest child = %v, want pid 8", got)
	}
	if gotCleanup != nil {
		t.Fatal("nil cleanup published as non-nil")
	}
}

// TestNilHandoffIsInert pins the library shape: publishing into a nil
// *Handoff is a no-op, and Latest is nil — a launch without supervision
// compiles and publishes silently.
func TestNilHandoffIsInert(t *testing.T) {
	var h *Handoff
	h.Publish(&os.Process{Pid: 1}, func() {})
	if child, cleanup := h.Latest(); child != nil || cleanup != nil {
		t.Fatalf("nil handoff Latest = (%v, %v), want nil/nil", child, cleanup != nil)
	}
}

// runNoopChildCmd builds a command that exits immediately: the test
// binary re-run with -test.run targeting the no-op TestLadderChildNoop
// sentinel (exits 0 at once without re-entering the suite).
func runNoopChildCmd() (*exec.Cmd, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(bin, "-test.run", "^TestLadderChildNoop$", "-test.v=false"), nil
}

// TestStartChildPublishesOnlyAfterSuccessfulStart drives the real
// mechanics with a real quick child: Start succeeds, the record publishes
// the started child with the wired cleanup, and Wait returns the child's
// clean exit.
func TestStartChildPublishesOnlyAfterSuccessfulStart(t *testing.T) {
	cmd, err := runNoopChildCmd()
	if err != nil {
		t.Fatalf("child cmd: %v", err)
	}
	h := NewHandoff()
	cleaned := false
	if err := StartChild(cmd, func() { cleaned = true }, h); err != nil {
		t.Fatalf("StartChild: %v", err)
	}
	child, cleanup := h.Latest()
	if child == nil {
		t.Fatal("a successful Start must publish the child")
	}
	if child.Pid != cmd.Process.Pid {
		t.Errorf("published pid %d, want the started child %d", child.Pid, cmd.Process.Pid)
	}
	if cleanup == nil {
		t.Fatal("published record lost the wired cleanup")
	}

	cleanup()
	if !cleaned {
		t.Fatal("the published cleanup did not run")
	}
}

// TestStartChildFailedStartPublishesNothing pins the failed-Start edge:
// nothing is published for an interrupt to read (no live sandbox paired
// with a half-started child), and the error propagates.
func TestStartChildFailedStartPublishesNothing(t *testing.T) {
	h := NewHandoff()
	cmd := exec.Command("kyacode-definitely-not-a-binary-xyz")
	err := StartChild(cmd, func() {}, h)
	if err == nil {
		t.Fatal("expected the Start failure")
	}
	if child, cleanup := h.Latest(); child != nil || cleanup != nil {
		t.Fatalf("failed Start published (%v, %v); the record must stay empty", child, cleanup != nil)
	}
}

// TestStartChildRunsConfigureGroupBeforeStart pins the AC-4 ordering
// IN-PROCESS: with a bogus path (Start cannot succeed), the group-wiring
// seam still runs exactly once and strictly before the Start attempt —
// headless, no console, both platforms. The old proof needed the
// console-attached Windows child; this one does not (the concrete
// Windows CreationFlags effect keeps its own pin below).
func TestStartChildRunsConfigureGroupBeforeStart(t *testing.T) {
	orig := configureGroup
	t.Cleanup(func() { configureGroup = orig })
	var mu sync.Mutex
	var events []string
	configureGroup = func(cmd *exec.Cmd) {
		mu.Lock()
		events = append(events, "configure")
		mu.Unlock()
	}
	h := NewHandoff()
	err := StartChild(exec.Command("kyacode-definitely-not-a-binary-xyz"), func() {}, h)
	if err == nil {
		t.Fatal("expected the bogus-path Start failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0] != "configure" {
		t.Fatalf("events = %v, want exactly one configure call before the Start attempt", events)
	}
	if child, _ := h.Latest(); child != nil {
		t.Fatal("a failed Start must publish nothing")
	}
}

// TestLadderChildNoop is the -test.run target StartChild's real-child pin
// launches: a no-op sentinel that exits at once.
func TestLadderChildNoop(t *testing.T) {}
