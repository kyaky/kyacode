package hygiene

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The script-execution-environment atoms: the machinery every executable-
// contract harness in this package shares. Each atom moved verbatim from
// its duplicating site (installer_test.go / readme_test.go) — the payloads
// are stamped, not retyped.

// unameStub answers only -s and -m, what install.sh and the README fences
// ask; the * branch is the drift alarm for a future flag.
const unameStub = "#!/bin/sh\ncase \"$1\" in -s) echo \"$KYA_FAKE_OS\";; -m) echo \"$KYA_FAKE_ARCH\";; *) echo \"uname: unsupported flag\" >&2; exit 1;; esac\n"

// writePathWrapper writes the msys cygpath wrapper named name into dir
// (mode 0o755) and returns its path. Two caller-supplied literal holes:
// pathLine is the full PATH-export line (append vs stub-only restrict),
// execTail the wrapper's final command(s), possibly multi-line. This is
// the only code in the package that assembles wrapper text, so the
// cygpath atom cannot fork again.
func writePathWrapper(t *testing.T, dir, name, pathLine, execTail string) string {
	t.Helper()
	wrapper := "#!/bin/sh\n" +
		"stub=\"$1\"\n" +
		"command -v cygpath >/dev/null 2>&1 && stub=\"$(cygpath -u -- \"$stub\")\"\n" +
		pathLine + "\n" +
		"shift\n" +
		execTail + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// sandboxedHome returns a TempDir HOME with the sentinel binary preloaded
// at .local/bin/kyacode — every caller starts with a working install, so
// a failure-state test can assert the old binary survived.
func sandboxedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kyacode"), sentinelBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

// posixify converts a Windows-form path to the slash form an msys child
// expects; a no-op on POSIX hosts.
func posixify(p string) string { return strings.ReplaceAll(p, "\\", "/") }

const (
	// pathLineAppend prepends the stub dir to the host PATH.
	pathLineAppend = `export PATH="$stub:$PATH"`
	// pathLineRestrict makes the PATH stub-ONLY: nothing outside the stub
	// dir resolves, which is why execTailPathOnly must use the absolute
	// /bin/sh — a bare `sh` cannot be found.
	pathLineRestrict = `export PATH="$stub"`
)

const (
	// execTailInstallerScript execs the script file (plus forwarded args)
	// with the host's /bin/sh — dash on the ubuntu CI leg, so the
	// dash-clean claim is exercised, not just asserted.
	execTailInstallerScript = "script=\"$1\"\nshift\nexec sh \"$script\" \"$@\""
	// execTailReadmeFence execs the fence body through bash -c.
	execTailReadmeFence = `exec bash -c "$1"`
	// execTailPathOnly execs the script through the absolute /bin/sh —
	// the stub-only PATH cannot resolve a bare `sh`.
	execTailPathOnly = `exec /bin/sh "$1"`
)
