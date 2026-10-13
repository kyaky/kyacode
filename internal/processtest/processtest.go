// Package processtest is the one home of the parent-side machinery the
// signal-rung suites share for their re-exec'd sentinel children: the
// arming-marker string both sides speak, the race-safe stdout watcher
// that sees it, and the deadline poller that refuses to proceed (or
// reds with the one shared verdict) until it does. Before this package
// the same watcher existed twice (procctl's windows file and ladder's
// wiring test), the marker string three times inline, and the timeout
// verdict was three character-identical sentences (the two stdout-marker legs
// plus the posix pipe leg) with no owner.
//
// What stays LOCAL on purpose: child bodies are thin -test.run entries
// in their own packages (each selects its role from its own env key — five
// re-exec bodies, three parent pollers),
// and the POSIX procctl leg keeps its readiness PIPE (ExtraFiles fd 3)
// instead of a stdout marker — a different mechanism whose only shared
// thing with the watchers is the verdict sentence, which it now reads
// from here too. Test-support only: production code never imports this
// package.
package processtest

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ArmingMarker is printed by a sentinel child once its signal handler is
// installed. Parents gate every signal delivery on seeing it — a fixed
// sleep would let a slow-starting child take the interrupt under the
// default disposition and false-red the test.
const ArmingMarker = "KYA_SENTINEL_READY"

// ReadyVerdict is the one sentence every stdout-marker poller and the
// posix pipe leg red with when a sentinel never armed.
const ReadyVerdict = "sentinel never signaled handler readiness"

const (
	readyTimeout = 10 * time.Second
	pollInterval = 5 * time.Millisecond
)

// ReadyWatch records whether the arming marker has passed through it.
// Use as the cmd.Stdout target: every Write is mutex-guarded so the
// polling parent never races the exec read-pump goroutine (the
// unsynchronized field read was the -race flare this shape exists to
// avoid).
type ReadyWatch struct {
	mu    sync.Mutex
	ready bool
}

func (w *ReadyWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.Contains(string(p), ArmingMarker) {
		w.ready = true
	}
	return len(p), nil
}

// Ready reports whether the marker has been seen.
func (w *ReadyWatch) Ready() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ready
}

// AwaitReady blocks until w has seen the arming marker. done is the
// child's Wait-result channel and kill runs before every Fatal so a
// failed wait never strands the child — both are required, every real
// consumer has both at hand (an early child exit is itself a red, never
// a silent hang to the deadline). A slow-starting sentinel must not
// fold the test into a signal-death false red — that is the whole
// reason the protocol exists.
func AwaitReady(t *testing.T, w *ReadyWatch, done <-chan error, kill func()) {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for !w.Ready() {
		if time.Now().After(deadline) {
			kill()
			t.Fatal(ReadyVerdict)
		}
		select {
		case <-done:
			kill()
			t.Fatal("sentinel child exited before signaling readiness")
		default:
		}
		time.Sleep(pollInterval)
	}
}
