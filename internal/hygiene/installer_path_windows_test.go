//go:build windows

package hygiene

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// readUserPath reads HKCU\Environment\Path with its raw value (no
// %VAR% expansion) and registry type, reporting whether it existed.
func readUserPath(t *testing.T) (string, uint32, bool) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open HKCU\\Environment: %v", err)
	}
	defer k.Close()
	v, vt, err := k.GetStringValue("Path")
	if err != nil {
		if err == registry.ErrNotExist {
			return "", 0, false
		}
		t.Fatalf("read user Path: %v", err)
	}
	return v, vt, true
}

// restoreUserPath puts HKCU\Environment\Path back byte-exactly — value
// AND type (the installer's [Environment]::SetEnvironmentVariable call
// writes REG_SZ, so a REG_EXPAND_SZ original must be re-stored with its
// own type, not with the installer's write path).
func restoreUserPath(t *testing.T, val string, typ uint32, existed bool) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		t.Fatalf("reopen HKCU\\Environment for restore: %v", err)
	}
	defer k.Close()
	if !existed {
		if err := k.DeleteValue("Path"); err != nil {
			t.Fatalf("restore (delete the added Path): %v", err)
		}
		return
	}
	switch typ {
	case registry.EXPAND_SZ:
		if err := k.SetExpandStringValue("Path", val); err != nil {
			t.Fatalf("restore EXPAND_SZ Path: %v", err)
		}
	case registry.SZ:
		if err := k.SetStringValue("Path", val); err != nil {
			t.Fatalf("restore SZ Path: %v", err)
		}
	default:
		t.Fatalf("unexpected user-Path registry type %d; restore it manually", typ)
	}
}

// countPathEntry counts exact semicolon-separated entries equal to dir.
func countPathEntry(path, dir string) int {
	n := 0
	for _, part := range strings.Split(path, ";") {
		if part == dir {
			n++
		}
	}
	return n
}

// TestPowerShellInstallerPersistsTheUserPathIdempotently runs the REAL
// user-PATH persistence (the one the matrix rows skip via
// KYACODE_SKIP_PATH_PERSIST) against the real HKCU\Environment: one
// install adds the dir exactly once, a re-run adds nothing, and the
// original value+type is restored on cleanup.
func TestPowerShellInstallerPersistsTheUserPathIdempotently(t *testing.T) {
	orig, origType, existed := readUserPath(t)
	t.Cleanup(func() { restoreUserPath(t, orig, origType, existed) })

	asset := "kyacode-windows-amd64.exe"
	fakeBin := []byte("FAKEGO-BINARY\x00\n")
	correct := map[string]assetSpec{
		asset:             {body: fakeBin},
		asset + ".sha256": {body: sidecarFor(fakeBin, asset)},
	}

	dir := t.TempDir()
	res, installDir, _ := runPowerShellInstaller(t, "", correct, []string{"KYACODE_INSTALL_DIR=" + dir})
	if res.exit != 0 {
		t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
	}
	if installDir != dir {
		t.Fatalf("the runner's install dir drifted: %q vs %q", installDir, dir)
	}
	now, _, _ := readUserPath(t)
	if got := countPathEntry(now, dir); got != 1 {
		t.Errorf("after the first install the user PATH carries the dir %d times, want exactly 1", got)
	}
	// A second run must not duplicate the entry (idempotent).
	res, _, _ = runPowerShellInstaller(t, "", correct, []string{"KYACODE_INSTALL_DIR=" + dir})
	if res.exit != 0 {
		t.Fatalf("second run: exit = %d; stderr: %s", res.exit, res.stderr)
	}
	now, _, _ = readUserPath(t)
	if got := countPathEntry(now, dir); got != 1 {
		t.Errorf("after the second install the user PATH carries the dir %d times, want exactly 1 (idempotent)", got)
	}
}
