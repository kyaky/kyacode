package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kyacode/internal/gatewaytest"
	"kyacode/internal/harness/sandbox"

	"gopkg.in/yaml.v3"
)

// loadOMPSandbox builds the sandbox via omp's declared carrier channel
// (the launch boundary's Resolve supplies the catalog decision — live
// server or degrade) and returns its two parsed files: agent/models.yml
// (provider table) and agent/config.yml (settings, where omp's default
// model role lives).
func loadOMPSandbox(t *testing.T, ep Endpoint, model string) (modelsYml, configYml map[string]any) {
	t.Helper()
	withHome(t)
	w, err := ompSpec.CarrierBuild(ep, model, Resolve(ep, model))
	if err != nil {
		t.Fatalf("omp CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	if w.Root == "" {
		t.Fatalf("omp wire carries no sandbox root: %+v", w)
	}
	// omp's env points the harness at agent/ under the sandbox — the same
	// fact the wire's root records; settings live one level down.
	agentDir := filepath.Join(w.Root, "agent")

	readYML := func(name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(agentDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(b, &m); err != nil {
			t.Fatalf("parse %s: %v\n%s", name, err, b)
		}
		return m
	}
	return readYML("models.yml"), readYML("config.yml")
}

// The omp models.yml must be a valid file the omptype registry accepts:
// the root declares ONLY providers, and the kya provider carries inline
// models with no discovery key — discovery.type "list" is not one of the
// schema's enum values (ollama|llama.cpp|lm-studio|openai-models-list|
// proxy|litellm), which made the whole file fail loudly and OMP fall back
// to built-in providers.
func TestOMPBuildEnvModelsYmlCarriesOnlyAValidProvider(t *testing.T) {
	modelsYml, _ := loadOMPSandbox(t,
		Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	for _, k := range []string{"defaultModel", "model", "settings"} {
		if _, bad := modelsYml[k]; bad {
			t.Errorf("models.yml root carries dead key %q — root declares only providers", k)
		}
	}
	if _, ok := modelsYml["providers"]; !ok {
		t.Fatal("models.yml missing providers table")
	}
	kya, ok := modelsYml["providers"].(map[string]any)["kya"].(map[string]any)
	if !ok {
		t.Fatal("providers.kya missing")
	}
	if _, bad := kya["discovery"]; bad {
		t.Errorf("providers.kya carries a discovery key ('list' fails the schema enum)")
	}
	if kya["apiKey"] != "sk-test-12345" {
		t.Errorf("providers.kya.apiKey = %v, want the real token", kya["apiKey"])
	}
}

// The launch-selected model is pinned as omp's default model role
// (settings.modelRoles.default — the key findInitialModel honors), so a
// mid-session restart without CLI args still opens on the launched model.
func TestOMPBuildEnvPinsDefaultModelRole(t *testing.T) {
	_, configYml := loadOMPSandbox(t,
		Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	roles, ok := configYml["modelRoles"].(map[string]any)
	if !ok {
		t.Fatalf("config.yml missing modelRoles: %v", configYml)
	}
	if got, _ := roles["default"].(string); got != "kya/m1" {
		t.Errorf("modelRoles.default = %q, want the launched model in provider form", got)
	}
}

// The pin discriminator is fetched-ness, not a compare against the
// placeholder string: a gateway legitimately serving a catalog entry
// named like PlaceholderModelID must win the pin write like any real id
// — the old sentinel compare misclassified that gateway as "nothing
// valid" and silently left the pin unwritten.
func TestOMPBuildEnvPinsRealCatalogEntryNamedLikeThePlaceholder(t *testing.T) {
	withHome(t)
	gateway := gatewaytest.Models(t, "sk-test-12345", []string{"kya-model"})

	_, configYml := loadOMPSandbox(t, Endpoint{BaseURL: gateway.URL, Token: "sk-test-12345"}, "")
	roles, ok := configYml["modelRoles"].(map[string]any)
	if !ok {
		t.Fatalf("a fetched catalog with a placeholder-named entry must still pin the role; config: %v", configYml)
	}
	if got, _ := roles["default"].(string); got != scopedModelID(PlaceholderModelID) {
		t.Errorf("modelRoles.default = %q, want the placeholder-named catalog entry in provider form", got)
	}
}

// The degraded half of the same discriminator stays intact: no CLI model
// and an unreachable catalog means nothing valid exists, so no
// config.yml (and no pin pointing at a nonexistent id) is written.
func TestOMPBuildEnvWritesNoPinOnDegradedCatalog(t *testing.T) {
	withHome(t)
	w, err := ompSpec.CarrierBuild(Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "", degradedCatalog())
	if err != nil {
		t.Fatalf("omp CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	if w.Root == "" {
		t.Fatalf("omp wire carries no sandbox root: %+v", w)
	}
	agentDir := filepath.Join(w.Root, "agent")
	if _, err := os.Stat(filepath.Join(agentDir, "config.yml")); !os.IsNotExist(err) {
		t.Errorf("degraded catalog must write no config.yml at all (stat err %v)", err)
	}
}

// F1 residue: omp's key channel is FileOnly — the real key lives in
// agent/models.yml only, so the child env carries NO credential row.
// Asserted at omp's own site (omp is outside the four-runner sandbox table
// that pins the keyInEnv column).
func TestOMPBuildEnvChildEnvCarriesNoKeyRow(t *testing.T) {
	withHome(t)
	w, err := ompSpec.CarrierBuild(Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1", typedCatalog("m1"))
	if err != nil {
		t.Fatalf("omp CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	for _, row := range w.Env {
		if strings.HasPrefix(row, sandbox.KeyEnvName+"=") {
			t.Errorf("omp child env carries a %s row (FileOnly: the key is in models.yml only): %v", sandbox.KeyEnvName, w.Env)
			return
		}
	}
}
