package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"kyacode/internal/gatewaytest"
)

// readPiSandbox builds the pi sandbox against srv and returns the parsed
// models.json providers plus the sandbox dir. It runs inside the shared
// home-isolation helper like every other sandbox test — pointing BuildEnv
// at the developer's real ~/.kyacode (observed empirically once) violates
// the withHome-everywhere contract, so the helper both isolates HOME and
// canary-asserts the sandbox landed inside the temp home.
func readPiSandbox(t *testing.T, baseURL string) (dir string, providers map[string]any) {
	t.Helper()
	home := withHome(t)
	ep := Endpoint{BaseURL: baseURL, Token: "sk-test-12345"}
	w, err := piSpec.CarrierBuild(ep, "glm-5.3-flash-nvfp4", Resolve(ep, "glm-5.3-flash-nvfp4"))
	if err != nil {
		t.Fatalf("CarrierBuild: %v", err)
	}
	t.Cleanup(w.Cleanup)
	dir = w.Root
	if dir == "" {
		t.Fatalf("wire carries no sandbox root")
	}
	if !testDirWithin(home, dir) {
		t.Fatalf("pi sandbox canary FAILED: sandbox %s is outside the isolated home %s — BuildEnv read the ambient HOME", dir, home)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".pi", "agent", "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var parsed struct {
		Providers map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("models.json not valid JSON: %v (%s)", err, b)
	}
	return dir, parsed.Providers
}

// testDirWithin reports whether dir lies at or below root (the sandbox
// canary used by the home-isolated sandbox helpers).
func testDirWithin(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// piModelIDs flattens the kya provider's models array into id strings.
func piModelIDs(t *testing.T, providers map[string]any) []string {
	t.Helper()
	p, ok := providers["kya"].(map[string]any)
	if !ok {
		t.Fatalf("missing kya provider: %v", providers)
	}
	raw, ok := p["models"].([]any)
	if !ok {
		t.Fatalf("missing models array: %v", p)
	}
	var ids []string
	for _, m := range raw {
		mm, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("model entry not an object: %v", m)
		}
		id, _ := mm["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// TestPiBuildEnvFetchesAllGatewayModels pins the catalog-refresh behavior:
// the sandbox models.json lists EVERY gateway model, not just the CLI one.
func TestPiBuildEnvFetchesAllGatewayModels(t *testing.T) {
	srv := gatewaytest.Models(t, "sk-test-12345", []string{"zeta-model", "alpha-model", "glm-5.3-flash-nvfp4"})
	_, providers := readPiSandbox(t, srv.URL)
	ids := piModelIDs(t, providers)
	// CLI model first, then the rest sorted.
	want := []string{"glm-5.3-flash-nvfp4", "alpha-model", "zeta-model"}
	if len(ids) != len(want) {
		t.Fatalf("model ids = %v, want %v", ids, want)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Fatalf("model ids = %v, want %v (order: CLI model leads, rest sorted)", ids, want)
		}
	}
}

// TestPiBuildEnvSettingsDefaultModel pins the settings.json wiring for the
// CLI-chosen model: pi honors a saved default only as the pair
// defaultProvider + defaultModel (defaultModel carries the bare id — the
// 'kya/<id>' compose form is a CLI-path shape pi never reads from
// settings).
func TestPiBuildEnvSettingsDefaultModel(t *testing.T) {
	srv := gatewaytest.Models(t, "sk-test-12345", []string{"glm-5.3-flash-nvfp4"})
	dir, _ := readPiSandbox(t, srv.URL)
	b, err := os.ReadFile(filepath.Join(dir, ".pi", "agent", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatalf("settings.json not valid JSON: %v (%s)", err, b)
	}
	if settings.DefaultProvider != "kya" {
		t.Fatalf("defaultProvider = %q, want %q", settings.DefaultProvider, "kya")
	}
	if settings.DefaultModel != "glm-5.3-flash-nvfp4" {
		t.Fatalf("defaultModel = %q, want %q", settings.DefaultModel, "glm-5.3-flash-nvfp4")
	}
}

// TestPiBuildEnvFallsBackOnGatewayError pins the degradation path: a
// failing models fetch must not block the launch — the CLI model (or the
// kya-model placeholder) is still written.
func TestPiBuildEnvFallsBackOnGatewayError(t *testing.T) {
	// Empty model list from the gateway = failure to expand.
	srv := gatewaytest.Models(t, "sk-test-12345", nil)
	_, providers := readPiSandbox(t, srv.URL)
	ids := piModelIDs(t, providers)
	if len(ids) != 1 || ids[0] != "glm-5.3-flash-nvfp4" {
		t.Fatalf("fallback ids = %v, want [glm-5.3-flash-nvfp4]", ids)
	}

	// No CLI model either: placeholder.
	ep := Endpoint{BaseURL: srv.URL, Token: "sk-test-12345"}
	w, err := piSpec.CarrierBuild(ep, "", Resolve(ep, ""))
	if err != nil {
		t.Fatalf("CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	b, err := os.ReadFile(filepath.Join(w.Root, ".pi", "agent", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "kya-model") {
		t.Fatalf("placeholder missing on full degrade: %s", b)
	}
}

// TestCatalogCarriersListEveryGatewayModel pins the shared behavior for
// every runner whose config file can carry a model catalog: the sandbox
// config must list EVERY gateway model (CLI model first), not just one.
// Runners with single-model wiring (claude/qwen env, codex toml, cline,
// hermes) are intentionally not in this table.
func TestCatalogCarriersListEveryGatewayModel(t *testing.T) {
	withHome(t) // dsh's carrier test reads its sandbox from ~/.kyacode
	srv := gatewaytest.Models(t, "sk-test-12345", []string{"zeta-model", "alpha-model"})
	ep := Endpoint{BaseURL: srv.URL, Token: "sk-test-12345"}
	cliModel := "glm-5.3-flash-nvfp4"

	for _, s := range []Spec{opencodeSpec, ompSpec, openclawSpec, droidSpec, dshSpec} {
		content := carrierChannelContent(t, s, ep, cliModel)
		for _, id := range []string{cliModel, "alpha-model", "zeta-model"} {
			if !strings.Contains(content, id) {
				t.Errorf("%s: config missing gateway catalog model %q\n%s", s.Name, id, content)
			}
		}
	}
}

// TestCatalogCarriersDegradeOnGatewayError pins the degrade for every
// catalog carrier: a dead gateway degrades to the CLI model (or the
// kya-model placeholder) and the launch proceeds.
func TestCatalogCarriersDegradeOnGatewayError(t *testing.T) {
	withHome(t) // dsh's carrier test reads its sandbox from ~/.kyacode
	// Unroutable endpoint — connection refused / timeout.
	ep := Endpoint{BaseURL: "http://127.0.0.1:1", Token: "sk-test-12345"}
	for _, s := range []Spec{opencodeSpec, ompSpec, openclawSpec, droidSpec, dshSpec, piSpec} {
		content := carrierChannelContent(t, s, ep, "m1")
		if !strings.Contains(content, "m1") {
			t.Errorf("%s: degraded config lost the CLI model", s.Name)
		}
	}

	// No CLI model: every carrier degrades to the kya-model placeholder.
	for _, s := range []Spec{opencodeSpec, ompSpec, openclawSpec, droidSpec, dshSpec, piSpec} {
		content := carrierChannelContent(t, s, ep, "")
		if !strings.Contains(content, "kya-model") {
			t.Errorf("%s: degraded config missing the kya-model placeholder", s.Name)
		}
	}
}

// carrierChannelContent drives one spec's declared carrier channel the
// way the launch boundary would (Resolve supplies the decision — live or
// degraded) and returns the config content for assertion. A spec that is
// NOT a declared carrier fails the test: this helper exists for the
// carrier tables only.
func carrierChannelContent(t *testing.T, s Spec, ep Endpoint, model string) string {
	t.Helper()
	var content string
	switch {
	case s.Wiring == WiringSandbox && s.CarrierBuild != nil:
		w, err := s.CarrierBuild(ep, model, Resolve(ep, model))
		if err != nil {
			t.Fatalf("%s: CarrierBuild: %v", s.Name, err)
		}
		// Content must be read BEFORE cleanup: the sandbox is reclaimed
		// the moment cleanup runs.
		content = carrierContent(t, s.Name, w.Root)
		w.Cleanup()
	case s.Wiring == WiringEnv && s.CarrierEnv != nil:
		content = strings.Join(s.CarrierEnv(ep, model, Resolve(ep, model)), "\n")
	default:
		t.Fatalf("%s: not a declared catalog carrier (wiring %v, carrier channels unset)", s.Name, s.Wiring)
	}
	return content
}

// carrierContent gathers the config carrier for assertion from the wire's
// sandbox root — every file under it, concatenated. dsh is special: the
// patch yaml inside its sandbox references the settings path, so the
// settings file is read through that indirection (the hop the CLI itself
// takes; the wire's root makes it a direct join, no dir-shape guessing).
func carrierContent(t *testing.T, name, root string) string {
	t.Helper()
	if name == "dsh" {
		patch, err := os.ReadFile(filepath.Join(root, "kya.cordis.yml"))
		if err != nil {
			t.Fatalf("dsh: read patch: %v", err)
		}
		var entries []struct {
			Config struct {
				Path string `json:"path"`
			} `json:"config"`
		}
		if err := yaml.Unmarshal(patch, &entries); err != nil {
			t.Fatalf("dsh: parse patch: %v", err)
		}
		if len(entries) == 0 || entries[0].Config.Path == "" {
			t.Fatalf("dsh: patch does not reference settings: %s", patch)
		}
		settings, err := os.ReadFile(entries[0].Config.Path)
		if err != nil {
			t.Fatalf("dsh: read settings: %v", err)
		}
		return string(settings)
	}
	return walkFiles(t, name, root)
}

// walkFiles reading every file under root.
func walkFiles(t *testing.T, name, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sb.Write(b)
		sb.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("%s: walk sandbox: %v", name, err)
	}
	return sb.String()
}
