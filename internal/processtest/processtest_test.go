package processtest

import (
	"strings"
	"testing"
)

// The watcher/poller seam is test-support for real-process proofs, but
// its own contract gets a proof too: the marker detection (split across
// Write calls, case, near-misses). The early-exit and timeout branches
// of AwaitReady are exercised by their real consumers — the procctl and
// ladder sentinel suites red into them when a child misbehaves.

func TestReadyWatchSeesMarkerAcrossWrites(t *testing.T) {
	var w ReadyWatch
	if w.Ready() {
		t.Fatal("fresh watcher is ready before the marker")
	}
	n, err := w.Write([]byte("noise\n"))
	if n != 6 || err != nil {
		t.Fatalf("Write contract: (%d,%v), want (6,nil) — the exec pump must not stall", n, err)
	}
	if _, err := w.Write([]byte("pre " + ArmingMarker + " post\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !w.Ready() {
		t.Fatal("watcher missed the marker embedded in a line")
	}
}

func TestReadyWatchIgnoresNearMisses(t *testing.T) {
	var w ReadyWatch
	_, _ = w.Write([]byte(strings.ToLower(ArmingMarker) + "\n"))
	_, _ = w.Write([]byte("KYA_SENTINEL\nREADY\n"))
	if w.Ready() {
		t.Fatal("watcher armed on a near-miss — the marker is case- and spelling-exact")
	}
}
