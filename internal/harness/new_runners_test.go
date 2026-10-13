package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"kyacode/internal/gatewaytest"
	"kyacode/internal/harness/sandbox"

	"gopkg.in/yaml.v3"
)

// new_runners_test.go carries the per-runner wiring contracts that do not
// belong in one runner's file: the qwen env-only contract, droid's bare-
// model argv pin, the shell-file key-embedding guard (no sandbox file may
// carry the literal KYA_API_KEY env-var name), and the four-runner sandbox
// table (layout + reclaim + the keyInEnv credential channel). The filename
// keeps its history (it predates the runners it now names).

// TestQwenEnvRunnerContract pins the qwen env-only wiring (the table that
// historically covered hermes and dsh too — Round-5 audit: hermes no
// longer injects a bogus CUSTOM_BASE_URL (H-NEW1) and is a sandbox runner
// now, dsh is a sandbox runner with its apiKeyEnv schema, and claude's
// contract has its own file — so this table is qwen's).
func TestQwenEnvRunnerContract(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	model := "qwen3.8-27b"

	cases := []struct {
		spec     Spec
		wantEnv  []string
		wantArgs []string
	}{
		{
			spec:     qwenSpec,
			wantEnv:  []string{"OPENAI_BASE_URL=https://gw.example/v1", "OPENAI_API_KEY=sk-test-12345", "OPENAI_MODEL=qwen3.8-27b"},
			wantArgs: []string{"--auth-type", "openai", "--model", "qwen3.8-27b"},
		},
	}
	for _, c := range cases {
		if c.spec.Wiring != WiringEnv {
			t.Fatalf("%s: not env-wired", c.spec.Name)
		}
		env := c.spec.Env(ep, model)
		joined := strings.Join(env, "\n")
		for _, want := range c.wantEnv {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: env missing %q (got: %s)", c.spec.Name, want, joined)
			}
		}
		args := c.spec.ExtraArgs(model)
		for i, want := range c.wantArgs {
			if i >= len(args) || args[i] != want {
				t.Errorf("%s: ExtraArgs = %v, want %v", c.spec.Name, args, c.wantArgs)
				break
			}
		}
	}
}

// TestDroidModelArgsBareTheModel pins issue-02's verified contract: droid
// CLI (-m, droid 0.211.0) rejects "provider/model" combos for custom models
// ("Invalid model: kya/<id>") while the bare customModels[].model value
// passes the model gate — so the flag must carry the bare id, matching the
// settings default fixed in d12e15d. Launch-path extra args are still
// passed through verbatim; these are the runner's own leading args.
func TestDroidModelArgsBareTheModel(t *testing.T) {
	got := droidModelArgs("qwen3.8-27b")
	want := []string{"-m", "qwen3.8-27b"}
	if len(got) != len(want) {
		t.Fatalf("model args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("model args = %v, want %v (bare id, no kya/ prefix)", got, want)
		}
	}
	empty := droidModelArgs("")
	if empty != nil {
		t.Errorf("model args(\"\") = %v, want nil", empty)
	}
}

// TestShellFileRunner writes embed the REAL key (never a bare env var
// reference) — round-5 H-NEW2 regression guard: no config file may contain
// the literal string "KYA_API_KEY" (that's an env-var NAME, not a key).
func TestShellFileRunnerRealKeyEmbedded(t *testing.T) {
	withHome(t)
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-real-key-9876"}

	// The class-aware rule the closures declare at their buildSandbox
	// call sites (the Channel enum lives in the sandbox package):
	//   - FileOnly/Dual: the real token value MUST appear in the sandbox
	//     files (the file carries the credential), and the env-var NAME
	//     must not — a NAME in a file is a reference, and only the
	//     EnvRef/Dual classes may reference.
	//   - EnvRef: the value must appear NOWHERE in the files (it rides
	//     the process env only); the NAME is the credential reference.
	// The NAME literal is allowed only where a runner's schema
	// legitimately references it: codex's config.toml env_key and dsh's
	// settings.yaml apiKeyEnv. Corroboration per runner beyond this walk:
	// hermes/omp's zero-row pins (hermes_providers_test.go,
	// omp_env_test.go), codex's auth.json value pin (harness_test.go),
	// dsh's frozen env shape (wire_test.go).
	rows := map[string]struct {
		wantValue   bool     // the real token must appear in some sandbox file
		nameAllowed []string // sandbox-relative files where the NAME literal is the schema's reference
	}{
		"cline":    {wantValue: true},
		"droid":    {wantValue: true},
		"pi":       {wantValue: true},
		"openclaw": {wantValue: true},
		"omp":      {wantValue: true},
		"hermes":   {wantValue: true},
		"codex":    {wantValue: true, nameAllowed: []string{"config.toml"}},
		"dsh":      {wantValue: false, nameAllowed: []string{"settings.yaml"}},
	}
	// Registry-keyed, like the wiring matrix: a registered sandbox runner
	// with no row fails with the extend-the-table refusal — the
	// security-critical walk must never silently lose a runner (and a
	// stale row for a removed runner fails the count check below).
	runners := sandboxRunners()
	if len(rows) != len(runners) {
		t.Fatalf("key-channel walk rows = %d, want exactly one per sandbox runner (%d) — stale or missing row", len(rows), len(runners))
	}
	for _, s := range runners {
		tc, declared := rows[s.Name]
		if !declared {
			t.Fatalf("sandbox runner %s has no key-channel walk row; extend the table when adding a runner", s.Name)
		}
		t.Run(s.Name, func(t *testing.T) {
			// Drive the declared channel the launch boundary would: the
			// carriers receive the typed-degrade decision (the same shape
			// Resolve produces against an unreachable gateway with a model).
			w := buildWire(t, s, ep, "m1")

			// Walk the wire's own sandbox root — no env re-derivation. The
			// walk reads BEFORE the reclaim: after Cleanup the root is
			// gone and the guard would walk nothing and pass evidence-free.
			root := w.Root
			if root == "" {
				t.Fatalf("%s: wire carries no sandbox root", s.Name)
			}
			var valueIn, nameIn string
			filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				rel, _ := filepath.Rel(root, path)
				if strings.Contains(string(b), ep.Token) {
					valueIn = rel
				}
				if strings.Contains(string(b), sandbox.KeyEnvName) && !slices.Contains(tc.nameAllowed, rel) {
					nameIn = rel
				}
				return nil
			})
			w.Cleanup()
			if tc.wantValue && valueIn == "" {
				t.Errorf("%s: no sandbox file carries the real key — the runner's declared channel puts the credential in a file", s.Name)
			}
			if !tc.wantValue && valueIn != "" {
				t.Errorf("%s: sandbox file %s carries the key VALUE — an env-REFERENCE runner references the name and the value rides the process env only", s.Name, valueIn)
			}
			if nameIn != "" {
				t.Errorf("%s: sandbox file %s contains the env-var NAME literal outside the schema's reference site(s) %v (H-NEW2 shape)", s.Name, nameIn, tc.nameAllowed)
			}
		})
	}
}

// TestNewSandboxRunners verifies each config-file runner creates the
// expected sandbox layout and reclaims it on cleanup. Registry-keyed
// with HELD pointer rows: a registered sandbox runner with no entry
// fails with the extend-the-table refusal. Full rows carry the layout
// truth (wantFile/keyInEnv/dirEnv); pointer rows mark runners whose
// layout is genuinely pinned elsewhere (codex's auth.json+config.toml
// pair in harness_test.go, dsh's settings+patch in wire_test.go, hermes's
// config.yaml in hermes_providers_test.go, omp's agent/models.yml in
// omp_env_test.go) and are driven only for the prefix/reclaim contract
// the loop asserts for everyone.
func TestNewSandboxRunners(t *testing.T) {
	withHome(t)
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}

	sandboxCases := map[string]struct {
		prefix   string
		wantFile string // "" = pointer row: the layout is pinned elsewhere
		wantKey  string
		keyInEnv bool // if false, key is in the config file not env
		dirEnv   string
	}{
		"cline":    {prefix: "cline", wantFile: "data/settings/providers.json", wantKey: "sk-test-12345", keyInEnv: false, dirEnv: "CLINE_DATA_DIR"},
		"droid":    {prefix: "droid", wantFile: ".factory/settings.json", wantKey: "sk-test-12345", keyInEnv: true, dirEnv: "HOME"},
		"pi":       {prefix: "pi", wantFile: ".pi/agent/models.json", wantKey: "sk-test-12345", keyInEnv: true, dirEnv: "HOME"},
		"openclaw": {prefix: "openclaw", wantFile: ".openclaw/openclaw.json", wantKey: "sk-test-12345", keyInEnv: true, dirEnv: "HOME"},
		"codex":    {prefix: "codex"},  // layout pinned in harness_test.go (auth.json + config.toml)
		"dsh":      {prefix: "dsh"},    // layout pinned in wire_test.go (patch pair + frozen env; NO redirect row — its env is the credential row alone)
		"hermes":   {prefix: "hermes"}, // layout pinned in hermes_providers_test.go
		"omp":      {prefix: "omp"},    // layout pinned in omp_env_test.go (PI_CODING_AGENT_DIR carries the agent/ subdir)
	}

	runners := sandboxRunners()
	if len(sandboxCases) != len(runners) {
		t.Fatalf("layout rows = %d, want exactly one per sandbox runner (%d) — stale or missing row", len(sandboxCases), len(runners))
	}

	for _, r := range runners {
		c, declared := sandboxCases[r.Name]
		if !declared {
			t.Fatalf("sandbox runner %s has no layout row; extend the table when adding a runner", r.Name)
		}
		if r.Wiring != WiringSandbox || r.SandboxPrefix != c.prefix {
			t.Fatalf("%s: declared wiring/prefix %v/%q does not match the case row (sandbox/%q)", r.Name, r.Wiring, r.SandboxPrefix, c.prefix)
		}
		w := buildWire(t, r, ep, "test-model")
		joined := strings.Join(w.Env, "\n")

		// The redirect row is asserted for FULL rows only — pointer rows'
		// redirects live in wire_test.go's wantRedirect map (and dsh has
		// none: its env is the credential row alone).
		dir := w.Root
		if dir == "" {
			t.Fatalf("%s: wire carries no sandbox root (env: %s)", r.Name, joined)
		}
		if c.dirEnv != "" && !slices.Contains(w.Env, c.dirEnv+"="+dir) {
			t.Fatalf("%s: env missing the exact %s=<sandbox root> row (got: %s)", r.Name, c.dirEnv, joined)
		}

		// Pointer rows end here: their layouts are pinned in their own
		// files (named in the table above).
		if c.wantFile == "" {
			continue
		}

		sandboxPath := filepath.Join(dir, c.wantFile)
		data, err := os.ReadFile(sandboxPath)
		if err != nil {
			t.Fatalf("%s: read %s: %v", r.Name, c.wantFile, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("%s: config is not valid JSON: %v", r.Name, err)
		}
		if !strings.Contains(string(data), c.wantKey) {
			t.Errorf("%s: config file missing key %q", r.Name, c.wantKey)
		}
		_ = parsed

		// keyInEnv (F1 residue): the column was declared but never asserted —
		// make it live. The Dual/EnvRef runners' child env carries EXACTLY ONE
		// KYA_API_KEY row; the FileOnly runner's carries ZERO. Bridge class
		// (declared test data → observed row); the values are test truth, not
		// re-derived from the spec (no config file carries the literal
		// variable name — TestShellFileRunnerRealKeyEmbedded).
		keyRows := 0
		for _, row := range w.Env {
			if strings.HasPrefix(row, sandbox.KeyEnvName+"=") {
				keyRows++
			}
		}
		if c.keyInEnv && keyRows != 1 {
			t.Errorf("%s: child env carries %d %s rows, want exactly one (keyInEnv=true): %v", r.Name, keyRows, sandbox.KeyEnvName, w.Env)
		} else if !c.keyInEnv && keyRows != 0 {
			t.Errorf("%s: child env carries %d %s rows, want zero (keyInEnv=false, FileOnly): %v", r.Name, keyRows, sandbox.KeyEnvName, w.Env)
		}

		if runtime.GOOS == "windows" && c.dirEnv == "HOME" {
			// Emission pin (ticket 01 Q6(d), review round): the HOME-
			// replacing runners must still APPEND the module's redirect
			// triad — the module test pins the triad's shape, this pin
			// keeps the call site. Without it a dropped append would
			// sail through on Windows and the harness would write its
			// key-bearing config to the real user profile. The rows are
			// asserted as exact pairs pointing at the recorded root.
			drive := filepath.VolumeName(dir)
			if !slices.Contains(w.Env, "USERPROFILE="+dir) ||
				!slices.Contains(w.Env, "HOMEDRIVE="+drive) ||
				!slices.Contains(w.Env, "HOMEPATH="+strings.TrimPrefix(dir, drive)) {
				t.Errorf("%s: env %v, want the home triad exact rows pointing at the sandbox %q", r.Name, w.Env, dir)
			}
		}

		w.Cleanup()
		if _, err := os.Stat(sandboxPath); !os.IsNotExist(err) {
			t.Errorf("%s: sandbox not reclaimed after cleanup: %s still exists", r.Name, sandboxPath)
		}
	}
}

// TestDSHKeepsNoSecondArgsChannel pins the seam deletion: the patch pair
// rides Wire.Args only (pinned on the wire in wire_test.go), so the old
// scavenging channel — ArgsFromEnv reading the pseudo-env carrier — must
// stay deleted. In spec data the guarantee is structural: a sandbox
// runner MUST NOT declare ExtraArgs at all (Register rejects the dead
// declaration), so only the declared-nil state of dsh's spec is pinned
// here; the reverse side is TestRegisterRejectsIncoherentSpecs.
func TestDSHKeepsNoSecondArgsChannel(t *testing.T) {
	if extra := dshSpec.ExtraArgs; extra != nil {
		t.Errorf("dsh spec declares ExtraArgs (%T) — the patch pair rides Wire.Args and the launch boundary can never read a sandbox runner's ExtraArgs", extra)
	}
}

// TestHermesAndDSHAreSandboxRunners pins the round-5 dump: hermes config via
// sandbox config.yaml (never CUSTOM_BASE_URL), dsh via settings+patch files.
// The must-be-a-Launcher / must-not-be-an-EnvRunner capability snapshot is
// gone with the interfaces — the wiring class is declared data now, pinned
// registry-wide by the Spec matrix test.
func TestHermesAndDSHAreSandboxRunners(t *testing.T) {
	withHome(t)
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}

	if hermesSpec.Wiring != WiringSandbox || dshSpec.Wiring != WiringSandbox {
		t.Fatalf("hermes/dsh declared wiring changed (wants sandbox: hermes=%v dsh=%v)", hermesSpec.Wiring, dshSpec.Wiring)
	}

	// hermes sandbox content: config.yaml must carry base_url + the REAL
	// key (readme-audit issue: placeholder-api-key — the placeholder carried
	// no credential, leaving hermes unauthenticated against the gateway).
	w, err := hermesSpec.BuildEnv(ep, "m1")
	if err != nil {
		t.Fatalf("hermes BuildEnv: %v", err)
	}
	defer w.Cleanup()
	sandbox := w.Root
	if sandbox == "" {
		t.Fatalf("hermes wire carries no sandbox root: %+v", w)
	}
	b, err := os.ReadFile(filepath.Join(sandbox, "config.yaml"))
	if err != nil {
		t.Fatalf("hermes config.yaml: %v", err)
	}
	content := string(b)
	for _, want := range []string{"base_url:", "kya-launch", "api_key:", ep.Token} {
		if !strings.Contains(content, want) {
			t.Errorf("hermes config.yaml missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "api_key: kyacode") {
		t.Error("hermes config.yaml must not carry the placeholder as credential")
	}
	if strings.Contains(content, "CUSTOM_BASE_URL") {
		t.Error("hermes config.yaml must not echo the fabricated var name")
	}
}

// TestDefaultModelDegradesToFirstCatalogID pins the readme-audit fix
// (placeholder-default-model): with no CLI model and a live gateway, droid,
// openclaw and dsh pin the FIRST REAL catalog id as the default instead of
// the kya-model placeholder; with no valid id at all the placeholder is
// still the last resort so the config shape stays valid.
func TestDefaultModelDegradesToFirstCatalogID(t *testing.T) {
	withHome(t) // dsh's carrier test reads its sandbox from ~/.kyacode
	srv := gatewaytest.Models(t, "sk-test-12345", []string{"zeta-model", "alpha-model"})
	ep := Endpoint{BaseURL: srv.URL, Token: "sk-test-12345"}

	droidWire := buildWire(t, droidSpec, ep, "")
	b, err := os.ReadFile(filepath.Join(droidWire.Root, ".factory", "settings.json"))
	if err != nil {
		t.Fatalf("droid settings.json: %v", err)
	}
	// Exact-field assertion: a substring check would also be satisfied by
	// the customModels entries (each is the bare id), so a regression to
	// the kya-model placeholder would slip through. The org-style default
	// carrier is sessionDefaultSettings.model (the top-level 'model' key
	// is a user-settings field droid's own examples don't rely on).
	var droidSettings struct {
		Model                  string `json:"model"`
		SessionDefaultSettings struct {
			Model string `json:"model"`
		} `json:"sessionDefaultSettings"`
	}
	if err := json.Unmarshal(b, &droidSettings); err != nil {
		t.Fatalf("droid settings.json not valid JSON: %v (%s)", err, b)
	}
	if droidSettings.SessionDefaultSettings.Model != "alpha-model" {
		t.Errorf("droid sessionDefaultSettings.model = %q, want the first real catalog id \"alpha-model\"", droidSettings.SessionDefaultSettings.Model)
	}
	if droidSettings.Model != "" {
		t.Errorf("top-level 'model' must not be written (org default lives in sessionDefaultSettings); got %q", droidSettings.Model)
	}

	ocWire := buildWire(t, openclawSpec, ep, "")
	b, err = os.ReadFile(filepath.Join(ocWire.Root, ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatalf("openclaw.json: %v", err)
	}
	var ocSettings struct {
		Agents struct {
			Defaults struct {
				Model struct {
					Primary string `json:"primary"`
				} `json:"model"`
			} `json:"defaults"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(b, &ocSettings); err != nil {
		t.Fatalf("openclaw.json not valid JSON: %v (%s)", err, b)
	}
	if ocSettings.Agents.Defaults.Model.Primary != "kya/alpha-model" {
		t.Errorf("openclaw agents.defaults.model.primary = %q, want \"kya/alpha-model\" (its provider key is \"kya\")", ocSettings.Agents.Defaults.Model.Primary)
	}

	dshWire := buildWire(t, dshSpec, ep, "")
	settingsData := []byte(carrierContent(t, "dsh", dshWire.Root))
	var dshSettings struct {
		AgentDefaultModel struct {
			Provider string `yaml:"provider"`
			Model    string `yaml:"model"`
		} `yaml:"agent-default-model"`
	}
	if err := yaml.Unmarshal(settingsData, &dshSettings); err != nil {
		t.Fatalf("dsh settings.yaml not valid yaml: %v (%s)", err, settingsData)
	}
	if dshSettings.AgentDefaultModel.Model != "alpha-model" {
		t.Errorf("dsh agent-default-model.model = %q, want \"alpha-model\"", dshSettings.AgentDefaultModel.Model)
	}
}

// buildWire drives the spec's declared sandbox build channel the way the
// launch boundary would — a carrier receives the launch's resolved
// decision (Resolve, degrade included), a plain launcher builds without
// any catalog fetch — and returns the whole Wire (caller reclaims —
// usually t.Cleanup(w.Cleanup); anything the assertion needs rides on it).
func buildWire(t *testing.T, s Spec, ep Endpoint, model string) Wire {
	t.Helper()
	if s.Wiring != WiringSandbox {
		t.Fatalf("%s: not a sandbox-wired spec", s.Name)
	}
	var w Wire
	var err error
	if s.CarrierBuild != nil {
		w, err = s.CarrierBuild(ep, model, Resolve(ep, model))
	} else {
		w, err = s.BuildEnv(ep, model)
	}
	if err != nil {
		t.Fatalf("%s: build: %v", s.Name, err)
	}
	t.Cleanup(w.Cleanup)
	return w
}

// TestPinVerdictUnit covers the pin rule's decision table directly
// (white-box, no socket): the typed model always wins; a fetched catalog
// yields its first real id; a catalog serving ONLY a placeholder-named
// id still pins it (an explicitly served id is real, whatever it is
// named); a degrade with no CLI model pins nothing.
func TestPinVerdictUnit(t *testing.T) {
	cases := []struct {
		name string
		cat  Catalog
		want string
		ok   bool
	}{
		{"typed model wins", typedCatalog("cli"), "cli", true},
		{"placeholder last resort", Catalog{ids: []string{PlaceholderModelID}, pin: PlaceholderModelID}, PlaceholderModelID, true},
		{"first real sorted", Catalog{ids: []string{"zeta", "alpha"}, pin: "alpha"}, "alpha", true},
		{"degraded no model: no pin", degradedCatalog(), "", false},
	}
	for _, c := range cases {
		bare, scoped, ok := c.cat.Pin()
		if ok != c.ok || bare != c.want {
			t.Errorf("%s: Pin = (%q, %v), want (%q, %v)", c.name, bare, ok, c.want, c.ok)
		}
		if ok && scoped != scopedModelID(bare) {
			t.Errorf("%s: scoped form = %q, want the single compose of the bare id", c.name, scoped)
		}
	}
}
