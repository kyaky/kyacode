package sandbox

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// withHome isolates a test in its own fake $HOME so the sandbox root
// (and its ~/.kyacode parent) stay hermetic. USERPROFILE is set to the
// same dir: os.UserHomeDir prefers it on Windows. (Same shape as the
// harness package's withHome; the sandbox package cannot import it.)
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// TestNewCreatesAWellFormedSandboxRoot pins the root-creation contract:
// the sandbox lands under ~/.kyacode as <prefix>-<pid>-<decimal rand> —
// the exact os.MkdirTemp shape the orphan sweep anchors on — so a
// created dir can never fall outside the sweep's match list. MkdirTemp
// refuses to clobber an existing path, so two launches racing on the
// same PID cannot share — or RemoveAll — each other's live sandbox.
func TestNewCreatesAWellFormedSandboxRoot(t *testing.T) {
	home := withHome(t)
	sb, err := New("codex", Key(EnvRef, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	root := sb.Root()
	if filepath.Dir(root) != filepath.Join(home, ".kyacode") {
		t.Errorf("sandbox root %s is not under the isolated ~/.kyacode", root)
	}
	// The independent shape: prefix + the sweep's anchored pid + decimal
	// random (os/tempfile nextRandom), nothing else.
	name := filepath.Base(root)
	want := fmt.Sprintf(`^codex-%d-\d+$`, os.Getpid())
	if !regexp.MustCompile(want).MatchString(name) {
		t.Errorf("sandbox name %s does not match the MkdirTemp shape %s the sweep anchors on", name, want)
	}
	// Same-module coherence: the exported predicate this package owns
	// (the grammar the orphan sweep consumes) accepts the name New just
	// created — creation and matching share one home.
	if !NameShape("codex").MatchString(name) {
		t.Errorf("sandbox name %s is rejected by this module's own NameShape — creation and the grammar diverged", name)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("sandbox root missing on disk: %v", err)
	}
}

// TestWriteCreatesParentDirsAndPrivateFiles pins the module's only write
// path: Write creates any missing parent dirs (MkdirAll 0700 — the
// uniform subdir rule; no per-dir probe, the root's fail-closed probe
// already covers the unwritable-home class) and writes the file
// mode-0-then-chmod-0600, so a key-bearing config is never even briefly
// wider than 0600 (H-1). A second Write replaces (truncates), never
// appends.
func TestWriteCreatesParentDirsAndPrivateFiles(t *testing.T) {
	withHome(t)
	sb, err := New("cline", Key(FileOnly, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	const body = `{"openai":{}}`
	if err := sb.Write("data/settings/providers.json", []byte(body)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p := filepath.Join(sb.Root(), "data", "settings", "providers.json")
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != body {
		t.Errorf("file content = %q, want %q", got, body)
	}
	if runtime.GOOS != "windows" { // Windows modes are ACL-shaped; the 0600 discipline is POSIX umask protection
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat written file: %v", err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600 (create-at-0-then-chmod discipline)", fi.Mode().Perm())
		}
	}
	// Re-write truncates: the file holds exactly the new body.
	if err := sb.Write("data/settings/providers.json", []byte("x")); err != nil {
		t.Fatalf("re-Write: %v", err)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != "x" {
		t.Errorf("re-Write content = %q (err %v), want exactly %q", got, err, "x")
	}
}

// TestWriteRefusesPathsThatEscapeTheSandbox pins the fail-closed join:
// a rel that would resolve outside the 0700 sandbox root (a traversal,
// or an absolute path) is refused — a key-bearing file planted outside
// the dir the lifecycle reclaims is exactly the leak the sandbox
// exists to prevent.
func TestWriteRefusesPathsThatEscapeTheSandbox(t *testing.T) {
	withHome(t)
	sb, err := New("pi", Key(Dual, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()
	for _, rel := range []string{"../evil.json", "..", "a/../../evil.json", filepath.Join("..", "x.json")} {
		if err := sb.Write(rel, []byte("key")); err == nil {
			t.Errorf("Write(%q) succeeded; escaping the sandbox root must be refused", rel)
		}
	}
	// An absolute rel is never sandbox-relative (the root itself is
	// absolute on every platform, so this case is platform-stable).
	if err := sb.Write(filepath.Join(sb.Root(), "evil.json"), []byte("key")); err == nil {
		t.Error("Write(absolute path) succeeded; only sandbox-relative paths are legal")
	}
}

// TestCredentialsEmitTheDeclaredChannelRows pins the key-channel
// contract (H-NEW2 by construction): EnvRef and Dual export exactly one
// credential row (KeyEnvName=value, assembled here — never in a runner
// body), and FileOnly exports nothing — a FileOnly runner cannot leak a
// credential env row because there is no row for it to emit.
func TestCredentialsEmitTheDeclaredChannelRows(t *testing.T) {
	withHome(t)
	mk := func(c Channel) *Sandbox {
		sb, err := New("x", Key(c, "sk-test-12345"))
		if err != nil {
			t.Fatalf("New(%v): %v", c, err)
		}
		t.Cleanup(sb.Cleanup)
		return sb
	}
	if rows := mk(FileOnly).Credentials(); len(rows) != 0 {
		t.Errorf("FileOnly Credentials = %v, want no row (the key stays in the sandbox file)", rows)
	}
	want := []string{KeyEnvName + "=sk-test-12345"}
	if rows := mk(EnvRef).Credentials(); !reflect.DeepEqual(rows, want) {
		t.Errorf("EnvRef Credentials = %v, want exactly %v", rows, want)
	}
	if rows := mk(Dual).Credentials(); !reflect.DeepEqual(rows, want) {
		t.Errorf("Dual Credentials = %v, want exactly %v", rows, want)
	}
}

// TestHomeTriadEmitsTheWindowsRedirectOnce pins the home redirect the
// HOME-replacing runners need, at the one place it is now emitted: on
// Windows, os.homedir() and the .NET/Node home lookups read USERPROFILE
// / HOMEDRIVE+HOMEPATH, not HOME — so HOME=sandbox alone leaves the
// harness writing to the real user profile (with the key embedded
// there). Contract: exactly three rows in the frozen order, USERPROFILE
// pointing at the sandbox root, and HOMEDRIVE+HOMEPATH re-composing that
// root exactly. Off-Windows there is no redirect to emit. (Consolidates
// the per-runner Windows legs ticket 01 Q6(d) deletes.)
func TestHomeTriadEmitsTheWindowsRedirectOnce(t *testing.T) {
	withHome(t)
	sb, err := New("droid", Key(Dual, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(sb.Cleanup)
	root := sb.Root()

	if runtime.GOOS != "windows" {
		if triad := sb.HomeTriad(); len(triad) != 0 {
			t.Fatalf("off-Windows HomeTriad = %v, want no rows (HOME needs no redirect)", triad)
		}
		return
	}
	triad := sb.HomeTriad()
	if len(triad) != 3 {
		t.Fatalf("HomeTriad = %v, want exactly the USERPROFILE/HOMEDRIVE/HOMEPATH rows", triad)
	}
	if triad[0] != "USERPROFILE="+root {
		t.Errorf("USERPROFILE row = %q, want the sandbox root %q", triad[0], root)
	}
	driveName, drive, ok := strings.Cut(triad[1], "=")
	if !ok || driveName != "HOMEDRIVE" || drive == "" {
		t.Errorf("row 1 = %q, want a non-empty HOMEDRIVE", triad[1])
	}
	pathName, path, _ := strings.Cut(triad[2], "=")
	if pathName != "HOMEPATH" {
		t.Errorf("row 2 = %q, want a HOMEPATH", triad[2])
	}
	if drive+path != root {
		t.Errorf("HOMEDRIVE+HOMEPATH = %q, want the sandbox root %q re-composed exactly", drive+path, root)
	}
}

// TestCleanupReclaimsTheSandbox pins the reclamation contract: Cleanup
// removes the whole tree (root plus subdirs plus the key-bearing files),
// and a second Cleanup is a silent no-op — the launch path can invoke
// the same cleanup func from the interrupt ladder AND the deferred
// post-child-exit path (main's H-3 publish window), so idempotency is
// part of the frozen handle contract.
func TestCleanupReclaimsTheSandbox(t *testing.T) {
	withHome(t)
	sb, err := New("omp", Key(FileOnly, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sb.Write("agent/models.yml", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	root := sb.Root()
	sb.Cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Cleanup left the sandbox tree behind (stat err = %v)", err)
	}
	sb.Cleanup() // idempotent no-op: must not panic, re-fail, or re-log
}

// TestCleanupRetryExhaustionWarnsLoudly is the NEW key-safety pin
// (ticket 01 Q6(e)): when reclamation keeps failing — the just-exited
// child or an AV/indexer holding a file open — Cleanup retries briefly,
// then fails LOUDLY on stderr with the "sandbox cleanup failed (holds
// key)" line. Today that last loud key-safety line had no test; a
// regression that made exhaustion silent would leave the real key on
// disk with no trace. The fixture makes the dir persistently
// unremovable (a non-empty read-only subdir): POSIX-DAC only — on
// Windows the read-only bit is not enforcement (RemoveAll clears it and
// retries), and root bypasses DAC entirely.
func TestCleanupRetryExhaustionWarnsLoudly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only dir enforcement is posix-DAC only; RemoveAll on Windows clears the bit and succeeds")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; read-only dir not enforceable")
	}
	withHome(t)
	sb, err := New("hermes", Key(FileOnly, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Non-empty is load-bearing — an empty read-only subdir is
	// rmdir-able from a writable parent, so it would not pin the failure.
	locked := filepath.Join(sb.Root(), "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "holding"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700) // let t.TempDir teardown reclaim the fixture
		_ = os.RemoveAll(filepath.Dir(locked))
	})

	captured := captureStderr(t, func() {
		sb.Cleanup()
		sb.Cleanup() // idempotent: the exhaustion warning must print exactly once
	})
	if !strings.Contains(captured, "sandbox cleanup failed (holds key)") {
		t.Errorf("exhausted Cleanup stayed silent — the surviving sandbox holds the real key, the warning must be loud:\n%s", captured)
	}
	if n := strings.Count(captured, "sandbox cleanup failed (holds key)"); n != 1 {
		t.Errorf("warning printed %d times across two Cleanups; idempotency means exactly once", n)
	}
	// The fixture must have BLOCKED reclamation: after exhaustion the
	// sandbox still exists. (CI run 34090775921: the inverted check
	// fired on the expected outcome — the warning had in fact been
	// printed exactly once and the dir had survived, exactly as
	// designed.)
	if _, err := os.Stat(sb.Root()); os.IsNotExist(err) {
		t.Errorf("fixture did not block reclamation: the sandbox is gone (stat err = %v), so the exhaustion path never ran", err)
	}
}

// captureStderr runs f with os.Stderr redirected to a pipe and returns
// everything f wrote there. Single-goroutine by construction (the test
// process's only writer during the capture is f), so no synchronization
// is needed — the same fixture discipline as the CLI tests, which
// capture the child's stderr out-of-process.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr capture pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	func() {
		defer func() { os.Stderr = old }()
		f()
		_ = w.Close() // EOF for the read below
	}()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(b)
}

// TestNewConservesAnExistingRootMode is the POSITIVE mode-conservation
// pin (ticket 02 graft): an existing root the owner can write but that
// is not 0700 (fixture: 0750) must be conserved — the creation policy
// normalizes only fresh roots, and an existing one is kept as-is under
// the never-chmod-an-existing-root rule (ensurePrivateDir states both
// halves: narrowing would fight a legitimate mode, widening would
// violate an intentional read-only setup). The negative family (0500
// unwritable) pins the refusal; this pin catches a future "normalize to
// 0700" edit. The Chmod after MkdirAll defeats umask on the fixture
// itself. Windows Stat cannot report 0750 dir modes (ACL-shaped), so
// the pin runs on POSIX (the conservation contract is DAC-shaped).
func TestNewConservesAnExistingRootMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows Stat cannot report a 0750 dir mode; conservation is pinned on POSIX")
	}
	home := withHome(t)
	root := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	sb, err := New("pi", Key(Dual, "sk-test-12345"))
	if err != nil {
		t.Fatalf("New over a writable non-0700 root must succeed (existing mode conserved, not normalized): %v", err)
	}
	defer sb.Cleanup()
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o750 {
		t.Errorf("root mode = %o, want 0750 conserved (existing dirs are never chmodded)", got)
	}
}

// TestNewFailsClosedWhenTheRootDirIsNotWritable pins the fail-closed
// writability probe: an unwritable ~/.kyacode must fail New before any
// sandbox dir exists (no half-built key-bearing residue), with no
// sandbox handle returned. POSIX-DAC fixture: on Windows Chmod only
// toggles the read-only bit, and root bypasses DAC entirely.
func TestNewFailsClosedWhenTheRootDirIsNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only dir enforcement is posix-DAC only")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; read-only dir not enforceable")
	}
	home := withHome(t)
	kya := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(kya, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(kya, 0o700)

	if _, err := New("codex", Key(EnvRef, "sk-test-12345")); err == nil {
		t.Fatal("New must fail when ~/.kyacode is not writable; no handle may come back")
	}
	matches, _ := filepath.Glob(filepath.Join(kya, "codex-*"))
	if len(matches) != 0 {
		t.Fatalf("failed New left sandbox residue (holds the real key): %v", matches)
	}
}
