package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSweepOrphansReportsAScanFailureInsteadOfNothingToSweep pins the
// error contract: a scan that cannot happen is an ERROR, never a silent
// "nothing to sweep" (a surviving sandbox holds the real key — the CLI
// decides how loud to be, the library must not swallow it). The sweep
// also resolves ~/.kyacode read-only: it creates nothing and write-probes
// nothing, so a bare home reports (nil, nil) with no directory left
// behind, and status never re-races the shared write-probe at fsutil's
// ensurePrivateDir.
func TestSweepOrphansReportsAScanFailureInsteadOfNothingToSweep(t *testing.T) {
	t.Run("does not create ~/.kyacode when it is absent", func(t *testing.T) {
		home := withHome(t)
		kya := filepath.Join(home, ".kyacode")

		removed, err := SweepOrphans()
		if err != nil {
			t.Errorf("SweepOrphans over a bare home = err %v, want nil", err)
		}
		if removed != nil {
			t.Errorf("removed = %v, want nil", removed)
		}
		if _, statErr := os.Stat(kya); !os.IsNotExist(statErr) {
			t.Errorf("sweep must not create %s, stat err = %v", kya, statErr)
		}
	})

	t.Run("returns an error and zero removals when ~/.kyacode is a regular file", func(t *testing.T) {
		home := withHome(t)
		kya := filepath.Join(home, ".kyacode")
		if err := os.WriteFile(kya, []byte("a file, not a dir"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Windows classifies ReadDir-over-a-file as ENOENT-family, so the
		// sweep must judge "absent" by Stat, not by the ReadDir error
		// class — exactly why this fixture is held to err != nil with no
		// ENOTDIR wording asserted.
		removed, err := SweepOrphans()
		if err == nil {
			t.Error("SweepOrphans over a file = nil error, want a scan error")
		}
		if removed != nil {
			t.Errorf("removed = %v, want nil (nothing was scandable)", removed)
		}
	})
}
