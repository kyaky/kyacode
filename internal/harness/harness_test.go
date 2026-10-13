package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kyacode/internal/harness/sandbox"
)

// withHome isolates a test in its own fake $HOME so the sandbox root
// (and its ~/.kyacode parent) stay hermetic. USERPROFILE is set to the
// same dir: the sandbox package's config dir follows os.UserHomeDir,
// which prefers USERPROFILE on Windows.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestCodexBuildEnvSandboxContainsKeyAndCleansUp(t *testing.T) {
	withHome(t)
	ep := Endpoint{
		BaseURL: "https://127.0.0.1:1",
		Token:   "sk-1-2-3-MARGIN",
	}

	w, err := buildCodexWirePair(t, ep, "glm-5.3-flash-nvfp4")
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	joined := strings.Join(w.Env, "\n")
	if !strings.Contains(joined, "CODEX_HOME=") {
		t.Fatalf("env missing CODEX_HOME: %q", joined)
	}
	if !strings.Contains(joined, "KYA_API_KEY="+ep.Token) {
		t.Fatalf("env missing KYA_API_KEY: %q", joined)
	}

	// The sandbox dir must exist on disk with the real key in auth.json
	// (exactly what cleanup must be able to reclaim). The wire carries the
	// root as a fact, so no globbing of the real home is needed (on Windows
	// os.UserHomeDir ignores HOME, so a writable-dir glob races other
	// tests and concurrent launches).
	dir := w.Root
	authBytes, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	if !strings.Contains(string(authBytes), ep.Token) {
		t.Fatalf("auth.json does not embed the real token: %q", authBytes)
	}

	w.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the sandbox dir; stat err = %v", err)
	}
}

func TestCodexBuildEnvCleansUpOnFailedConfigWrite(t *testing.T) {
	// The read-only-dir premise is Unix DAC; on Windows Chmod only toggles
	// the read-only bit and 0500 on a dir enforces nothing.
	if runtime.GOOS == "windows" {
		t.Skip("read-only dir enforcement is unix-only")
	}
	home := withHome(t)
	// Force the config.toml write to fail by making ~/.kyacode read-only.
	kya := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(kya, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(kya, 0o700)

	w, err := buildCodexWirePair(t, Endpoint{Token: "sk-x"}, "")
	if err == nil {
		if os.Getuid() == 0 {
			w.Cleanup()
			t.Skip("running as root; read-only dir not enforceable")
		}
		t.Fatal("BuildEnv must fail hard when the sandbox cannot be built; it must NOT fall back to the real ~/.codex")
	}
	if w.Cleanup != nil {
		t.Fatal("the failed build's wire must carry no cleanup (nothing to reclaim)")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".kyacode", "codex-*"))
	if len(matches) != 0 {
		t.Fatalf("failed BuildEnv must not leave sandbox residue: %v", matches)
	}
}

// The must-marshal family (sandbox.WriteJSON/WriteYAML; opencode's
// mustMarshalJSON for the env-row string) panics on an unmarshalable
// value instead of swallowing it — the sibling failure mode (returning a
// placeholder or empty config) silently drops the key from the child's
// view, which is the exact bug class this family exists to prevent. The
// panics are also what keeps buildSandbox's success-gated panic-reclaim
// gate LIVE for the sandbox members.
func TestMustStyleJSONFamilyPanicsOnUnmarshalableValue(t *testing.T) {
	assertPanics := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s swallowed an unmarshalable value; the must-idiom contract is a panic, not silence", name)
			}
		}()
		f()
	}
	home := withHome(t)
	sb, err := sandbox.New("probe", sandbox.Key(sandbox.FileOnly, "sk-test"))
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	t.Cleanup(sb.Cleanup)
	unmarshalable := map[string]any{"f": func() {}}
	assertPanics("WriteJSON", func() { _ = sb.WriteJSON("x.json", unmarshalable) })
	assertPanics("WriteYAML", func() { _ = sb.WriteYAML("x.yml", unmarshalable) })
	assertPanics("mustMarshalJSON", func() { _ = mustMarshalJSON(unmarshalable) })
	_ = home
}

// In-flight BuildEnv failure (the sandbox dir already exists when the
// failure lands): codex's config validation rejects an unembeddable base
// URL AFTER the sandbox root is created — buildSandbox's failure path
// must reclaim the half-built sandbox so a key-bearing dir never leaks.
func TestCodexBuildEnvCleansUpOnInFlightValidationFailure(t *testing.T) {
	home := withHome(t)
	ep := Endpoint{BaseURL: "https://cannot-embed\n.example", Token: "sk-test-12345"}
	w, err := buildCodexWirePair(t, ep, "m1")
	if err == nil {
		t.Fatal("an unembeddable base URL must fail codex BuildEnv")
	}
	if w.Cleanup != nil {
		t.Fatal("the failed build's wire must carry no cleanup (nothing left to reclaim)")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".kyacode", "codex-*"))
	if len(matches) != 0 {
		t.Fatalf("in-flight BuildEnv failure left sandbox residue (holds the real key): %v", matches)
	}
}

// The failed-BuildEnv residue contract across runner families, beyond
// codex: every launcher whose sandbox build fails (here: the read-only
// ~/.kyacode fixture kills sandbox.New before any dir exists) must return
// a nil cleanup and leave zero residue under ~/.kyacode. A launcher that
// leaks on failure forfeits the zero-residue claim the launch contract
// is built on. Unix-DAC fixture, like the codex test above.
// The wiring closures run must-idiom code that can panic; a panic must
// reclaim the key-bearing dir exactly like an error return — the
// success-gated defer in buildSandbox is what makes that hold, and this
// pin keeps the cleanup from regressing into a return-path-only shape
// (a bare err-branch Cleanup lets a panic leak the dir to the orphan
// sweep's 24h mercy instead of reclaiming it now).
func TestBuildSandboxReclaimsOnWiringPanic(t *testing.T) {
	home := withHome(t)
	func() {
		defer func() { _ = recover() }() // the must-idiom panic is the documented behavior
		_, _ = buildSandbox("probe", sandbox.Dual, "sk-test-12345", func(sb *sandbox.Sandbox) ([]string, []string, error) {
			if err := sb.Write("auth.json", []byte(`{"key":"sk-test-12345"}`)); err != nil {
				return nil, nil, err
			}
			panic("wiring panic after the key landed")
		})
	}()
	matches, _ := filepath.Glob(filepath.Join(home, ".kyacode", "probe-*"))
	if len(matches) != 0 {
		t.Fatalf("a wiring panic left the key-bearing sandbox behind: %v", matches)
	}
}

func TestLauncherFamiliesBuildEnvFailureLeavesNoResidue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only dir enforcement is unix-only")
	}
	// Registry-keyed, with codex as an OWNED exclusion: its failure paths
	// (read-only dir at TestCodexBuildEnvCleansUpOnFailedConfigWrite,
	// in-flight validation at TestCodexBuildEnvCleansUpOnInFlightValidation
	// Failure) are pinned in the dedicated codex tests above, so the
	// family map carries the other seven. A registered sandbox runner
	// with no entry fails with the extend-the-table refusal — the
	// failure contract must never silently lose a runner.
	builds := map[string]func(Endpoint, string) (Wire, error){
		// Carriers ride their declared channel; the catalog value is
		// irrelevant here — sandbox.New fails before any wiring runs.
		"omp":      func(ep Endpoint, m string) (Wire, error) { return ompSpec.CarrierBuild(ep, m, degradedCatalog()) },
		"pi":       func(ep Endpoint, m string) (Wire, error) { return piSpec.CarrierBuild(ep, m, degradedCatalog()) },
		"droid":    func(ep Endpoint, m string) (Wire, error) { return droidSpec.CarrierBuild(ep, m, degradedCatalog()) },
		"cline":    clineSpec.BuildEnv,
		"dsh":      func(ep Endpoint, m string) (Wire, error) { return dshSpec.CarrierBuild(ep, m, degradedCatalog()) },
		"hermes":   hermesSpec.BuildEnv,
		"openclaw": func(ep Endpoint, m string) (Wire, error) { return openclawSpec.CarrierBuild(ep, m, degradedCatalog()) },
	}
	for _, s := range sandboxRunners() {
		build, declared := builds[s.Name]
		if !declared {
			if s.Name == "codex" {
				continue // pinned by the dedicated codex failure tests above
			}
			t.Fatalf("sandbox runner %s has no failure-map entry; extend the map when adding a runner", s.Name)
		}
		name := s.Name
		t.Run(name, func(t *testing.T) {
			home := withHome(t)
			kya := filepath.Join(home, ".kyacode")
			if err := os.MkdirAll(kya, 0o500); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(kya, 0o700)
			if os.Getuid() == 0 {
				t.Skip("running as root; read-only dir not enforceable")
			}
			w, err := build(Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
			if err == nil {
				t.Fatal("BuildEnv must fail hard when the sandbox cannot be created")
			}
			if w.Cleanup != nil {
				t.Fatal("the failed build's wire must carry no cleanup")
			}
			matches, _ := filepath.Glob(filepath.Join(kya, name+"-*"))
			if len(matches) != 0 {
				t.Fatalf("failed BuildEnv left sandbox residue: %v", matches)
			}
		})
	}
}

func TestOpenAIBaseAppendsV1OnceAndTrimsSlash(t *testing.T) {
	cases := map[string]string{
		"https://gw.example":       "https://gw.example/v1",
		"https://gw.example/":      "https://gw.example/v1",
		"https://gw.example/v1":    "https://gw.example/v1",
		"https://gw.example/v1/":   "https://gw.example/v1", // trailing slash trimmed before /v1 check
		"https://gw.example/v1/v1": "https://gw.example/v1/v1",
	}
	for in, want := range cases {
		ep := Endpoint{BaseURL: in}
		if got := ep.OpenAIBase(); got != want {
			t.Errorf("OpenAIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindInSymlinkedFallbackPathResolves(t *testing.T) {
	withHome(t) // hermetic HOME: the fallback must resolve inside the temp home
	// The fallback candidate homePaths builds carries the platform suffix
	// (.exe on Windows), so the link must be created AT that exact path —
	// a bare `claude` link tests a path findIn never probes. The target is
	// stat-only: findIn never executes it.
	candidates := homePaths("claude", ".local/bin")
	if len(candidates) == 0 {
		t.Fatal("homePaths built no fallback candidate")
	}
	link := candidates[0]
	target := link + ".real"
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := exec.LookPath("claude"); err == nil {
		t.Skip("a real `claude` is already on PATH; the fallback branch would not run")
	}
	// Not on PATH (tempdir is not in it), so the fallback path branch runs.
	got, ok := findIn("claude", candidates)
	if !ok {
		t.Fatal("findIn must resolve a symlinked fallback path")
	}
	if got != link {
		t.Fatalf("findIn = %q, want %q", got, link)
	}
}

// TestLaunchRejectsRunnerWithNoEnvContract is superseded: the capability
// ladder (Runner → EnvRunner/Launcher probing) died with ticket 04 — the
// undeclared-wiring fail-closed contract (same error sentence) now pins
// Spec data at the launch seam in spec_test.go
// (TestLaunchRejectsAnUndeclaredWiring), and coherence is guaranteed at
// Register.

// The token embedded in auth.json must survive quoting round-trips: a key
// containing a double quote, a backslash, or a control character must not
// corrupt the sandbox JSON (judge: mutation M14 — dropping the escape
// produced passing tests even though auth.json would be unparseable).
func TestCodexQuoteJSONSurvivesHostileToken(t *testing.T) {
	withHome(t)
	ep := Endpoint{
		BaseURL: "https://127.0.0.1:1",
		Token:   `sk-"weird"\` + string([]byte{0x01}) + " key",
	}
	w, err := buildCodexWirePair(t, ep, "")
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	defer w.Cleanup()
	dir := w.Root
	authBytes, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`
	}
	if err := json.Unmarshal(authBytes, &parsed); err != nil {
		t.Fatalf("auth.json must be parseable JSON for hostile tokens: %v (%s)", err, authBytes)
	}
	if parsed.OpenAIAPIKey != ep.Token {
		t.Fatalf("auth.json key round-trip failed: got %q, want %q", parsed.OpenAIAPIKey, ep.Token)
	}
}

// stubCodexHarvest replaces the entry-harvest seam with a deterministic
// stub for the test's duration: the real harvest spawns the installed
// codex binary, which hermetic tests must neither require nor pay for.
func stubCodexHarvest(t *testing.T) {
	t.Helper()
	original := codexCatalogEntryHarvest
	codexCatalogEntryHarvest = func(string) (map[string]any, bool) {
		return map[string]any{
			"slug":         "gpt-6-sol",
			"visibility":   "list",
			"priority":     3,
			"display_name": "GPT-6 Sol",
			"shell_type":   "unified_exec",
			"model_messages": map[string]any{
				"instructions_template": "You are Codex, the harvested test template.",
			},
		}, true
	}
	t.Cleanup(func() { codexCatalogEntryHarvest = original })
}

// buildCodexWirePair is the codex wire builder for tests that drive the
// (Wire, error) channel directly: the launch boundary's carrier handover —
// Resolve degrades instantly against the dead probe endpoint — with the
// harvest stubbed so the build stays hermetic.
func buildCodexWirePair(t *testing.T, ep Endpoint, model string) (Wire, error) {
	t.Helper()
	stubCodexHarvest(t)
	return codexSpec.CarrierBuild(ep, model, Resolve(ep, model))
}

// wantCodexWiring is the expected wiring pair list, spelled independently
// of codexWiringPairs so a wiring change must consciously touch both —
// the codex tests consume it byte-for-byte (TOML rendering) and
// element-for-element (argv rendering). catalogPath non-empty expects the
// model_catalog_json pair at its wiring position (after model).
func wantCodexWiring(model, catalogPath string) [][2]string {
	wants := [][2]string{
		{"model", fmt.Sprintf("%q", model)},
	}
	if catalogPath != "" {
		wants = append(wants, [2]string{"model_catalog_json", fmt.Sprintf("%q", catalogPath)})
	}
	return append(wants,
		[][2]string{
			{"model_provider", `"kya"`},
			{"forced_login_method", `"api"`},
			{"web_search", `"disabled"`},
			{"features.daemon_auto_start", "false"},
			{"features.plugins", "false"},
			{"model_providers.kya.name", `"KYA Gateway"`},
			{"model_providers.kya.base_url", `"https://127.0.0.1:1/v1"`},
			{"model_providers.kya.wire_api", `"responses"`},
			{"model_providers.kya.env_key", fmt.Sprintf("%q", sandbox.KeyEnvName)},
		}...)
}

// codexWantArgs renders the hand-spelled want table the way the wiring
// does, so the argv pin test and wire_test's runner-argv table share one
// independent baseline instead of two copies that can drift.
func codexWantArgs(model, catalogPath string) []string {
	var args []string
	for _, p := range wantCodexWiring(model, catalogPath) {
		args = append(args, "-c", p[0]+"="+p[1])
	}
	return args
}

// buildCodexWire is the shared carrier build for the codex wiring tests.
func buildCodexWire(t *testing.T) Wire {
	t.Helper()
	withHome(t)
	w, err := buildCodexWirePair(t, Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}, "m1")
	if err != nil {
		t.Fatalf("codex CarrierBuild: %v", err)
	}
	t.Cleanup(w.Cleanup)
	return w
}

// F6/codex residue (the pre-existing gap ticket 01 records as out of scope):
// codex's INTERNAL two-literal equivalence. The written config.toml's
// `model_provider = "…"` value must EQUAL the `[model_providers.…]` table
// key (the two "kya" literals the wiring emits); drift breaks codex's own
// provider lookup. Independent of providerScopePrefix — codex is
// prefix-independent (no scope in any codex row), so this pin is a
// different invariant than the scoped-composer bridge.
func TestCodexConfigModelProviderMatchesTheProvidersTableKey(t *testing.T) {
	w := buildCodexWire(t)
	b, err := os.ReadFile(filepath.Join(w.Root, "config.toml"))
	if err != nil {
		t.Fatalf("codex config.toml read: %v", err)
	}
	text := string(b)
	var modelProvider, tableKey string
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "model_provider = "); ok {
			modelProvider = strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "[model_providers."); ok {
			tableKey = strings.TrimSuffix(v, "]")
		}
	}
	if modelProvider == "" || tableKey == "" {
		t.Fatalf("codex config.toml lacks model_provider or a [model_providers.…] table:\n%s", text)
	}
	if modelProvider != tableKey {
		t.Errorf("codex config.toml: model_provider = %q but the table key is [model_providers.%s] — the internal pair must match", modelProvider, tableKey)
	}
}

// F6/codex residue (the 0.162 breakage): the config surface the sandbox
// depends on, pinned by a SECTION-AWARE parse of the generated file rather
// than substring matches. Three invariants, each one a codex upgrade that
// broke launches: web_search rides the ROOT as the enum switch "disabled"
// (upstream parses the boolean [tools] key and deliberately discards it);
// the daemon and plugins features are OFF at the root (0.157's daemon
// default detaches env_key from the wired env, and the plugins marketplace
// clone races Cleanup past child exit); and every root assignment precedes
// the first table header — TOML silently re-homes a later bare key into
// the current table, which is how the web_search pin went inert the first
// time it was appended after [model_providers.kya].
func TestCodexConfigTomlSectionParsePinsTheSurface(t *testing.T) {
	w := buildCodexWire(t)
	b, err := os.ReadFile(filepath.Join(w.Root, "config.toml"))
	if err != nil {
		t.Fatalf("codex config.toml read: %v", err)
	}
	// A two-state scanner is enough for the shape codexConfigToml
	// writes: flat `key = value` lines under an optional table header.
	// Anything richer (arrays, inline tables, nested subtables) fails the
	// scan loudly instead of passing silently — a carrier change that
	// rich deserves a conscious test update.
	root := map[string]string{}
	tables := map[string]map[string]string{}
	current := root
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				t.Fatalf("config.toml line %d: malformed table header %q", i+1, line)
			}
			name := strings.Trim(line, "[]")
			if _, dup := tables[name]; dup {
				t.Fatalf("config.toml line %d: duplicate table [%s]", i+1, name)
			}
			current = map[string]string{}
			tables[name] = current
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("config.toml line %d: not a key = value line: %q", i+1, line)
		}
		key = strings.TrimSpace(key)
		if _, dup := current[key]; dup {
			t.Errorf("config.toml line %d: duplicate key %q in its section", i+1, key)
		}
		current[key] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	for _, want := range wantCodexWiring("m1", filepath.Join(w.Root, "models.json")) {
		value := strings.Trim(want[1], `"`)
		if name, ok := strings.CutPrefix(want[0], "model_providers.kya."); ok {
			provider := tables["model_providers.kya"]
			if provider == nil {
				t.Fatalf("config.toml: no [model_providers.kya] table:\n%s", b)
			}
			if got := provider[name]; got != value {
				t.Errorf("config.toml [model_providers.kya] %s = %q, want %q", name, got, value)
			}
			continue
		}
		if got := root[want[0]]; got != value {
			t.Errorf("config.toml root %s = %q, want %q (an empty got means the key is missing or moved under a table — the exact TOML trap)", want[0], got, value)
		}
	}
	if len(tables) != 1 {
		t.Fatalf("config.toml: want exactly one table, got %d: %v", len(tables), tables)
	}
}

// F6/codex residue (the 0.162 trusted-project hijack): the runtime-layer
// pins. The child argv must carry EVERY wiring pair as `-c key=value` —
// the one layer that outranks the cwd/tree/repo project layers which
// replaced the wired model with the operator's personal one the moment a
// launch's directory tree was trusted. A pin missing here means that fact
// silently reverts to whatever the nearest .codex/config.toml says.
func TestCodexRuntimeArgsPinTheWiring(t *testing.T) {
	w := buildCodexWire(t)
	wantPairs := wantCodexWiring("m1", filepath.Join(w.Root, "models.json"))
	if len(w.Args) != 2*len(wantPairs) {
		t.Fatalf("codex argv pin count = %d elements, want %d (-c pairs for every wiring fact): %v",
			len(w.Args), 2*len(wantPairs), w.Args)
	}
	for i, want := range wantPairs {
		flag, kv := w.Args[2*i], w.Args[2*i+1]
		if flag != "-c" {
			t.Fatalf("codex argv element %d = %q, want the -c flag", 2*i, flag)
		}
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("codex argv element %d = %q, want key=value", 2*i+1, kv)
		}
		if key != want[0] || value != want[1] {
			t.Errorf("codex argv pin %d: %s=%s, want %s=%s", i, key, value, want[0], want[1])
		}
	}
}
