// Package ladder owns the interrupt/cleanup ladder main used to inline
// (ticket 06): the goroutine that runs the CLI's work, the published
// child/cleanup handoff, the escalation rungs (graceful interrupt → TERM
// → windows → tree force), and the process exit contract. Once
// thrice-patched inline (H-3, the ladder rung, the typed hooks), the
// ladder now carries its own in-process tests at fast configurable
// timings — the correctness argument lives on this package, not in
// main().
//
// Layering: procctl (primitives) ← ladder (this: start mechanics +
// publication + escalation policy + exit contract) ← harness (launch
// composition) ← cmd (dispatch).
package ladder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"kyacode/internal/procctl"
)

// Exit is the process exit contract Run returns to main's single exit
// site. The classes are disjoint and pinned:
//
//   - Exit{0, nil}: clean return — including the M-5/M-12 case where the
//     child finished cleanly during an interrupt grace (never a fake 1).
//   - Exit{1, err}: the real error channel — a normal fail or a fail
//     returned while the ladder was mid-escalation. main prints the one
//     "error: <err>" line.
//   - Exit{1, nil}: grace expired. The module ITSELF reported the
//     escalation diagnoses and the "interrupt: forcing exit" banner to
//     ErrOut before returning; main prints nothing else. Never mixed with
//     Fail (a forced exit does not print "error:").
type Exit struct {
	Code int
	Fail error
}

// Config carries the escalation's owned timetable and error sink.
// Production passes nil (DefaultConfig); the suite sets short durations
// so every rung drives deterministically. The fields are the timetable —
// the constants in main.go's documentable ladder persisted as values, so
// a timing change is one edit here and the suite sees it.
type Config struct {
	ErrOut    io.Writer
	SoftGrace time.Duration // the INT→TERM gap
	TermWait  time.Duration // first escalation select → force
	ExitGrace time.Duration // force-window expiry → reclaim + exit-1
}

// DefaultConfig is the production timetable (500ms before TERM; up to
// 1500ms from the first escalation select to force; up to 2s grace for
// the work goroutine to return before reclaim + exit 1).
func DefaultConfig() Config {
	return Config{
		ErrOut:    os.Stderr,
		SoftGrace: 500 * time.Millisecond,
		TermWait:  1500 * time.Millisecond,
		ExitGrace: 2 * time.Second,
	}
}

// The escalation's primitive seams are package vars ONLY so the in-process
// suite can record rung order and targets deterministically (the old proof
// needed a console-attached Windows child); every production call site
// stays exactly one, and each var's production value is the procctl
// primitive.
var (
	gracefulInterrupt = procctl.GracefulInterrupt
	terminate         = procctl.Terminate
	forceTree         = procctl.ForceTree
)

// forceKill is the ONE force-kill call site: both escalation windows
// (TermWait expiry and ExitGrace expiry) reach the tree force only
// through here.
//
// Accepted risk (carried over verbatim from the inline ladder, unchanged
// by ticket 06): the target is a bare pid with no revalidation against
// the original process — a Windows pid recycled inside the escalation
// window would aim the tree force at the new owner. procctl documents
// the opposite false-LOUD window; the revalidation design stays procctl
// follow-up territory.
func forceKill(ErrOut io.Writer, pid int) {
	if err := forceTree(pid); err != nil {
		fmt.Fprintf(ErrOut, "kyacode: force-kill not delivered: %v\n", err)
	}
}

// Run drives work under the interrupt escalation. ctx is the CLI's armed
// signal context (main owns signal.NotifyContext — the operator-facing
// disposition is CLI surface, and staying signal-free is what makes this
// module in-process testable); disarm is that arming's teardown, run
// synchronously on BOTH arms the instant an arm is entered, BEFORE any
// escalation fires — this ordering IS the documented second-Ctrl-C
// disposition (the impatient operator's next Ctrl-C is default-fatal
// while this ladder runs) and is pin-tested.
//
// work runs on this module's goroutine exactly as main.go used to:
// buffered-1 error channel with a select-default send — a late return
// after the process already decided never blocks, on the buffer alone.
func Run(ctx context.Context, disarm func(), work func() error, h *Handoff, cfg *Config) Exit {
	c := DefaultConfig()
	if cfg != nil {
		c = *cfg
		// Adopter-by-adopter defaults: a partially-filled Config inherits
		// the documented pacing for what it left zero — a zero duration
		// otherwise means an unpaced ladder (immediate force), which is
		// never a legitimate intent.
		if c.ErrOut == nil {
			c.ErrOut = os.Stderr
		}
		if c.SoftGrace == 0 {
			c.SoftGrace = DefaultConfig().SoftGrace
		}
		if c.TermWait == 0 {
			c.TermWait = DefaultConfig().TermWait
		}
		if c.ExitGrace == 0 {
			c.ExitGrace = DefaultConfig().ExitGrace
		}
	}

	errCh := make(chan error, 1)
	go func() {
		err := work()
		select {
		case errCh <- err:
		default: // the process already decided via a previous window
		}
	}()

	select {
	case err := <-errCh:
		disarm()
		return report(err)
	case <-ctx.Done():
		disarm()
		// A result that raced the interrupt wins the honesty arm without
		// spending a single rung on it (the child may already be reaped —
		// rungs against it would only print diagnostics for a ghost).
		select {
		case err := <-errCh:
			return report(err)
		default:
		}
		// The interrupt-time snapshot is ONE load of the joint record —
		// nil-child launches (no publish) skip every rung as before. The
		// record pairs child and cleanup, so the reclaim is only ever read
		// at the exit-grace re-read below; the old two-slot fallback
		// (first snapshot's cleanup if the re-read had none) is
		// structurally dead with a joint record — a published record is
		// never un-published (Latest is monotonic).
		child, _ := h.Latest()
		if child != nil {
			// Rung 1 (soft): CTRL_BREAK to the child's process group on
			// Windows (reaches the .cmd shim AND its node/bun
			// descendants), SIGINT to the foreground-group child on POSIX.
			if err := gracefulInterrupt(child); err != nil && !errors.Is(err, os.ErrProcessDone) {
				fmt.Fprintf(c.ErrOut, "kyacode: graceful interrupt not delivered: %v\n", err)
			}
			// The soft grace is a YIELDING window, not a blind sleep: a
			// child that exits cleanly during the gap must skip the TERM
			// rung at this window too (the honesty rule above is enforced
			// at every wait, not only at the interrupt instant). Pacing is
			// unchanged when no result arrives.
			select {
			case err := <-errCh:
				return report(err)
			case <-time.After(c.SoftGrace):
			}
			// Rung 2 (TERM): Terminate carries the platform fact — Windows
			// HAS no TERM rung (nil, logging nothing) so no fake delivery
			// failure is ever printed; POSIX logs a real one, EXCEPT an
			// already-reaped child (os.ErrProcessDone): the rung is moot,
			// not a delivery failure (the microsecond race between the
			// interrupt-instant snapshot and this call).
			if err := terminate(child); err != nil && !errors.Is(err, os.ErrProcessDone) {
				fmt.Fprintf(c.ErrOut, "kyacode: TERM rung not delivered: %v\n", err)
			}
			// Do not wait for the child here — StartChild's cmd.Wait does
			// that; this only accelerates it. The force rung is a TREE
			// kill: a child ignoring INT+TERM ends here with the tree it
			// spawned, targeting the interrupt-time snapshot (deliberately
			// not re-read mid-window — the window is too short for a
			// late Start to matter and re-reading would chase a moving
			// target; the exit-grace re-read below is the H-3 guard).
			select {
			case err := <-errCh:
				return report(err)
			case <-time.After(c.TermWait):
				forceKill(c.ErrOut, child.Pid)
			}
		}
		// Exit-grace window: give the work goroutine's deferred cleanup
		// (the sandbox reclaim) one bounded chance to run to completion.
		select {
		case err := <-errCh:
			return report(err)
		case <-time.After(c.ExitGrace):
			// H-3, now structural: re-read the published record and force
			// THAT child — the joint record never un-publishes, so the
			// re-read is either the same child (published pre-interrupt)
			// or a child that landed late (published mid-grace); the
			// re-read alone decides, and the force runs strictly before
			// the reclaim (pin-tested by an ordered log).
			childNow, cleanupNow := h.Latest()
			if childNow != nil {
				forceKill(c.ErrOut, childNow.Pid)
			}
			if cleanupNow != nil {
				cleanupNow()
			}
			fmt.Fprintln(c.ErrOut, "interrupt: forcing exit")
			return Exit{Code: 1}
		}
	}
}

// report is the honesty contract: the child's own outcome wins, even
// after an interruption never — a clean exit during grace is Exit{0,nil}
// (M-5/M-12), a failing one its real error.
func report(err error) Exit {
	if err != nil {
		return Exit{Code: 1, Fail: err}
	}
	return Exit{Code: 0}
}
