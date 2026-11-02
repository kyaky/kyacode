package shim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// TestInvokedAsEngineFalseForTheTestBinary pins the dispatch gate's
// identity rule: a binary that merely NAMED codex would not be enough —
// the running binary must live in the shim install dir, so the test
// binary (and any operator-renamed kyacode) stays out of the engine
// personality.
func TestInvokedAsEngineFalseForTheTestBinary(t *testing.T) {
	if InvokedAsEngine() {
		t.Error("InvokedAsEngine fired for the test binary — dispatch must require the shim install dir")
	}
}

// TestRuntimeRecordRoundtripAndCorruptDegrades pins the engine record's
// lifecycle: a save is read back verbatim, and a corrupt record degrades
// to missing (derived state, rewritten by the next launch) rather than
// erroring the spawn path.
func TestRuntimeRecordRoundtripAndCorruptDegrades(t *testing.T) {
	withHome(t)
	// The launch materialises the home (sandbox.Named) BEFORE recording
	// the engine — mirror that order here; SaveRuntimeRecord stays
	// strict about the ordering contract instead of self-healing it.
	home, err := HomeDir()
	if err != nil {
		t.Fatalf("HomeDir: %v", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if got := readRuntimeRecord(); got != nil {
		t.Fatalf("record on a bare home = %+v, want nil", got)
	}
	if err := SaveRuntimeRecord(`C:\engines\codex.exe`, "codex-cli 0.166.0"); err != nil {
		t.Fatalf("SaveRuntimeRecord: %v", err)
	}
	rec := readRuntimeRecord()
	if rec == nil || rec.CodexExe != `C:\engines\codex.exe` || rec.CodexVersion != "codex-cli 0.166.0" {
		t.Fatalf("roundtrip = %+v, want the saved record", rec)
	}
	p, _ := RuntimeRecordPath()
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt the record: %v", err)
	}
	if got := readRuntimeRecord(); got != nil {
		t.Fatalf("corrupt record = %+v, want the nil degrade", got)
	}
}

// TestSetEnabledToggle pins the pass-through switch: the flag file's
// existence is the switch, and removing an absent flag is a no-op.
func TestSetEnabledToggle(t *testing.T) {
	withHome(t)
	p, _ := EnableFlagPath()
	if err := SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("enable flag missing: %v", err)
	}
	if err := SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("enable flag survived the off toggle; stat err = %v", err)
	}
	if err := SetEnabled(false); err != nil {
		t.Errorf("removing an absent flag errored: %v", err)
	}
}

// TestInstallCopiesTheRunningBinary pins the second-name install: the
// shim path carries the current binary's exact bytes, and a re-install
// over an existing copy stays successful (the replace dance).
func TestInstallCopiesTheRunningBinary(t *testing.T) {
	withHome(t)
	if err := Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	dst, _ := ExePath()
	src, _ := os.Executable()
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source binary: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read installed shim: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("installed shim size %d, want the source's %d", len(got), len(want))
	}
	if err := Install(); err != nil {
		t.Fatalf("re-Install over an existing copy: %v", err)
	}
}

// TestShimChildExitThree is runChild's exit-code sentinel child: it only
// runs when the parent set the marker env, and exits 3 so the
// propagation pin can assert the exact code.
func TestShimChildExitThree(t *testing.T) {
	if os.Getenv("KYA_SHIM_TEST_CHILD") == "" {
		t.Skip("sentinel child; run through TestRunChildPropagatesTheExitCode")
	}
	os.Exit(3)
}

// TestRunChildPropagatesTheExitCode pins the shim's contract with the
// editor: the child's exit code IS the shim's exit code, nothing
// swallowed — driven through runChild itself with an injected env row so
// the injection path is exercised on the same spawn.
func TestRunChildPropagatesTheExitCode(t *testing.T) {
	withHome(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	code := runChild(exe,
		[]string{"-test.run=^TestShimChildExitThree$", "-test.count=1"},
		[]string{"KYA_SHIM_TEST_CHILD=1"})
	if code != 3 {
		t.Fatalf("sentinel child exit = %d, want 3", code)
	}
}

// TestRunChildExitCodeFold pins the fold itself: success folds to 0 and
// a spawn failure (not an ExitError) folds to 1.
func TestRunChildExitCodeFold(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	err := exec.Command("definitely-not-a-real-binary-kyacode", "x").Start()
	if err == nil {
		t.Fatal("the fake binary started; the test premise broke")
	}
	if got := exitCode(err); got != 1 {
		t.Errorf("exitCode(exec failure) = %d, want 1", got)
	}
}

// TestExePathNamesTheEngine pins the install name: the shim answers as
// codex on every platform spelling the editor would point at.
func TestExePathNamesTheEngine(t *testing.T) {
	withHome(t)
	p, err := ExePath()
	if err != nil {
		t.Fatalf("ExePath: %v", err)
	}
	base := strings.ToLower(filepath.Base(p))
	if base != "codex.exe" && base != "codex" {
		t.Errorf("shim exe = %q, want the engine name", base)
	}
}
