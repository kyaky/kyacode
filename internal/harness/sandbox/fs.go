package sandbox

import (
	"fmt"
	"os"

	"kyacode/internal/rootdir"
)

// writePrivate writes data with permissions independent of umask: create
// with mode 0 then chmod up, so the file is never even briefly wider than
// 0600 — the same create-at-mode-0 discipline internal/config's saveAtomic
// establishes for ~/.kyacode files. The duplicated shape (instead of one
// shared helper) is deliberate: sandbox files are born and die inside a
// fresh per-launch dir nobody else reads while they are being written, so
// the no-rename write here is sufficient, and the harness library
// deliberately carries no import of the CLI-facing config package for a
// file utility.
func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// rootDir resolves the ~/.kyacode root from rootdir (the single owner
// of the dir's name) and applies the launch-side creation policy on top:
// create-if-missing 0700 with existing-mode conservation, plus the
// fail-closed writability probe. The probe is deliberately launch-side —
// a key-bearing sandbox must never land in an unwritable tree, while
// read-only CLI commands must keep serving one; rootdir's package doc
// states the split and the 0300-vs-0500 permission classes.
func rootDir() (string, error) {
	p, err := rootdir.Path()
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDir(p); err != nil {
		return "", err
	}
	return p, nil
}

// ensurePrivateDir creates dir (0700 irrespective of umask). An EXISTING
// dir is never chmodded — narrowing would fight a legitimate mode, and
// widening one would violate a user's intentional read-only setup.
// Instead writability is probed and the failure propagated, so callers
// never half-build a sandbox. (Conserving an intentionally read-only
// ~/.kyacode is the deliberate behavior; a TestCodexBuildEnvCleansUp pin
// depends on it.)
func ensurePrivateDir(dir string) error {
	fresh := false
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fresh = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fresh {
		if err := os.Chmod(dir, 0o700); err != nil { // neutralize umask
			return err
		}
	}
	// The probe must be UNIQUE per call: concurrent BuildEnv calls probe
	// the same ~/.kyacode against one HOME, and a shared sentinel name
	// races on Windows as a sharing violation (an open handle blocks the
	// other probe's remove) or as ENOENT (removed between open and
	// remove) — both false "not writable" failures (the probe race the
	// concurrent-launch stress test pins).
	probe, err := os.CreateTemp(dir, ".kyacode-write-probe-")
	if err != nil {
		return fmt.Errorf("dir %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	// Tolerate a no-longer-present probe (unknown cleaner still counts
	// as writability proof); keep the real-remove diagnosis.
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
