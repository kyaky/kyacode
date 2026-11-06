package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kyacode/internal/harness/sandbox"
	"kyacode/internal/shim"
)

// The codex GUI harness: the official Codex IDE extension driven against
// the KYA gateway through the engine shim, with a PERSISTENT isolated
// CODEX_HOME and zero disk writes to the user's real ~/.codex.
//
// The architecture differs from the codex CLI harness in exactly one
// load-bearing dimension: lifecycle. The CLI is a throwaway per-launch
// sandbox reclaimed when `kyacode launch` returns; the GUI's engine is
// an app-server the editor spawns and keeps alive for hours across many
// kyacode-less windows, so the home cannot be per-launch — it is a
// persistent Named sandbox (sandbox.Named), fixed-name and structurally
// outside the orphan sweep's grammar. The persistence is safe because
// the home holds NO key: no auth.json is written (verified empirically
// on codex 0.166.0: the app-server path runs a real turn with an
// env_key provider on env alone), the config references the key by the
// KYA_API_KEY name, and the value rides only the shim's injected
// environment at spawn time. A key-free persistent home is state on
// disk, not a credential at rest — the doctrine the sandbox package
// protects holds without an exception.
//
// Wiring facts live in the home's config.toml alone (the same
// codexWiringPairs the CLI renders twice): the shim synthesizes no -c
// pins because it cannot know the entry model without parsing the file,
// and upstream's project-layer denylist keeps every trust-gated layer
// from overriding model_provider/model_providers (verified in the
// codex-rs loader). The catalog (models.json + the model_catalog_json
// pin) is what makes the extension's /model picker list gateway models:
// without it the picker falls back to codex's bundled OpenAI list, every
// row a 404 against the gateway.
//
// The extension spawns the engine recorded in the home's runtime.json —
// the EXACT binary this build harvested the catalog with (the certified
// tuple rule from the design review: harvest and runtime must never
// drift apart).
const codexGuiExtension = "openai.chatgpt"

// codexGuiPrefix is the registry anchor the validate contract demands
// from every WiringSandbox spec. The GUI never creates pid-random
// sandboxes under it — its home is the fixed-name sandbox.Named shape —
// so the anchor stays permanently match-less (NameShape requires the
// numeric pid-random tail) and the orphan sweep can never touch the
// home. The prefix is declared to keep the registry coherent, not to be
// swept.
const codexGuiPrefix = "codexgui"

var codexGuiSpec = Spec{
	Name:        "codex-gui",
	Description: "OpenAI Codex IDE extension (persistent isolated CODEX_HOME, no disk writes to ~/.codex)",
	Wiring:      WiringSandbox,
	// The prefix is a coherence anchor only; see codexGuiPrefix.
	SandboxPrefix: codexGuiPrefix,
	// The home is the fixed-name persistent sandbox (see the file doc):
	// declared so the sweep/reclaim contracts branch on data instead of
	// sniffing dir shapes.
	PersistentHome: true,
	// RequiresModel as the entry point, exactly like the CLI codex: the
	// typed model pins the home's config.toml `model` (the picker's
	// isDefault row) and heads the catalog.
	RequiresModel: true,
	// Find is the extension's presence probe: the VS Code `code` CLI on
	// PATH AND the Codex extension installed in it. The probe spawns the
	// CLI once per launch/status under a bounded wait — the one
	// subprocess cost a GUI runner carries that env-lookup runners do
	// not.
	Find: codexGuiFind,
	// Install is the extension's official installer through the code CLI
	// — the confirm-gated path ensureInstalled already owns. When the
	// code CLI itself is missing, the installer fails loudly (exec not
	// found); VS Code with `code` on PATH is the documented prerequisite.
	Install: func() (string, []string) {
		return "code", []string{"--install-extension", codexGuiExtension}
	},
	CarrierBuild: codexGuiCarrierBuild,
}

// codexGuiFind locates the `code` CLI and confirms the extension. The
// bounded wait fails closed: a hung CLI probe reads as "not installed"
// and the launch proceeds to the confirm-gated installer path.
func codexGuiFind() (string, bool) {
	code, ok := findIn("code", nil)
	if !ok {
		return "", false
	}
	if !codexExtensionInstalled(code) {
		return "", false
	}
	return code, true
}

// codexExtensionInstalled runs `code --list-extensions --show-versions`
// and matches the Codex extension id through the pure matcher below.
func codexExtensionInstalled(code string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, code, "--list-extensions", "--show-versions").Output()
	if err != nil {
		return false
	}
	return matchCodexExtensionLines(out)
}

// matchCodexExtensionLines matches the extension id EXACTLY (id or
// id@version) against the listing's lines: a prefix match would
// false-positive on any similarly-named sibling (openai ships
// companions like openai.codex-audio under its own id, and a
// hypothetical openai.chatgpt-<suffix> would be a different extension).
// Pure so the matching rule is testable without spawning a CLI.
func matchCodexExtensionLines(out []byte) bool {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if line == codexGuiExtension || strings.HasPrefix(line, codexGuiExtension+"@") {
			return true
		}
	}
	return false
}

// findCodexEngine is the GUI build's engine seam: the exact binary the
// shim records AND the harvest draws from (the certified tuple — one
// resolution, two consumers). A var seam for the same hermetic reason the
// harvest arms are vars: a machine without codex installed must be able
// to drive the GUI build's tests.
var findCodexEngine = func() (string, bool) { return findIn("codex", nil) }

// codexGuiCarrierBuild materialises the launch's wiring: the lease probe
// (never regenerate under a live server), the persistent home's managed
// layer, the engine record, the shim install, and the editor spawn.
func codexGuiCarrierBuild(ep Endpoint, model string, cat Catalog) (Wire, error) {
	home, err := shim.HomeDir()
	if err != nil {
		return Wire{}, err
	}
	// Decision 1 (v1): a live server owns the home; the launch refuses
	// instead of rewriting config under it (an app-server reads config
	// at startup only — a silent rewrite would apply never). The lease
	// is freed by the holder's death, so the recovery is: close the
	// Codex panel or reload the window, then re-run.
	held, err := sandbox.LeaseHeld(home)
	if err != nil {
		return Wire{}, fmt.Errorf("probe the Codex GUI lease: %w", err)
	}
	if held {
		return Wire{}, fmt.Errorf("a Codex GUI server is already live for %s — close the Codex panel or run 'Developer: Reload Window' in VS Code, then re-run `kyacode launch codex-gui` to apply new wiring", home)
	}
	sb, err := sandbox.Named(shim.HomeName)
	if err != nil {
		return Wire{}, err
	}
	// The engine rides the seam above: the exact binary recorded for the
	// shim AND harvested from (the certified tuple — one resolution, two
	// consumers).
	codexPath, ok := findCodexEngine()
	if !ok {
		return Wire{}, fmt.Errorf("codex engine not found for the GUI home; install it first (see `kyacode status`)")
	}
	version := codexVersionProbe(codexPath)
	catalogPath := ""
	if entry, ok := codexCatalogEntryHarvestFrom(codexPath, sb.Root()); ok {
		if body, ok := codexCatalogJSON(cat.Rows(), model, entry); ok {
			if err := sb.Write("models.json", body); err != nil {
				return Wire{}, err
			}
			catalogPath = filepath.Join(sb.Root(), "models.json")
		}
	}
	pairs, err := codexWiringPairs(ep, model, catalogPath)
	if err != nil {
		return Wire{}, err
	}
	if err := sb.Write("config.toml", []byte(codexConfigToml(pairs))); err != nil {
		return Wire{}, err
	}
	// No auth.json, deliberately: with an env_key provider the
	// app-server needs no login and no static key (verified with a real
	// turn); the key rides the shim's spawn-time environment only.
	if err := shim.SaveRuntimeRecord(codexPath, version); err != nil {
		return Wire{}, err
	}
	changed, err := shimChanged()
	if err != nil {
		return Wire{}, err
	}
	if err := shim.Install(); err != nil {
		return Wire{}, err
	}
	if err := shim.SetEnabled(true); err != nil {
		return Wire{}, err
	}
	codexGuiAnnounce(home, version, changed)
	// The child is the editor CLI, never the engine: `code -n <cwd>`
	// opens a window on the directory the launch ran in, and the
	// extension inside it spawns the shim by its own configured path.
	// No key env rides the editor's process tree — the wiring rows live
	// only in the shim's own spawn. The home persists (Named sandbox),
	// so the launch's cleanup is a no-op by contract.
	wd, err := os.Getwd()
	if err != nil {
		return Wire{}, err
	}
	return Wire{Root: sb.Root(), Args: []string{"-n", wd}, Cleanup: func() {}}, nil
}

// shimChanged reports whether the installed shim binary is missing or
// differs from the CURRENT binary — the state change that decides
// whether the launch prints the full first-setup block or the one-line
// ready note.
func shimChanged() (bool, error) {
	dst, err := shim.ExePath()
	if err != nil {
		return false, err
	}
	src, err := os.Executable()
	if err != nil {
		return false, err
	}
	newBody, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	oldBody, err := os.ReadFile(dst)
	if err != nil {
		return true, nil // missing shim: first install
	}
	return !bytes.Equal(oldBody, newBody), nil
}

// codexGuiWrite is the announce seam (stub-able in tests): the harness
// library's one declared stdout surface, the way sandbox.Cleanup is its
// one declared stderr surface. The guidance is a property of the wiring
// build — the CLI boundary has no per-spec surface to render it — and it
// prints exactly when the shim's state changed or on every launch's
// one-line readiness note.
var codexGuiWrite = func(b []byte) {
	_, _ = os.Stdout.Write(b)
}

// codexGuiAnnounce prints the operator-facing setup facts. The full block
// rides a state change (first install or a refreshed binary); otherwise
// a single ready line keeps the launch output quiet and deterministic.
func codexGuiAnnounce(home, version string, changed bool) {
	dst, err := shim.ExePath()
	if err != nil {
		return
	}
	eng := version
	if eng == "" {
		eng = "unknown version"
	}
	var b []byte
	if changed {
		b = []byte("codex-gui: engine shim installed at " + dst + "\n" +
			"codex-gui: persistent home " + home + " (engine " + eng + ", no key stored there)\n" +
			"One-time editor setup — set this VS Code setting, then re-run `kyacode launch codex-gui`:\n" +
			"  \"chatgpt.cliExecutable\": \"" + dst + "\"\n" +
			"Until the setting is saved the Codex panel runs vanilla codex; keep the Codex setting 'Run in WSL' OFF (a Windows-side CODEX_HOME does not reach a WSL-spawned engine).\n")
	} else {
		b = []byte("codex-gui: ready (home " + home + ", engine " + eng + ")\n")
	}
	codexGuiWrite(b)
}

// codexVersionProbe reads the engine's --version (bounded); a failure
// degrades to an empty version, never a launch failure — the version is
// a record, not a gate.
func codexVersionProbe(codexPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, codexPath, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
