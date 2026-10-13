// Package rootdir owns the name of kyacode's root dir (~/.kyacode) and
// its pure path resolution. It imports only the standard library — that
// invariant is load-bearing: internal/config, internal/harness, and
// internal/harness/sandbox all take the root from here, so any
// dependency rootdir ever gains is inherited by all three.
//
// Path resolves the root WITHOUT touching the filesystem: it creates
// nothing, chmods nothing, write-probes nothing. Each consumer applies
// its own creation policy on top, and the divergence is deliberate:
//
//   - internal/config (the token + config.json store): creation at the
//     write sites ONLY — the token save and the config save mkdirAll
//     0700 right before the temp-file write they serve, no probe; the
//     read paths (Load, Token, the path getters) resolve purely. A
//     readable-but-unwritable root (the POSIX 0500 class: r--x) must
//     keep serving read-only commands — the probe's fail-closed
//     refusal is a launch-safety property, not a config-read property.
//   - internal/harness/sandbox (key-bearing sandboxes): create-if-
//     missing 0700 with existing-mode conservation, plus a fail-closed
//     per-call write probe — a sandbox holding the real key must never
//     land in an unwritable tree (the 0500-root DAC test family pins
//     it).
//   - The orphan sweep (internal/harness): strictly read-only Stat
//     resolution — the sweep itself never creates or probes (a bare
//     home has nothing to sweep; pinned by the sweep's "does not
//     create" test).
//
// Permission classes, precisely: the probe needs only owner w+x, so a
// 0300 root (--w--x--) PASSES the probe and fails only ReadDir (no r) —
// that is the unreadable-root WARNING contract `status` reports; a 0500
// root (r--x) FAILS the probe while ReadDir passes — the class that
// keeps the probe launch-side-only, since read-only commands must keep
// serving a readable-but-unwritable root.
package rootdir

import (
	"os"
	"path/filepath"
)

// name is the single production definition of the root dir's name. The
// quoted-literal sweep (grep -rn '"\.kyacode"' over non-test files) must
// find exactly this definition; the ".kyacode-write-probe-" sentinel in
// the sandbox package is a probe FILE name, not a path definition.
const name = ".kyacode"

// Path is <home>/.kyacode as resolved by os.UserHomeDir. Pure: no
// creation, no chmod, no write probe, no error wrapping — consumers
// keep their own failure taxonomies.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, name), nil
}
