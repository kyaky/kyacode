//go:build windows

package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCmdlineMatchesMarker pins the quote-tolerant marker match: Electron
// quotes the --user-data-dir value in its own argv, so a literal-quote
// contains would miss every real spawn (observed live before the fix).
func TestCmdlineMatchesMarker(t *testing.T) {
	dir := `C:\Users\u\.kyacode\codex-gui-electron`
	marker := strings.ToLower("--user-data-dir=" + dir)
	yes := []string{
		`"C:\...\ChatGPT.exe" --user-data-dir="` + dir + `" --no-first-run`,
		`"C:\...\ChatGPT.exe" --user-data-dir=` + dir,               // unquoted form
		`"C:\...\ChatGPT.EXE" --USER-DATA-DIR="` + strings.ToUpper(dir) + `"`, // case-insensitive
	}
	for _, line := range yes {
		if !cmdlineMatchesMarker(line, marker) {
			t.Errorf("marker not matched in %q", line)
		}
	}
	no := []string{
		`"C:\...\ChatGPT.exe" --no-first-run --no-pings`,           // everyday instance
		`--user-data-dir=C:\some\other\profile`,                    // a different profile
		`--user-data-dir-prefix=` + dir,                            // a prefix collision must not count
	}
	for _, line := range no {
		if cmdlineMatchesMarker(line, marker) {
			t.Errorf("marker matched in %q", line)
		}
	}
}

// TestDesktopVanillaRuntime pins the host-runtime resolver's contract:
// exactly one codex.exe candidate resolves; zero fails loudly; multiple
// candidates degrade to the newest binary with a warning, never a silent
// guess (the post-upgrade ambiguity case).
func TestDesktopVanillaRuntime(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	bin := filepath.Join(local, "OpenAI", "Codex", "bin")

	// No bin dir at all.
	if _, _, err := desktopVanillaRuntime(); err == nil {
		t.Fatal("resolved a runtime from a missing bin dir")
	}

	// Empty bin dir.
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := desktopVanillaRuntime(); err == nil {
		t.Fatal("resolved a runtime from an empty bin dir")
	}

	// One candidate: the single-candidate rule.
	a := filepath.Join(bin, "aaaa", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(a), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := desktopVanillaRuntime()
	if err != nil || !ok || got != a {
		t.Fatalf("single candidate = (%q, %v, %v), want (%q, true, nil)", got, ok, err, a)
	}

	// Two candidates: the newest binary wins, deterministically even with
	// equal names at different mtimes.
	b := filepath.Join(bin, "bbbb", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(b), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(a, old, old); err != nil {
		t.Fatal(err)
	}
	got, ok, err = desktopVanillaRuntime()
	if err != nil || !ok {
		t.Fatalf("two candidates = (%q, %v, %v), want the newest with no error", got, ok, err)
	}
	if got != b {
		t.Fatalf("two candidates picked %q, want the newest %q", got, b)
	}
}
