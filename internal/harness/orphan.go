// Package harness — orphanSweep reclaims sandbox dirs left behind by paths
// where the deferred cleanup could not run. SIGINT/SIGTERM/SIGHUP are all
// handled in-process (main's escalation ladder); what the sweep covers is
// the residue after SIGKILL or a second-Ctrl-C exit that skipped the
// deferred cleanup. A surviving sandbox holds the real key, so the sweep
// runs with `kyacode status` — the only caller: launches do not sweep.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"kyacode/internal/harness/sandbox"
	"kyacode/internal/rootdir"
)

// orphanMaxAge: sandbox mtime older than this is safe to remove. There is
// deliberately no PID-liveness check (Windows lacks a kill -0 equivalent
// in the stdlib, and a recorded PID could be reused by another process) —
// the age bound is the only guard. Accepted risk: a single launch that
// runs longer than 24h without touching its sandbox would be swept by a
// concurrent `kyacode status`; sandbox dirs are written once at build
// time, so their mtime never refreshes to protect them.
const orphanMaxAge = 24 * time.Hour

// sandboxPrefixes derives the sweep's match list from the runner registry
// (every declared SandboxPrefix among the WiringSandbox specs), so
// registering a new sandbox runner cannot orphan the sweep: the old
// hand-maintained copy drifted the moment a runner's prefix changed
// without a second edit here. Runners() is name-sorted, so the derivation
// output is deterministic without an extra sort pass.
var sandboxPrefixes = sync.OnceValue(func() []string {
	var out []string
	for _, s := range Runners() {
		if s.Wiring == WiringSandbox {
			out = append(out, s.SandboxPrefix)
		}
	}
	return out
})

// sandboxShapes memoizes the per-prefix matchers — compiled by the
// sandbox module's NameShape, the grammar's single owner (the sweep
// re-derives nothing; it consumes the predicate). Registry order is fixed
// at first call, so compiling eagerly inside the OnceValue is safe and
// read paths need no lock.
var sandboxShapes = sync.OnceValue(func() map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp, len(sandboxPrefixes()))
	for _, prefix := range sandboxPrefixes() {
		out[prefix] = sandbox.NameShape(prefix)
	}
	return out
})

// SweepOrphans removes ~/.kyacode/<prefix>-<pid>-<random> dirs that are
// older than orphanMaxAge. PID liveness is not checked portably (Windows
// lacks a kill -0 equivalent in stdlib); the age bound is the guard.
// Returns the list of removed dirs; per-dir remove failures are reported
// on stderr and collected in the returned error without aborting the
// sweep. A scanned-but-unreadable ~/.kyacode is a scan error.
func SweepOrphans() ([]string, error) {
	// The scan root comes from rootdir (the single owner of the dir's
	// name); the sweep applies its read-only policy on top — it never
	// creates or probes (rootdir's package doc states the split).
	d, err := rootdir.Path()
	if err != nil {
		return nil, err
	}
	// Classify "absent" via Stat, not ReadDir's error class: on Windows,
	// ReadDir over a REGULAR FILE is IsNotExist-classified, which would
	// silently swallow the ~/.kyacode-is-not-a-directory scan failure.
	if _, err := os.Stat(d); os.IsNotExist(err) {
		// Read-only resolution: the sweep never creates ~/.kyacode — a
		// bare home has nothing to sweep, not a setup job to do. (No
		// read-only command creates the root either since the config
		// reads went pure: only the token/config saves do.)
		return nil, nil
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil, fmt.Errorf("orphan sweep: scan %s: %w", d, err)
	}
	var removed []string
	var errs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !isSandboxName(name) {
			continue
		}
		path := filepath.Join(d, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < orphanMaxAge {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			// The per-dir failure rides the returned error alone — the
			// status command renders it (no second, unseamed stderr
			// channel: the report goes out exactly once).
			errs = append(errs, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		removed = append(removed, path)
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("orphan sweep: could not remove %d orphan(s): %s",
			len(errs), strings.Join(errs, "; "))
	}
	return removed, nil
}

// isSandboxName asks the sandbox module — the grammar's single owner —
// whether name is a sandbox dir one of the registry-derived scan anchors
// matches (the exact os.MkdirTemp shape: <prefix>-<pid>-<decimal rand>;
// see sandbox.NameShape for the grammar and its stdlib coupling). The
// sweep keeps the policy: the age bound, the read-only resolution, the
// loud per-dir failures.
func isSandboxName(name string) bool {
	for _, re := range sandboxShapes() {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}
