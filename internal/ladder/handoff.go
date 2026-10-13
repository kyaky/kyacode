package ladder

import (
	"os"
	"os/exec"
	"sync/atomic"

	"kyacode/internal/procctl"
)

// publication is the joint child/cleanup record. One atomic record — not
// two separately-written slots — is what makes the ticket-03 guarantee
// ("the child is published BEFORE the cleanup, never a live sandbox
// paired with a nil child") structurally untearable: an interrupt either
// sees the whole record or none of it, and the re-read windows cannot
// observe a torn pair between the two loads.
type publication struct {
	child   *os.Process
	cleanup func()
}

// Handoff is the launch's publication slot: the typed successor of
// harness.LaunchHooks (ticket 06 — dissolved wholesale). StartChild
// publishes into it exactly once per launch, after a successful Start;
// Run's escalation windows load it (the ONE snapshot mechanism — a
// single load of the joint record).
type Handoff struct {
	pub atomic.Pointer[publication]
}

// NewHandoff returns an empty publication slot. A nil *Handoff is also
// valid wherever launching happens without supervision (tests, library
// use): publishing skips silently.
func NewHandoff() *Handoff { return &Handoff{} }

// Publish records child + cleanup as ONE joint record, after Start. The
// child leads and the reclaim travels with it — cleanup may be nil (a
// pure-env runner's launch builds no sandbox).
func (h *Handoff) Publish(child *os.Process, cleanup func()) {
	if h == nil {
		return
	}
	h.pub.Store(&publication{child: child, cleanup: cleanup})
}

// Latest is THE snapshot mechanism (one load of the joint record): the
// interrupt-time snapshot and the exit-grace re-read are the same
// one-line call, distinct only in their windows.
func (h *Handoff) Latest() (*os.Process, func()) {
	if h == nil {
		return nil, nil
	}
	p := h.pub.Load()
	if p == nil {
		return nil, nil
	}
	return p.child, p.cleanup
}

// configureGroup is the seam the in-process ordering pin stubs: real
// launches use procctl.ConfigureGroup; the pin swaps in a recorder and
// reruns StartChild against a bogus path so the order — group wiring
// BEFORE Start — is observable headlessly (the old proof needed a
// console-attached Windows child).
var configureGroup = procctl.ConfigureGroup

// StartChild is harness's runChild absorbed (ticket 06): the launch
// mechanics the escalation polices live beside the policy that polices
// them, so the "ConfigureGroup must run before Start or the graceful
// rung is undeliverable" contract is intra-module and error-path pinned.
// Ordering: group wiring, Start, Publish (child leads, after success
// only — a failed Start publishes nothing), then Wait.
func StartChild(cmd *exec.Cmd, cleanup func(), h *Handoff) error {
	configureGroup(cmd)
	if err := cmd.Start(); err != nil {
		// Start failed before the sandbox was ever used by a child — no
		// leak, the caller's deferred cleanup still runs; and nothing is
		// published for an interrupt to read.
		return err
	}
	h.Publish(cmd.Process, cleanup)
	return cmd.Wait()
}
