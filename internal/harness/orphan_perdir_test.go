package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSweepOrphansKeepsGoingWhenOneSandboxCannotBeRemoved pins that one
// stuck orphan does not strand the others (and does not abort the sweep
// silently, either): the healthy dir is removed, the stuck dir survives,
// and the returned error carries the stuck dir's path. POSIX-DAC fixture —
// chmod on Windows is the read-only bit, so the unwritable-subdir shape is
// unix-only here; Windows-wide loudness is pinned by the scan-error
// contract (orphan_scan_test.go) and cmdStatus wiring.
func TestSweepOrphansKeepsGoingWhenOneSandboxCannotBeRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("0500-subdir enforcement is posix-DAC only")
	}
	home := withHome(t)
	kya := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(kya, 0o700); err != nil {
		t.Fatal(err)
	}

	// Healthy: an old sandbox with no obstacles.
	healthy := filepath.Join(kya, "codex-11-1111")
	if err := os.Mkdir(healthy, 0o700); err != nil {
		t.Fatal(err)
	}
	// Stuck: an old sandbox holding a non-empty 0500 subdir. Non-empty is
	// load-bearing — an EMPTY 0500 subdir is rmdir-able from a writable
	// parent, so it would not pin the failure.
	stuck := filepath.Join(kya, "codex-22-2222")
	stuckChild := filepath.Join(stuck, "child")
	if err := os.MkdirAll(stuckChild, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuckChild, "holding"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stuckChild, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(stuckChild, 0o700) // let teardown reclaim the fixture

	for _, dir := range []string{healthy, stuck} {
		stamp := time.Now().Add(-25 * time.Hour)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := SweepOrphans()

	if len(removed) != 1 || removed[0] != healthy {
		t.Errorf("removed = %v, want [%s] (only the healthy dir)", removed, healthy)
	}
	if err == nil {
		t.Fatal("SweepOrphans must report the per-dir remove failure as an error")
	}
	if !strings.Contains(err.Error(), stuck) {
		t.Errorf("error %q must name the stuck dir %s", err, stuck)
	}
	if _, statErr := os.Stat(stuck); statErr != nil {
		t.Errorf("stuck dir must survive the sweep: %v", statErr)
	}
}
