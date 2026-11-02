package harness

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kyacode/internal/harness/sandbox"
	"kyacode/internal/shim"
)

// TestCodexGuiBuildWritesTheKeyFreeHome pins the persistent home's
// managed layer byte for byte: config.toml carries the full wiring pair
// set, the catalog renders from the stubbed harvest, the runtime record
// names an engine, and NO auth.json exists — the key-free-home doctrine
// the file comment records, enforced here so a credential file cannot
// creep back in.
func TestCodexGuiBuildWritesTheKeyFreeHome(t *testing.T) {
	withHome(t)
	stubCodexHarvest(t)
	w := buildWire(t, codexGuiSpec, Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if filepath.Base(w.Root) != shim.HomeName {
		t.Fatalf("home dir = %s, want the fixed name %q", w.Root, shim.HomeName)
	}
	cfg := string(mustRead(t, filepath.Join(w.Root, "config.toml")))
	for _, want := range []string{
		`model = "m1"`,
		`model_provider = "kya"`,
		`forced_login_method = "api"`,
		`web_search = "disabled"`,
		"features.daemon_auto_start = false",
		"features.plugins = false",
		`env_key = "KYA_API_KEY"`,
		`wire_api = "responses"`,
		// The catalog pin rides the pairs' %q rendering: backslashes are
		// escaped, so the TOML basic string stays parseable on Windows.
		`model_catalog_json = ` + strconv.Quote(filepath.Join(w.Root, "models.json")),
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.toml missing %q:\n%s", want, cfg)
		}
	}
	if _, err := os.Stat(filepath.Join(w.Root, "auth.json")); !os.IsNotExist(err) {
		t.Errorf("the GUI home must hold NO auth.json (key rides the shim's env only); stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "models.json")); err != nil {
		t.Errorf("catalog models.json missing: %v", err)
	}
	rec := string(mustRead(t, filepath.Join(w.Root, "runtime.json")))
	if !strings.Contains(rec, `"codex_exe"`) {
		t.Errorf("runtime record missing codex_exe:\n%s", rec)
	}
}

// TestCodexGuiCarrierRefusesWhenTheLeaseIsHeld pins decision 1 (v1): a
// live server owns the home, so the launch refuses with the recovery
// instruction instead of rewriting config an app-server would never
// re-read.
func TestCodexGuiCarrierRefusesWhenTheLeaseIsHeld(t *testing.T) {
	withHome(t)
	home, err := shim.HomeDir()
	if err != nil {
		t.Fatalf("HomeDir: %v", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	lease, err := sandbox.AcquireLease(home)
	if err != nil {
		t.Fatalf("acquire test lease: %v", err)
	}
	defer lease.Release()
	_, err = codexGuiSpec.CarrierBuild(Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1", Catalog{})
	if err == nil {
		t.Fatal("CarrierBuild succeeded while the lease was held")
	}
	if !strings.Contains(err.Error(), "already live") {
		t.Errorf("refusal = %v, want the live-server recovery sentence", err)
	}
}

// TestCodexGuiAnnounceBlockAndShortLine pins the announce seam's two
// shapes: the state-change block (the one-time editor setup, including
// the WSL-off warning) and the quiet ready line.
func TestCodexGuiAnnounceBlockAndShortLine(t *testing.T) {
	home := filepath.Join("H", shim.HomeName)
	var got []byte
	orig := codexGuiWrite
	codexGuiWrite = func(b []byte) { got = append(got, b...) }
	t.Cleanup(func() { codexGuiWrite = orig })

	codexGuiAnnounce(home, "codex-cli 0.166.0", true)
	block := string(got)
	for _, want := range []string{"chatgpt.cliExecutable", "Run in WSL", "0.166.0", shim.HomeName} {
		if !strings.Contains(block, want) {
			t.Errorf("announce block missing %q:\n%s", want, block)
		}
	}
	got = nil
	codexGuiAnnounce(home, "codex-cli 0.166.0", false)
	if line := string(got); !strings.HasPrefix(line, "codex-gui: ready") {
		t.Errorf("ready line = %q, want the quiet codex-gui: ready form", line)
	}
}

// TestMatchCodexExtensionLines pins the exact-id matcher: the real id
// (bare and @version), and the refusal of similarly-named siblings
// (openai.codex-audio is a different extension; a prefix match here
// would install-gate on the wrong package).
func TestMatchCodexExtensionLines(t *testing.T) {
	listing := []byte("anthropic.claude-code@2.1.0\nopenai.codex-audio@26.1.0\nopenai.chatgpt@26.51021.22027\n")
	if !matchCodexExtensionLines(listing) {
		t.Error("the real extension id@version did not match")
	}
	if matchCodexExtensionLines([]byte("anthropic.claude-code\nopenai.codex-audio\n")) {
		t.Error("matched without the codex extension present")
	}
	if matchCodexExtensionLines([]byte("openai.chatgpt-extra@1.0.0\n")) {
		t.Error("a prefixed sibling matched — the id must match exactly, not by prefix")
	}
	if !matchCodexExtensionLines([]byte("OPENAI.CHATGPT@1.0.0\n")) {
		t.Error("case-insensitive id acceptance broke (editor listings vary)")
	}
}

// TestCodexGuiCleanupKeepsTheHome pins the lifecycle flip through the
// Wire: the launch's deferred cleanup must NOT reclaim the persistent
// home (its removal is the operator's, never a launch exit path's).
func TestCodexGuiCleanupKeepsTheHome(t *testing.T) {
	withHome(t)
	stubCodexHarvest(t)
	w := buildWire(t, codexGuiSpec, Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	w.Cleanup()
	if _, err := os.Stat(w.Root); err != nil {
		t.Fatalf("persistent home gone after cleanup: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
