package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kyacode/internal/harness/sandbox"
)

// TestSweepOrphansRemovesOnlyWellFormedSandboxDirs pins the sweep's match
// shape: a sandbox dir is exactly `<prefix>-<pid>-<random>` as os.MkdirTemp
// produces it. Prefix-only matching would put any user-named directory
// sharing a prefix (codex-archive, pi-notes, codex-1-backup) in the blast
// radius of `kyacode status` — and the sweep removes key-bearing dirs, so
// the matcher must be strict even though MkdirTemp's random suffix is
// decimal-digits-only (os/tempfile nextRandom). Fixture dirs are backdated
// past orphanMaxAge so only the shape decides.
func TestSweepOrphansRemovesOnlyWellFormedSandboxDirs(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}

	t.Run("removes a 25h-old real codex sandbox built through BuildEnv", func(t *testing.T) {
		home := withHome(t)
		w, err := buildCodexWirePair(t, ep, "m1")
		if err != nil {
			t.Fatalf("BuildEnv: %v", err)
		}
		defer w.Cleanup() // no-op if the sweep already removed it
		dir := w.Root
		backdate(t, dir, 25*time.Hour)

		removed, _ := SweepOrphans()
		if len(removed) != 1 || removed[0] != dir {
			t.Fatalf("SweepOrphans = %v, want [%s]", removed, dir)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("sandbox dir still exists after sweep: %v", err)
		}
		_ = home
	})

	t.Run("keeps a fresh well-formed sandbox dir", func(t *testing.T) {
		_ = withHome(t)
		w, err := buildCodexWirePair(t, ep, "m1")
		if err != nil {
			t.Fatalf("BuildEnv: %v", err)
		}
		defer w.Cleanup()
		dir := w.Root
		backdate(t, dir, time.Hour) // well under orphanMaxAge

		removed, _ := SweepOrphans()
		if len(removed) != 0 {
			t.Errorf("fresh sandbox must survive the sweep, removed: %v", removed)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("fresh sandbox dir vanished: %v", err)
		}
	})

	for _, name := range []string{"codex-archive", "pi-notes", "codex-1-backup"} {
		t.Run("keeps a 25h-old foreign dir "+name, func(t *testing.T) {
			home := withHome(t)
			kya := filepath.Join(home, ".kyacode")
			if err := os.MkdirAll(kya, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(kya, name)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			backdate(t, dir, 25*time.Hour)

			removed, _ := SweepOrphans()
			if len(removed) != 0 {
				t.Errorf("user dir %s must never be swept, removed: %v", dir, removed)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("user dir vanished under the sweep: %v", err)
			}
		})
	}

	t.Run("keeps a 25h-old plain file with a sandbox-shaped name", func(t *testing.T) {
		home := withHome(t)
		kya := filepath.Join(home, ".kyacode")
		if err := os.MkdirAll(kya, 0o700); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(kya, "codex-123-456")
		if err := os.WriteFile(f, []byte("user data"), 0o600); err != nil {
			t.Fatal(err)
		}
		backdate(t, f, 25*time.Hour)

		removed, _ := SweepOrphans()
		if len(removed) != 0 {
			t.Errorf("regular file must never be swept, removed: %v", removed)
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("file vanished under the sweep: %v", err)
		}
	})
}

// TestSandboxShapeSurvivesHostilePrefixes pins sandbox.NameShape's
// impossible-failure contract from the hostile side: prefix text carrying
// regexp metacharacters (what a future careless SandboxPrefix declaration
// could produce) must yield an anchored matcher that behaves as literal text —
// it matches the real os.MkdirTemp shape of that exact prefix and rejects
// everything exotic. The old helper silently DROPPED a prefix whose
// pattern failed to compile — the sweep would go blind to key-bearing
// dirs with no trace — so the quoting (QuoteMeta) that makes compile
// failure unreachable is load-bearing and pinned here.
func TestSandboxShapeSurvivesHostilePrefixes(t *testing.T) {
	for _, prefix := range []string{"codex-(.*)", "codex`", "pi+", "hermes|null", "[dsh]"} {
		re := sandbox.NameShape(prefix)
		if re.MatchString(prefix + "-123-4567890123") {
			continue // literal-match they must — asserted below
		}
		t.Errorf("prefix %q: matcher fails its own MkdirTemp-shaped name %s", prefix, prefix+"-123-4567890123")
	}
	metachar := sandbox.NameShape("codex-(.*)")
	if metachar.MatchString("codex-123-456") {
		t.Error("matcher interpreted the (? ) metacharacters; the prefix must be a quoted literal")
	}
	if metachar.MatchString("codexfoo-123-456") {
		t.Error("matcher treated the group as optional; the literal text must be required")
	}
	backtick := sandbox.NameShape("codex`")
	if backtick.MatchString("codex`-123-456") == false || backtick.MatchString("codexx-123-456") {
		t.Error("backtick prefix is not matched as exact literal text")
	}
	if sandbox.NameShape("codex-archive").MatchString("codex-archive-1-2") == false {
		t.Error("a plain-text prefix must keep matching its literal shape")
	}
}

// TestEveryRegistryPrefixMatchesItsOwnMkdirTempShape exercises the
// sandbox.NameShape compile path through the whole current registry: each
// registered sandbox prefix — the per-runner constant shared by the
// creation site (sandbox.New) and the scan anchor (SandboxPrefix) — must
// produce a matcher that accepts the exact name os.MkdirTemp generates
// for it (prefix-<pid>-<decimal rand>) and rejects bare and padded
// variants. The BEHAVIORAL half of the prefix contract (a real BuildEnv
// dir is matched by the sweep) is pinned per runner in
// TestRunnerSandboxesMatchTheSweepsOwnNamePredicate.
func TestEveryRegistryPrefixMatchesItsOwnMkdirTempShape(t *testing.T) {
	prefixes := sandboxPrefixes()
	if len(prefixes) == 0 {
		t.Fatal("no sandbox prefixes derived from the registry")
	}
	for _, prefix := range prefixes {
		re := sandbox.NameShape(prefix)
		if !re.MatchString(prefix + "-123-456") {
			t.Errorf("prefix %q does not match its own sandbox shape %s", prefix, prefix+"-123-456")
		}
		if re.MatchString(prefix) || re.MatchString(prefix+"-123-456-extra") {
			t.Errorf("prefix %q matcher is wider than the anchored MkdirTemp shape", prefix)
		}
	}
}

// TestRunnerSandboxesMatchTheSweepsOwnNamePredicate pins the prefix
// contract behaviorally for EVERY registered sandbox-wired spec (ticket 01
// Q4 / Q6(b)): the dir a real BuildEnv creates — read straight off the
// Wire, no env re-derivation — is matched by the sweep's own isSandboxName
// predicate (built from the registry-derived scan anchors). A runner whose
// creation site ever drifts from its declared SandboxPrefix would
// silently hide key-bearing dirs from the orphan sweep — these pins are
// the replacement for the old per-runner comment-only contract. Codex
// additionally has the end-to-end pin above (the sweep itself removes a
// BuildEnv-built sandbox).
func TestRunnerSandboxesMatchTheSweepsOwnNamePredicate(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	for _, s := range sandboxRunners() {
		t.Run(s.Name, func(t *testing.T) {
			withHome(t)
			w := buildWire(t, s, ep, "")
			defer w.Cleanup()
			root := w.Root
			if root == "" {
				t.Fatalf("%s: wire carries no sandbox root", s.Name)
			}
			name := filepath.Base(root)
			if !strings.HasPrefix(name, s.SandboxPrefix+"-") {
				t.Errorf("created dir %s does not carry the declared prefix %q", name, s.SandboxPrefix)
			}
			if !isSandboxName(name) {
				t.Errorf("the sweep's own isSandboxName rejects dir %s that runner %s just created — key-bearing sandboxes would escape the orphan sweep", name, s.Name)
			}
		})
	}
}

// backdate sets a fixture's mtime to now-ago (works on dirs and files on
// Windows and POSIX alike).
func backdate(t *testing.T, path string, ago time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-ago)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("backdate %s: %v", path, err)
	}
}
