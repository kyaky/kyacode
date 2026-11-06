package claude3p

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var uuidShape = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

func withTempRoot(t *testing.T) string {
	t.Helper()
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	return filepath.Join(local, "Claude-3p", "configLibrary")
}

// TestUUIDv4Shape pins the identifier grammar the app's loader regex
// demands (36 chars, version-4 bits).
func TestUUIDv4Shape(t *testing.T) {
	id, err := uuidv4()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidShape.MatchString(id) {
		t.Fatalf("uuid %q does not match the v4 shape", id)
	}
	id2, _ := uuidv4()
	if id2 == id {
		t.Fatal("two uuid calls produced the same id")
	}
}

// TestBuildPinsTheHelperWiring pins the flat v1 body byte-for-byte at
// the key level: gateway provider, bearer, the credential helper pointing
// at kyacode itself (NO api-key field - the credential never rests in
// the config), the model entries with behavesAs and the lifted effort
// cap, and the chat-tab keep-alive.
func TestBuildPinsTheHelperWiring(t *testing.T) {
	metaBody, id, cfgBody, err := Build(
		[]string{"glm-5.3-flash", "qwen3.8-27b"},
		"https://inf.kyaky.ai", `C:\Programs\kyacode\kyacode.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if !uuidShape.MatchString(id) {
		t.Fatalf("build id %q is not a v4 uuid", id)
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgBody, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["inferenceProvider"] != "gateway" {
		t.Errorf("inferenceProvider = %v", cfg["inferenceProvider"])
	}
	if cfg["inferenceGatewayBaseUrl"] != "https://inf.kyaky.ai" {
		t.Errorf("base url = %v (want the /v1-less canonical base)", cfg["inferenceGatewayBaseUrl"])
	}
	if cfg["inferenceGatewayAuthScheme"] != "bearer" {
		t.Errorf("auth scheme = %v", cfg["inferenceGatewayAuthScheme"])
	}
	if cfg["inferenceCredentialKind"] != "helper-script" {
		t.Errorf("credential kind = %v", cfg["inferenceCredentialKind"])
	}
	if _, has := cfg["inferenceGatewayApiKey"]; has {
		t.Error("the config carries an api-key field - the credential must ride the helper only")
	}
	if cfg["inferenceCredentialHelperWindows"] != `C:\Programs\kyacode\kyacode.exe` {
		t.Errorf("helper path = %v", cfg["inferenceCredentialHelperWindows"])
	}
	args, _ := cfg["inferenceCredentialHelperArgs"].([]any)
	if len(args) != 1 || args[0] != "claude-desktop-cred" {
		t.Errorf("helper args = %v, want [claude-desktop-cred]", args)
	}
	models, _ := cfg["inferenceModels"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %v, want two entries", models)
	}
	first := models[0].(map[string]any)
	if first["name"] != "glm-5.3-flash" || first["behavesAs"] != "claude-sonnet-5" || first["maxEffort"] != "high" {
		t.Errorf("first model entry = %v", first)
	}
	if cfg["chatTabEnabled"] != true {
		t.Error("chatTabEnabled must keep the Chat tab alive in 3P mode")
	}
	var m meta
	if err := json.Unmarshal(metaBody, &m); err != nil {
		t.Fatal(err)
	}
	if m.AppliedID != id || len(m.Entries) != 1 || m.Entries[0].ID != id || m.Entries[0].Name != entryName {
		t.Fatalf("meta = %+v, want the applied id wired to the kyacode entry", m)
	}
	// BOM-less: the app's JSON parse is reported to choke on a BOM.
	if strings.HasPrefix(string(cfgBody), "\xef\xbb\xbf") {
		t.Error("config body carries a BOM")
	}
}

// TestApplyRemoveOwnershipLifecycle walks the ownership doctrine end to
// end on a temp root: apply over absent, re-apply without orphans,
// foreign-refusal on apply and remove, and the unapplied-entry removal
// that preserves the foreign configuration.
func TestApplyRemoveOwnershipLifecycle(t *testing.T) {
	dir := withTempRoot(t)

	id, err := Apply([]string{"m1"}, "https://g.example", "C:\\k\\kyacode.exe")
	if err != nil {
		t.Fatalf("apply over absent: %v", err)
	}
	state, applied, err := ReadState()
	if err != nil || state != StateOurs || applied != id {
		t.Fatalf("state after apply = %v/%q/%v, want ours/%s", state, applied, err, id)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
		t.Fatalf("entry file missing: %v", err)
	}

	// Re-apply: a fresh id, the old entry file gone.
	id2, err := Apply([]string{"m1", "m2"}, "https://g.example", "C:\\k\\kyacode.exe")
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if id2 == id {
		t.Fatal("re-apply reused the id")
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("the previous entry file survived re-apply: %v", err)
	}

	// A foreign configuration takes over (the in-app window's write):
	// apply must refuse, and remove removes only our entry while the
	// foreign configuration survives.
	foreignID := "00000000-0000-4000-8000-0000000000aa"
	fm := meta{AppliedID: foreignID, Entries: []metaEntry{
		{ID: id2, Name: entryName},
		{ID: foreignID, Name: "user's own"},
	}}
	body, err := json.MarshalIndent(fm, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_meta.json"), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply([]string{"m1"}, "https://g.example", "C:\\k\\kyacode.exe"); err == nil || !strings.Contains(err.Error(), "not kyacode's") {
		t.Fatalf("apply over foreign = %v, want the foreign refusal", err)
	}
	if err := Remove(); err != nil {
		t.Fatalf("remove with our unapplied entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id2+".json")); !os.IsNotExist(err) {
		t.Fatal("our entry file survived remove")
	}
	state, applied, err = ReadState()
	if err != nil || state != StateForeign || applied != foreignID {
		t.Fatalf("state after remove = %v/%q/%v, want foreign/%s", state, applied, err, foreignID)
	}

	// Remove with nothing of ours: loud refusal.
	if err := Remove(); err == nil {
		t.Fatal("remove with a foreign-only library succeeded")
	}

	// Off from ours: both files gone, the app returns to first-party.
	// The library still holds the foreign configuration from the step
	// above, so the reset deletes _meta.json first - the refusal the
	// previous Apply hit is the correct product behavior, not a bug.
	if err := os.Remove(filepath.Join(dir, "_meta.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply([]string{"m1"}, "https://g.example", "C:\\k\\kyacode.exe"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(); err != nil {
		t.Fatalf("remove from ours: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_meta.json")); !os.IsNotExist(err) {
		t.Fatal("_meta.json survived a full remove")
	}
}
