package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestNamedPersistsAcrossCleanup pins the persistent-home lifecycle: a
// Named sandbox's Cleanup is a no-op, so the launch's deferred reclaim
// can never take the home down (its removal is the operator's).
func TestNamedPersistsAcrossCleanup(t *testing.T) {
	testHome(t)
	sb, err := Named("codex-gui")
	if err != nil {
		t.Fatalf("Named: %v", err)
	}
	if err := sb.Write("config.toml", []byte("x = 1\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sb.Cleanup()
	sb.Cleanup() // idempotent, still nothing removed
	if _, err := os.Stat(filepath.Join(sb.Root(), "config.toml")); err != nil {
		t.Fatalf("persistent home did not survive cleanup: %v", err)
	}
}

// TestNamedRejectsInvalidNames pins the name grammar at the only place a
// Named name enters the tree: single lowercase segment, no separators,
// and never a pid-random-shaped tail (the sweep-collision guard).
func TestNamedRejectsInvalidNames(t *testing.T) {
	testHome(t)
	for _, name := range []string{"", "Codex", "a/b", `a\b`, ".x", "x y", "codex-1-2", "x-12-345", strings.Repeat("a", 65)} {
		if _, err := Named(name); err == nil {
			t.Errorf("Named(%q) succeeded; want refusal", name)
		}
	}
	if _, err := Named("codex-gui"); err != nil {
		t.Errorf("Named(codex-gui): %v", err)
	}
}

// TestNamedIsInvisibleToTheSweepGrammar pins the structural persistence
// guarantee at the grammar level: a fixed-name home can never match a
// registered prefix's pid-random matcher, whatever prefixes the registry
// declares.
func TestNamedIsInvisibleToTheSweepGrammar(t *testing.T) {
	if NameShape("codex").MatchString("codex-gui") {
		t.Error("the fixed home name matched the pid-random sweep grammar")
	}
	if NameShape("codexgui").MatchString("codex-gui") {
		t.Error("the codexgui anchor matched the fixed home name")
	}
}

// TestLeaseAcquireReleaseReacquire pins the lease's basic lifecycle: a
// released lease is immediately re-acquirable.
func TestLeaseAcquireReleaseReacquire(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLease(home)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, err := AcquireLease(home)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	l2.Release()
}

// TestLeaseSecondAcquireRefuses pins the single-server rule: a held
// lease refuses a second acquirer instead of queuing, and frees on
// release.
func TestLeaseSecondAcquireRefuses(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLease(home)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if _, err := AcquireLease(home); err == nil {
		t.Error("second acquire succeeded while the lease was held")
	}
	held, err := LeaseHeld(home)
	if err != nil || !held {
		t.Errorf("LeaseHeld = %v, %v; want true, nil", held, err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	held, err = LeaseHeld(home)
	if err != nil || held {
		t.Errorf("LeaseHeld after release = %v, %v; want false, nil", held, err)
	}
}

// TestLeaseProbeCreatesNothing pins the probe's read-only policy: a
// never-launched home probes free without materialising the lease file
// (the orphan sweep's "never creates" discipline, applied to the probe).
func TestLeaseProbeCreatesNothing(t *testing.T) {
	home := t.TempDir()
	held, err := LeaseHeld(home)
	if err != nil || held {
		t.Fatalf("LeaseHeld on a bare dir = %v, %v; want false, nil", held, err)
	}
	if _, err := os.Stat(filepath.Join(home, leaseName)); !os.IsNotExist(err) {
		t.Errorf("the probe created the lease file; stat err = %v", err)
	}
}
