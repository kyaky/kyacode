package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kyacode/internal/harness/sandbox"

	"gopkg.in/yaml.v3"
)

// loadHermesProviders builds the sandbox config.yaml the way a real launch
// does and parses the providers table back out, so tests assert the entry
// fields the upstream resolver actually reads (hermes_cli/runtime_provider.py
// named-custom path) rather than substrings that could match the model:
// section.
func loadHermesProviders(t *testing.T, ep Endpoint, model string) map[string]any {
	t.Helper()
	withHome(t)
	w, err := hermesSpec.BuildEnv(ep, model)
	if err != nil {
		t.Fatalf("hermes BuildEnv: %v", err)
	}
	defer w.Cleanup()
	if w.Root == "" {
		t.Fatalf("hermes wire carries no sandbox root: %+v", w)
	}
	b, err := os.ReadFile(filepath.Join(w.Root, "config.yaml"))
	if err != nil {
		t.Fatalf("hermes config.yaml: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("hermes config.yaml parse: %v\n%s", err, b)
	}
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		t.Fatalf("config.yaml has no providers table:\n%s", b)
	}
	entry, ok := providers[hermesProviderKey].(map[string]any)
	if !ok {
		t.Fatalf("providers has no %q entry:\n%s", hermesProviderKey, b)
	}
	return entry
}

// In the modern (config v12) resolver shape, `api` IS the base-URL field
// of the provider entry (runtime_provider.py:845: base_url = entry['api']).
// A URL in the entry is what makes hermes resolve to the real gateway;
// the literal provider type ("openai") leaves hermes pointed at nothing.
func TestHermesConfigCarriesGatewayURLInAPIField(t *testing.T) {
	entry := loadHermesProviders(t,
		Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if got, _ := entry["api"].(string); got != "https://127.0.0.1:1/v1" {
		t.Errorf("providers['kya-launch'].api = %q, want the gateway base (with /v1)", got)
	}
}

// The named-custom credential channel is the ENTRY's api_key (upstream
// never reads model.api_key on that path): without it hermes launches
// unauthenticated no matter what the model: section carries.
func TestHermesConfigPinsTheRealTokenInAPIKey(t *testing.T) {
	entry := loadHermesProviders(t,
		Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if got, _ := entry["api_key"].(string); got != "sk-test-12345" {
		t.Errorf("providers['kya-launch'].api_key = %q, want the real token", got)
	}
}

// default_model mirror model.default so hermes startup picks the launched
// model even when env indirection is defeated.
func TestHermesConfigPinsTheSelectedModelInDefaultModel(t *testing.T) {
	entry := loadHermesProviders(t,
		Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if got, _ := entry["default_model"].(string); got != "m1" {
		t.Errorf("providers['kya-launch'].default_model = %q, want the selected model", got)
	}
}

// transport is pinned so resolution never depends on URL sniffing
// (_detect_api_mode_for_url would reclassify any base URL ending in
// /anthropic as the Anthropic messages API).
func TestHermesConfigPinsTheChatCompletionsTransport(t *testing.T) {
	entry := loadHermesProviders(t,
		Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if got, _ := entry["transport"].(string); got != "openai_chat" {
		t.Errorf("providers['kya-launch'].transport = %q, want pinned 'openai_chat'", got)
	}
}

// F1 residue: hermes's key channel is FileOnly — the real key lives in the
// sandboxed config.yaml only, so the child env carries NO credential row.
// Asserted at hermes's own site (hermes is outside the four-runner sandbox
// table that pins the keyInEnv column).
func TestHermesBuildEnvChildEnvCarriesNoKeyRow(t *testing.T) {
	withHome(t)
	w, err := hermesSpec.BuildEnv(Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if err != nil {
		t.Fatalf("hermes BuildEnv: %v", err)
	}
	defer w.Cleanup()
	for _, row := range w.Env {
		if strings.HasPrefix(row, sandbox.KeyEnvName+"=") {
			t.Errorf("hermes child env carries a %s row (FileOnly: the key is in config.yaml only): %v", sandbox.KeyEnvName, w.Env)
			return
		}
	}
}
