package rootdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// withHome isolates a test in its own fake $HOME so the resolved root
// stays hermetic. USERPROFILE is set to the same dir: on Windows
// os.UserHomeDir reads USERPROFILE and ignores HOME.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// TestPathJoinsHome is the value pin for the literal move (ticket 02):
// Path() returns exactly <home>/.kyacode — the proof that handing the
// name to one owner changed nothing, since every existing fixture
// builds its own home+".kyacode" and keeps matching.
func TestPathJoinsHome(t *testing.T) {
	home := withHome(t)
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(home, ".kyacode")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

// TestPathFailsClosedWithoutHome pins the resolver's fail-closed edge:
// a process with no resolvable home (HOME and USERPROFILE both empty)
// gets an error, never a default root — key-bearing files must not be
// able to land in an arbitrary dir.
func TestPathFailsClosedWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := Path(); err == nil {
		t.Fatal("Path must fail closed when no home is resolvable")
	}
}

// TestPathCreatesNothing is the keystone pin: resolution performs NO
// filesystem operation — a bare home stays bare. This is what lets the
// CLI config store's write sites add their own creation step (the token
// save, the config save) and the orphan sweep stay strictly read-only on
// top of the same value; any creation or probe creeping into Path reds
// here before it can reach any consumer.
func TestPathCreatesNothing(t *testing.T) {
	withHome(t)
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("Path left side effects: stat %s = %v (want IsNotExist — resolution must create nothing)", got, err)
	}
}

// TestPathIsModeBlind pins the other half of purity: resolution
// consults no mode and runs no probe — an EXISTING root, even in the
// unwritable class the launch-side probe refuses (POSIX 0500: r--x),
// resolves without complaint. Enforcing a creation policy is each
// consumer's job (see the package doc); a resolver that refused it
// would brick the read-only CLI boots the 0500 class exists to allow.
// Windows maps chmod to the read-only attribute, whose DAC class
// differs — the mode-blindness contract is the platform-invariant one.
func TestPathIsModeBlind(t *testing.T) {
	withHome(t)
	root, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	m := os.FileMode(0o500)
	if runtime.GOOS == "windows" {
		m = 0o444
	}
	if err := os.Chmod(root, m); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	got, err := Path()
	if err != nil {
		t.Fatalf("Path over an existing mode-restricted root = %v; resolution is mode-blind, policy is the consumer's", err)
	}
	if got != root {
		t.Fatalf("Path = %q, want %q", got, root)
	}
}
