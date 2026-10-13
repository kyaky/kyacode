package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// eqArgs compares argv-shaped expectations positionally: for command
// lines the ORDER carries the contract, including "nothing follows".
// Siblings with whole-struct expectations use reflect.DeepEqual directly
// (requires_model_test.go) — both styles are in use here; pick by shape,
// positional equality for argv, DeepEqual for data.
func eqArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v (differ at %d)", got, want, i)
		}
	}
}

// qwen's selectedAuthType = argv.authType || persisted settings ||
// env inference — persisted settings outrank env, so an env-only wiring
// can be silently shadowed by a user's saved qwen settings. The launch
// must pin --auth-type openai on every invocation, and carry the model
// with it when one was launched.
func TestQwenExtraArgsPinsTheOpenAIAuthType(t *testing.T) {
	eqArgs(t, qwenSpec.ExtraArgs("m1"), []string{"--auth-type", "openai", "--model", "m1"})
	eqArgs(t, qwenSpec.ExtraArgs(""), []string{"--auth-type", "openai"})
}

// qwen's env auth-inference (getAuthTypeFromEnv) requires OPENAI_MODEL to
// exist, and the AUTH_ENV mappings consume it — so the launched model rides
// OPENAI_MODEL verbatim. The model is always present: qwen is RequiresModel
// (a model-less qwen is refused before qwenEnv runs), so there is no
// degrade-time placeholder to pin (the ticket-19 exception, re-ruled moot
// 2026-09-13).
func TestQwenEnvAlwaysNamesAModel(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	env := qwenSpec.Env(ep, "m1")
	if !slices.Contains(env, "OPENAI_MODEL=m1") {
		t.Errorf("env = %v, want the exact model-named row OPENAI_MODEL=m1", env)
	}
}

// pi honors a saved default only as the PAIR defaultProvider +
// defaultModel (defaultModel carries the BARE model id — the 'kya/<id>'
// compose form is only valid on the CLI path). settings.json must carry
// exactly those two keys.
func TestPiBuildEnvSavesBareDefaultProviderAndDefaultModel(t *testing.T) {
	settings := parseJSONFile(t, func() (string, func()) {
		withHome(t) // sandbox home isolation (never the real ~/.kyacode)
		w, err := piSpec.CarrierBuild(
			Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1", typedCatalog("m1"))
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Join(w.Root, ".pi", "agent", "settings.json"), w.Cleanup
	})
	if got, _ := settings["defaultProvider"].(string); got != "kya" {
		t.Errorf("defaultProvider = %q, want the provider key kya", got)
	}
	if got, _ := settings["defaultModel"].(string); got != "m1" {
		t.Errorf("defaultModel = %q, want the bare launched model id", got)
	}
}

// The droid settings schema requires identity fields on each customModels
// row (id must start with 'custom:', integer index for display order,
// displayName) and the org-style default lives at
// sessionDefaultSettings.model.
func TestDroidBuildEnvRowsCarryTheIdentityFieldsAndSessionDefault(t *testing.T) {
	settings := parseJSONFile(t, func() (string, func()) {
		withHome(t) // sandbox home isolation (never the real ~/.kyacode)
		w, err := droidSpec.CarrierBuild(
			Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1", typedCatalog("m1"))
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Join(w.Root, ".factory", "settings.json"), w.Cleanup
	})
	rows, ok := settings["customModels"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("customModels missing/empty: %v", settings["customModels"])
	}
	for i, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("row %d is not an object: %v", i, r)
		}
		id, _ := row["id"].(string)
		if !strings.HasPrefix(id, "custom:") {
			t.Errorf("row %d id = %q, must start with 'custom:'", i, id)
		}
		if _, ok := row["index"].(float64); !ok {
			t.Errorf("row %d missing integer index: %v", i, row)
		}
		if dn, _ := row["displayName"].(string); dn == "" {
			t.Errorf("row %d missing displayName: %v", i, row)
		}
	}
	if _, bad := settings["model"]; bad {
		t.Errorf("top-level 'model' is a user-settings key, not the org default carrier; got %v", settings["model"])
	}
	sds, ok := settings["sessionDefaultSettings"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing sessionDefaultSettings: %v", settings)
	}
	if got, _ := sds["model"].(string); got != "m1" {
		t.Errorf("sessionDefaultSettings.model = %q, want the default model", got)
	}
}

// parseJSONFile builds a sandbox through builder's code, reads the JSON
// file it produced, and hands back both the file content and the builder's
// cleanup. Builders must isolate HOME themselves (the shared withHome
// helper) — a sandbox built against the developer's real ~/.kyacode is
// the exact leak the withHome-everywhere contract bans.
func parseJSONFile(t *testing.T, builder func() (string, func())) map[string]any {
	t.Helper()
	path, cleanup := builder()
	defer cleanup()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
	return m
}
