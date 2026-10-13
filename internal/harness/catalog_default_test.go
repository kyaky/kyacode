package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"kyacode/internal/gatewaytest"

	"gopkg.in/yaml.v3"
)

// The catalog decision contract: the launch boundary resolves ONE Model
// catalog decision per launch (Resolve — opaque, never re-derived by a
// carrier) and hands it to every declared Catalog carrier's channel
// (CarrierBuild / CarrierEnv). The sentinel-row contract's two halves —
// the degraded LIST keeps the placeholder row, the default PIN is
// withheld on degrade — are decided inside Resolve, so the per-runner
// pins here drive the declared carrier channels with catalog VALUES and
// read exactly each runner's own pin site. qwen remains the documented
// non-carrier with a model-bearing env row (its auth-inference), pinned
// below; hermes is a non-carrier whose own model wiring is pinned in
// hermes_providers_test.go.

// degradedCatalog white-boxes Resolve's degrade shape (unreachable
// gateway, no CLI model): the sentinel row alone, no pin. The bridge to
// the real Resolve is pinned in TestResolveDegradeMatchesTheWhiteBox.
func degradedCatalog() Catalog {
	return Catalog{ids: []string{PlaceholderModelID}}
}

// typedCatalog white-boxes Resolve's degrade-with-CLI-model shape: the
// typed model is the only row AND the pin (a degraded fetch is
// overridden by a typed model).
func typedCatalog(m string) Catalog {
	return Catalog{ids: []string{m}, pin: m}
}

// pinProbe builds one runner's wiring from an explicit catalog decision
// and returns (primary site value, secondary site value, cleanup); ""
// means the site is ABSENT from the config — not an empty pin, which no
// runner ever writes.
type pinProbe struct {
	name  string
	build func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func())
}

func catalogPinProbes(t *testing.T) []pinProbe {
	t.Helper()
	return []pinProbe{
		{
			name: "omp",
			build: func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func()) {
				withHome(t)
				w, err := ompSpec.CarrierBuild(ep, model, cat)
				if err != nil {
					t.Fatalf("omp CarrierBuild: %v", err)
				}
				// omp's settings live under agent/ inside the sandbox the
				// wire recorded. The env redirect rows themselves are
				// pinned registry-wide in TestLaunchersWiresCarryTheLaunchFacts.
				agentDir := filepath.Join(w.Root, "agent")
				cleanup := w.Cleanup
				// Only absence counts as "no pin" ("no pin" means "no
				// file" since #08) — any other read error is a broken
				// build, not a pass.
				b, rerr := os.ReadFile(filepath.Join(agentDir, "config.yml"))
				if rerr != nil {
					if !os.IsNotExist(rerr) {
						t.Fatalf("omp config.yml read: %v", rerr)
					}
					return "", "", cleanup
				}
				var cfg map[string]any
				if err := yaml.Unmarshal(b, &cfg); err != nil {
					t.Fatalf("omp config.yml parse: %v\n%s", err, b)
				}
				roles, _ := cfg["modelRoles"].(map[string]any)
				pin, _ := roles["default"].(string)
				return pin, "", cleanup
			},
		},
		{
			name: "openclaw",
			build: func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func()) {
				withHome(t)
				w, err := openclawSpec.CarrierBuild(ep, model, cat)
				if err != nil {
					t.Fatalf("openclaw CarrierBuild: %v", err)
				}
				home := w.Root
				cleanup := w.Cleanup
				b, rerr := os.ReadFile(filepath.Join(home, ".openclaw", "openclaw.json"))
				if rerr != nil {
					t.Fatalf("openclaw config read: %v", rerr)
				}
				var cfg map[string]any
				if err := json.Unmarshal(b, &cfg); err != nil {
					t.Fatalf("openclaw config parse: %v\n%s", err, b)
				}
				if agents, ok := cfg["agents"].(map[string]any); ok {
					if defaults, ok := agents["defaults"].(map[string]any); ok {
						if model, ok := defaults["model"].(map[string]any); ok {
							pin, _ := model["primary"].(string)
							return pin, "", cleanup
						}
					}
				}
				return "", "", cleanup
			},
		},
		{
			name: "dsh",
			build: func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func()) {
				withHome(t)
				w, err := dshSpec.CarrierBuild(ep, model, cat)
				if err != nil {
					t.Fatalf("dsh CarrierBuild: %v", err)
				}
				// carrierContent resolves settings.yaml through the patch
				// redirect inside the wire's own sandbox.
				settings := carrierContent(t, "dsh", w.Root)
				cleanup := w.Cleanup
				var cfg map[string]any
				if err := yaml.Unmarshal([]byte(settings), &cfg); err != nil {
					t.Fatalf("dsh settings parse: %v\n%s", err, settings)
				}
				if adm, ok := cfg["agent-default-model"].(map[string]any); ok {
					pin, _ := adm["model"].(string)
					return pin, "", cleanup
				}
				return "", "", cleanup
			},
		},
		{
			name: "droid",
			build: func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func()) {
				withHome(t)
				w, err := droidSpec.CarrierBuild(ep, model, cat)
				if err != nil {
					t.Fatalf("droid CarrierBuild: %v", err)
				}
				home := w.Root
				cleanup := w.Cleanup
				b, rerr := os.ReadFile(filepath.Join(home, ".factory", "settings.json"))
				if rerr != nil {
					t.Fatalf("droid settings read: %v", rerr)
				}
				var cfg map[string]any
				if err := json.Unmarshal(b, &cfg); err != nil {
					t.Fatalf("droid settings parse: %v\n%s", err, b)
				}
				if sds, ok := cfg["sessionDefaultSettings"].(map[string]any); ok {
					pin, _ := sds["model"].(string)
					return pin, "", cleanup
				}
				return "", "", cleanup
			},
		},
		{
			name: "pi",
			build: func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func()) {
				withHome(t)
				w, err := piSpec.CarrierBuild(ep, model, cat)
				if err != nil {
					t.Fatalf("pi CarrierBuild: %v", err)
				}
				// The settings pair is the pin site; its defaultProvider
				// half is bridged separately (the scoped-composer key test).
				// Only absence counts as "no pin" — omp's discipline.
				b, rerr := os.ReadFile(filepath.Join(w.Root, ".pi", "agent", "settings.json"))
				if rerr != nil {
					if !os.IsNotExist(rerr) {
						t.Fatalf("pi settings.json read: %v", rerr)
					}
					return "", "", w.Cleanup
				}
				var st struct {
					DefaultModel string `json:"defaultModel"`
				}
				if err := json.Unmarshal(b, &st); err != nil {
					t.Fatalf("pi settings.json parse: %v\n%s", err, b)
				}
				return st.DefaultModel, "", w.Cleanup
			},
		},
		{
			name: "opencode",
			build: func(t *testing.T, ep Endpoint, model string, cat Catalog) (string, string, func()) {
				withHome(t)
				// opencode is WiringEnv — no sandbox, no file; the carrier
				// channel is CarrierEnv and the pin site is the inline
				// config's "model" key (written only for a TYPED model —
				// the held no-pin decision, declared by the field the
				// carrier simply never reads).
				env := opencodeSpec.CarrierEnv(ep, model, cat)
				var row string
				found := false
				for _, e := range env {
					if strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT=") {
						row = strings.TrimPrefix(e, "OPENCODE_CONFIG_CONTENT=")
						found = true
					}
				}
				if !found {
					t.Fatalf("opencode env carries no OPENCODE_CONFIG_CONTENT row: %v", env)
				}
				var cfg map[string]any
				if err := json.Unmarshal([]byte(row), &cfg); err != nil {
					t.Fatalf("opencode inline config parse: %v\n%s", err, row)
				}
				pin, _ := cfg["model"].(string)
				return pin, "", func() {}
			},
		},
	}
}

// TestDegradedCatalogWritesNoDefaultPin: a degraded catalog decision with
// no CLI model — every carrier's default-pin sites are ABSENT (the
// resolved verdict's no-ok branch). The catalog arrives as a white-box
// value (no socket): the degrade SHAPE itself is pinned at Resolve in
// TestResolveDegradeMatchesTheWhiteBox, so these rows test placement
// only. qwen is pinned separately below as the deliberate non-carrier:
// OPENAI_MODEL stays exported because qwen's env auth-inference requires
// the variable (dropping it makes the launch strictly worse; there is no
// picker default at that site).
func TestDegradedCatalogWritesNoDefaultPin(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	cat := degradedCatalog()
	for _, p := range catalogPinProbes(t) {
		t.Run(p.name, func(t *testing.T) {
			pin1, pin2, cleanup := p.build(t, ep, "", cat)
			defer cleanup()
			if pin1 != "" {
				t.Errorf("degraded catalog + no CLI model wrote a default pin %q; want the site absent (a default at a nonexistent placeholder only bends the picker)", pin1)
			}
			if pin2 != "" {
				t.Errorf("degraded catalog + no CLI model wrote a secondary default pin %q; want the site absent", pin2)
			}
		})
	}

	// qwen (RequiresModel) rides the given model verbatim in OPENAI_MODEL
	// (its env auth-inference requires the variable to exist) — driven WITH
	// a model: a model-less qwen is production-dead (the RequiresModel gate
	// refuses it before qwenEnv runs), so the degrade-time placeholder export
	// the ticket-19 exception pinned is gone (re-ruled moot 2026-09-13).
	env := qwenSpec.Env(ep, "m1")
	if !slices.Contains(env, "OPENAI_MODEL=m1") {
		t.Errorf("qwen env = %v, want the model-named export row OPENAI_MODEL=m1", env)
	}
}

// TestDegradedCatalogWithCLIModelStillPins covers the resolved verdict's
// third branch — the degraded fetch is overridden by a TYPED model: the
// pin lands at the CLI model for every carrier (a regression in the
// ok-branch ordering would otherwise sail through an all-green
// model-less table). Scoped sites carry the kya/ prefix, unscoped carry
// the bare id; opencode's only pin path is the typed model itself (the
// held no-pin decision governs the model-less case, pinned above).
func TestDegradedCatalogWithCLIModelStillPins(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	cat := typedCatalog("m1")
	for _, p := range catalogPinProbes(t) {
		t.Run(p.name, func(t *testing.T) {
			pin1, pin2, cleanup := p.build(t, ep, "m1", cat)
			defer cleanup()
			want1 := "m1"
			switch p.name {
			case "omp", "openclaw", "opencode":
				want1 = scopedModelID("m1")
			}
			if pin1 != want1 || pin2 != "" {
				t.Errorf("degraded catalog + CLI model: primary pin = %q secondary %q, want (%q, \"\")", pin1, pin2, want1)
			}
		})
	}
}

// TestRealCatalogEntryNamedLikeThePlaceholderWinsThePin: a REACHABLE
// gateway serving exactly one id named like the placeholder still wins
// every pinning carrier's default pin, like any real id (#08's rule for
// omp, made shared). This one rides the REAL Resolve against a live
// gatewaytest server — the fetch, merge and pin verdict are the thing
// under test, so no white-box shortcut.
func TestRealCatalogEntryNamedLikeThePlaceholderWinsThePin(t *testing.T) {
	gateway := gatewaytest.Models(t, "sk-test-12345", []string{PlaceholderModelID})
	ep := Endpoint{BaseURL: gateway.URL, Token: "sk-test-12345"}
	cat := Resolve(ep, "")

	want1 := map[string]string{
		"omp":      scopedModelID(PlaceholderModelID), // modelRoles.default is provider-scoped
		"openclaw": scopedModelID(PlaceholderModelID), // agents primary is provider-scoped
		"dsh":      PlaceholderModelID,
		"droid":    PlaceholderModelID,
		"pi":       PlaceholderModelID, // the settings pair carries the BARE id
	}
	for _, p := range catalogPinProbes(t) {
		want, inTable := want1[p.name]
		if !inTable {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			pin1, pin2, cleanup := p.build(t, ep, "", cat)
			defer cleanup()
			if pin1 != want || pin2 != "" {
				t.Errorf("served placeholder-named catalog: primary pin = %q (secondary %q), want %q", pin1, pin2, want)
			}
		})
	}
}

// TestResolveDegradeMatchesTheWhiteBox pins the bridge between the real
// Resolve and the white-box fixtures the per-runner tables drive: against
// an unreachable gateway the decision IS degradedCatalog()/typedCatalog()
// — same rows, same pin verdict — so the socket-free tables test the
// shapes launches actually receive.
func TestResolveDegradeMatchesTheWhiteBox(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	cat := Resolve(ep, "")
	if !reflect.DeepEqual(cat.IDs(), degradedCatalog().IDs()) {
		t.Errorf("Resolve(dead, \"\").IDs() = %v, want the white-box degrade shape %v", cat.IDs(), degradedCatalog().IDs())
	}
	if _, _, ok := cat.Pin(); ok {
		t.Errorf("Resolve(dead, \"\") yielded a pin; want the no-pin half of the sentinel-row contract")
	}
	cat = Resolve(ep, "m1")
	if !reflect.DeepEqual(cat.IDs(), typedCatalog("m1").IDs()) {
		t.Errorf("Resolve(dead, \"m1\").IDs() = %v, want the white-box typed shape %v", cat.IDs(), typedCatalog("m1").IDs())
	}
	if bare, scoped, ok := cat.Pin(); !ok || bare != "m1" || scoped != scopedModelID("m1") {
		t.Errorf("Resolve(dead, \"m1\").Pin() = (%q,%q,%v), want (m1, kya/m1, true)", bare, scoped, ok)
	}
}

// TestResolveMergesAndSortsTheFetchedCatalog pins the Model catalog
// shape at the seam: the CLI model leads, the rest arrive sorted, and
// the pin verdict follows the shared rule (typed model wins; otherwise
// the first served real id).
func TestResolveMergesAndSortsTheFetchedCatalog(t *testing.T) {
	srv := gatewaytest.Models(t, "sk-test-12345", []string{"zeta-model", "alpha-model"})
	ep := Endpoint{BaseURL: srv.URL, Token: "sk-test-12345"}

	cat := Resolve(ep, "glm-5.3-flash-nvfp4")
	want := []string{"glm-5.3-flash-nvfp4", "alpha-model", "zeta-model"}
	if got := cat.IDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("IDs = %v, want %v (CLI model leads, rest sorted)", got, want)
	}
	if bare, _, ok := cat.Pin(); !ok || bare != "glm-5.3-flash-nvfp4" {
		t.Errorf("Pin = (%q, %v), want the typed model", bare, ok)
	}

	// No CLI model: the first served real id wins (sorted order).
	cat = Resolve(ep, "")
	if got := cat.IDs(); !reflect.DeepEqual(got, []string{"alpha-model", "zeta-model"}) {
		t.Errorf("IDs = %v, want the sorted served list", got)
	}
	if bare, _, ok := cat.Pin(); !ok || bare != "alpha-model" {
		t.Errorf("Pin = (%q, %v), want the first served real id alpha-model", bare, ok)
	}
}

// TestResolveIDsAreACopy: the decision is per-launch state handed to the
// carrier — mutating the returned slice must not reach the decision (the
// next reader would otherwise see a mutated catalog).
func TestResolveIDsAreACopy(t *testing.T) {
	srv := gatewaytest.Models(t, "sk-test-12345", []string{"alpha-model"})
	ep := Endpoint{BaseURL: srv.URL, Token: "sk-test-12345"}
	cat := Resolve(ep, "")
	ids := cat.IDs()
	ids[0] = "mutated"
	if again := cat.IDs(); again[0] != "alpha-model" {
		t.Errorf("IDs() leaked the decision's backing array: first read mutated to %q, second read saw %q", ids[0], again[0])
	}
}

// Served slash-bearing ids compose ONCE: the provider-scoped pin is
// provider prefix + served id, exactly the correct provider/model form
// even when the served id itself carries slashes. Ticket 21's refusal
// is a CLI-FORM rule (what a human typed), never a rewrite of what the
// gateway serves.
func TestServedSlashBearingIdsComposeOnce(t *testing.T) {
	withHome(t)
	gateway := gatewaytest.Models(t, "sk-test-12345", []string{"anthropic/claude-sonnet"})
	_, configYml := loadOMPSandbox(t, Endpoint{BaseURL: gateway.URL, Token: "sk-test-12345"}, "")
	roles, ok := configYml["modelRoles"].(map[string]any)
	if !ok {
		t.Fatalf("served slash-id catalog must still pin the role; config: %v", configYml)
	}
	if got, _ := roles["default"].(string); got != scopedModelID("anthropic/claude-sonnet") {
		t.Errorf("modelRoles.default = %q, want the served slash-bearing id composed exactly once", got)
	}
}

// An id the gateway serves that starts with the provider scope
// composes verbatim (`kya/kya/x`) — the mark of the CLI-refusal rule's
// exact boundary: the form a human typed is screened; a SERVED id is
// never rewritten. Pinned so the distinction stays testable.
func TestServedIdNamedLikeTheScopeComposesVerbatim(t *testing.T) {
	withHome(t)
	gateway := gatewaytest.Models(t, "sk-test-12345", []string{"kya/x"})
	_, configYml := loadOMPSandbox(t, Endpoint{BaseURL: gateway.URL, Token: "sk-test-12345"}, "")
	roles, ok := configYml["modelRoles"].(map[string]any)
	if !ok {
		t.Fatalf("served kya/x must still pin; config: %v", configYml)
	}
	if got, _ := roles["default"].(string); got != scopedModelID("kya/x") {
		t.Errorf("modelRoles.default = %q, want the served id composed verbatim (%q)", got, scopedModelID("kya/x"))
	}
}

// The composer set is exactly the runners that hand the harness a
// scoped form (the CLI refusal must stay as wide as the composition
// risk, no wider): omp, openclaw, opencode, pi. The capability is
// declared data now (Spec.ScopedComposer) — the interface-assertion
// walk becomes a field walk.
func TestScopedComposersAreExactlyTheScopedFour(t *testing.T) {
	var got []string
	for _, s := range Runners() {
		if s.ScopedComposer {
			got = append(got, s.Name)
		}
	}
	want := []string{"omp", "openclaw", "opencode", "pi"}
	slices.Sort(got)
	if len(got) != len(want) {
		t.Fatalf("composer set = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("composer set = %v, want %v", got, want)
		}
	}
}

// TestScopedComposersWriteTheProviderScopeKey is the structural half of
// the provider-key ≡ provider-scope invariant (ticket 01): the sibling
// test above pins the composer SET (declared data); this bridge asserts
// the WRITTEN provider key equals providerScopePrefix, per runner, read
// back from each runner's own config (build wiring → parse the written
// config → assert against the declared constant, hermes_providers_test's
// house style).
//
// WHY THE KEY MATTERS: each harness resolves the composed "kya/<id>"
// reference against the provider NAMED in the config (name-based
// resolution, not membership), so a drifted key is a hard fail (opencode:
// the inline cfg "model" key would name a nonexistent provider) or a
// silent on-gateway pin loss (omp/openclaw/pi). Per-runner resolution
// evidence: pi (pi.go settings-pair contract — "pi infers the provider
// from the slash"; the PAIR defaultProvider+defaultModel is the second
// half this test asserts), omp (research §4 — the only credentialed
// candidate is kya/<id>), openclaw (research §5 —
// resolveConfiguredModelRef composes from the config's own key).
// OPENCODE re-verified 2026-09-16 against anomalyco/opencode@e03db9b
// (npm opencode-ai@1.18.31; research §7): the inline cfg "model" key
// resolves name-based against the provider map — the same
// drifted-key-fails-loud shape as the siblings.
//
// The bridge asserts provider KEYS ONLY — never model-id shapes: bare
// composers may legally carry gateway-served ids that start with the
// prefix, so an id-shape assertion would be wrong. A scoped composer with
// no extractor row fails closed (extend the table when adding one); a
// hardcoded four-name list is forbidden here on purpose.
func TestScopedComposersWriteTheProviderScopeKey(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	cat := typedCatalog("m1")
	rows := map[string]func(t *testing.T, ep Endpoint){
		"omp": func(t *testing.T, ep Endpoint) {
			withHome(t)
			w, err := ompSpec.CarrierBuild(ep, "m1", cat)
			if err != nil {
				t.Fatalf("omp CarrierBuild: %v", err)
			}
			defer w.Cleanup()
			b, err := os.ReadFile(filepath.Join(w.Root, "agent", "models.yml"))
			if err != nil {
				t.Fatalf("omp models.yml read: %v", err)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				t.Fatalf("omp models.yml parse: %v\n%s", err, b)
			}
			providers, _ := cfg["providers"].(map[string]any)
			assertSingleProviderKey(t, "omp", providers)
		},
		"pi": func(t *testing.T, ep Endpoint) {
			withHome(t)
			w, err := piSpec.CarrierBuild(ep, "m1", cat)
			if err != nil {
				t.Fatalf("pi CarrierBuild: %v", err)
			}
			defer w.Cleanup()
			// The pair: models.json's providers key AND settings.json's
			// defaultProvider must both name the constant. The model "m1"
			// reaches settings.json's conditional row (the typed pin is
			// always ok).
			mb, err := os.ReadFile(filepath.Join(w.Root, ".pi", "agent", "models.json"))
			if err != nil {
				t.Fatalf("pi models.json read: %v", err)
			}
			var models struct {
				Providers map[string]any `json:"providers"`
			}
			if err := json.Unmarshal(mb, &models); err != nil {
				t.Fatalf("pi models.json parse: %v\n%s", err, mb)
			}
			assertSingleProviderKey(t, "pi", models.Providers)
			sb, err := os.ReadFile(filepath.Join(w.Root, ".pi", "agent", "settings.json"))
			if err != nil {
				t.Fatalf("pi settings.json read (the model must reach the conditional row): %v", err)
			}
			var settings struct {
				DefaultProvider string `json:"defaultProvider"`
			}
			if err := json.Unmarshal(sb, &settings); err != nil {
				t.Fatalf("pi settings.json parse: %v\n%s", err, sb)
			}
			if settings.DefaultProvider != providerScopePrefix {
				t.Errorf("pi settings.json defaultProvider = %q, want the providerScopePrefix %q (the pair's second half)", settings.DefaultProvider, providerScopePrefix)
			}
		},
		"openclaw": func(t *testing.T, ep Endpoint) {
			withHome(t)
			w, err := openclawSpec.CarrierBuild(ep, "m1", cat)
			if err != nil {
				t.Fatalf("openclaw CarrierBuild: %v", err)
			}
			defer w.Cleanup()
			b, err := os.ReadFile(filepath.Join(w.Root, ".openclaw", "openclaw.json"))
			if err != nil {
				t.Fatalf("openclaw openclaw.json read: %v", err)
			}
			var cfg struct {
				Models struct {
					Providers map[string]any `json:"providers"`
				} `json:"models"`
			}
			if err := json.Unmarshal(b, &cfg); err != nil {
				t.Fatalf("openclaw parse: %v\n%s", err, b)
			}
			assertSingleProviderKey(t, "openclaw", cfg.Models.Providers)
		},
		"opencode": func(t *testing.T, ep Endpoint) {
			withHome(t)
			// opencode is WiringEnv — no sandbox, no file; Spec validation
			// forbids BuildEnv for it. The key rides the inline-config row.
			env := opencodeSpec.CarrierEnv(ep, "m1", cat)
			var row string
			found := false
			for _, e := range env {
				if strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT=") {
					row = strings.TrimPrefix(e, "OPENCODE_CONFIG_CONTENT=")
					found = true
				}
			}
			if !found {
				t.Fatalf("opencode env carries no OPENCODE_CONFIG_CONTENT row: %v", env)
			}
			var cfg struct {
				Provider map[string]any `json:"provider"`
			}
			if err := json.Unmarshal([]byte(row), &cfg); err != nil {
				t.Fatalf("opencode inline config parse: %v\n%s", err, row)
			}
			assertSingleProviderKey(t, "opencode", cfg.Provider)
		},
	}
	for _, s := range Runners() {
		if !s.ScopedComposer {
			continue
		}
		row, declared := rows[s.Name]
		if !declared {
			t.Fatalf("scoped composer %q has no provider-key extractor row; extend the table when adding a scoped composer", s.Name)
		}
		t.Run(s.Name, func(t *testing.T) {
			row(t, ep)
		})
	}
}

// assertSingleProviderKey is the settled bridge assertion: exactly ONE
// provider key and it equals the constant. A second provider row is a
// conscious design change that must go red at the matrix review point,
// not sail through.
func assertSingleProviderKey(t *testing.T, runner string, providers map[string]any) {
	t.Helper()
	if len(providers) != 1 {
		t.Fatalf("%s: %d provider rows, want exactly one: %v", runner, len(providers), providers)
	}
	for k := range providers {
		if k != providerScopePrefix {
			t.Errorf("%s: written provider key %q, want the providerScopePrefix %q (key drift is a hard fail or a silent pin loss)", runner, k, providerScopePrefix)
		}
	}
}

// TestDegradedListRowsKeepThePlaceholder guards the preserved half of the
// contract: the carriers' default PIN is dropped on degrade, but the
// degrade row in each MODELS LIST stays (the documented transient-outage
// degradation target). Asserted on openclaw, whose raw config joins both
// halves — no agents pin, yet a "kya-model" row under models.
func TestDegradedListRowsKeepThePlaceholder(t *testing.T) {
	withHome(t)
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	w, err := openclawSpec.CarrierBuild(ep, "", degradedCatalog())
	if err != nil {
		t.Fatalf("openclaw CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	// openclaw's env HOME is the sandbox root itself (rows that replace
	// HOME redirect the harness's state there), so the config path hangs
	// off the wire's root, not the isolated home.
	sandbox := w.Root
	b, err := os.ReadFile(filepath.Join(sandbox, ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatalf("read openclaw config: %v", err)
	}
	s := string(b)
	if strings.Contains(s, `"primary"`) {
		t.Errorf("degraded openclaw config still carries a primary pin:\n%s", s)
	}
	if !strings.Contains(s, PlaceholderModelID) {
		t.Errorf("degraded openclaw config lost the placeholder LIST row:\n%s", s)
	}
}

// TestCheckModelArgOwnsTheScopeGate pins the boundary deepening (2026-09-16
// review): the provider-scoped refusal is harness-owned — the Spec answers
// for its own compose shape and cmd keeps no re-derivation (the prefix
// constant is package-internal, so there is nothing to lean on across the
// seam). The refusal sentence itself stays byte-frozen where it is pinned,
// at the CLI boundary (main_test.go's ticket-21 fleet).
func TestCheckModelArgOwnsTheScopeGate(t *testing.T) {
	byName := map[string]Spec{}
	for _, s := range Runners() {
		byName[s.Name] = s
	}
	for _, name := range []string{"omp", "openclaw", "opencode", "pi"} {
		s := byName[name]
		if !s.ScopedComposer {
			t.Fatalf("%s must be a scoped composer (the registry matrix guards this too)", name)
		}
		err := s.CheckModelArg(providerScopePrefix + "/m1")
		if err == nil || !strings.Contains(err.Error(), "provider-scoped model argument — type the bare id") {
			t.Errorf("%s: scoped form = %v, want the second-scope refusal", name, err)
		}
		if err := s.CheckModelArg("m1"); err != nil {
			t.Errorf("%s: bare id refused: %v", name, err)
		}
	}
	// Bare composers may genuinely receive a SERVED id named like the
	// scope — the refusal must not fire for them (runner-aware).
	for _, name := range []string{"droid", "dsh"} {
		s := byName[name]
		if s.ScopedComposer {
			t.Fatalf("%s declared scoped — the four-carrier set changed", name)
		}
		if err := s.CheckModelArg(providerScopePrefix + "/x"); err != nil {
			t.Errorf("%s: bare composer refused a scope-named served id: %v", name, err)
		}
	}
	// The empty model is every other gate's business, not this one.
	if err := byName["pi"].CheckModelArg(""); err != nil {
		t.Errorf("CheckModelArg(\"\") = %v, want nil (RequiresModel and the catalog gates own that case)", err)
	}
}

// TestFetchedCatalogPinsPiStartupDefault is the behavior half of joining
// pi to the shared predicate (2026-09-16 review, ticket 7): a REACHABLE
// gateway and NO CLI model must still land pi on the gateway — the
// settings pair names the first served id, BARE. Without the pin, pi's
// step-5 selection scans its built-in known-provider tables first and
// the child env inherits the operator's shell wholesale: one stray
// ANTHROPIC_API_KEY/OPENAI_API_KEY boots the session OFF-gateway
// (source-verified against pi's selection order; omp's issues #3569/
// #6162 name the same hazard — the live reproduction of the flip
// remains an open empirical follow-up).
func TestFetchedCatalogPinsPiStartupDefault(t *testing.T) {
	gateway := gatewaytest.Models(t, "sk-test-12345", []string{"m2", "a1"})
	ep := Endpoint{BaseURL: gateway.URL, Token: "sk-test-12345"}
	withHome(t)
	w, err := piSpec.CarrierBuild(ep, "", Resolve(ep, ""))
	if err != nil {
		t.Fatalf("pi CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	b, err := os.ReadFile(filepath.Join(w.Root, ".pi", "agent", "settings.json"))
	if err != nil {
		t.Fatalf("fetched catalog without a CLI model must still write the settings pair: %v", err)
	}
	var st struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("settings parse: %v\n%s", err, b)
	}
	// Models() sorts — the first served id is a1, not the typed order.
	if st.DefaultModel != "a1" || st.DefaultProvider != providerScopePrefix {
		t.Errorf("settings pair = (%q,%q), want the first served BARE id %q under provider %q", st.DefaultModel, st.DefaultProvider, "a1", providerScopePrefix)
	}
}

// TestAmbientKeysInheritButAreNeverPlanted pins the two halves of the
// ambient-key premise behind the default-pin design (research §2; review
// 2026-09-16 gap-3 fix). Half one — INHERITANCE: the child env is the
// ambient process env plus the wire rows, so a stray ANTHROPIC_API_KEY in
// the operator shell REACHES the harness child (that is the flip channel
// the fetched-path default pin closes for pi). Half two — NO PLANTING:
// the pi/sandbox wiring itself never emits either flipping key, so any
// ambient key can only come from the operator environment, never from
// kyacode. Scope note (2026-09-16 third instance): qwen deliberately
// exports OPENAI_API_KEY=<gateway token> on its own WiringEnv path — that
// is qwen auth-inference, out of this pin by design.
func TestAmbientKeysInheritButAreNeverPlanted(t *testing.T) {
	// Half two at the real carrier: degraded, model-less pi wiring.
	withHome(t)
	dead := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	w, err := piSpec.CarrierBuild(dead, "", degradedCatalog())
	if err != nil {
		t.Fatalf("pi CarrierBuild: %v", err)
	}
	defer w.Cleanup()
	for _, row := range w.Env {
		if strings.HasPrefix(row, "ANTHROPIC_API_KEY=") || strings.HasPrefix(row, "OPENAI_API_KEY=") {
			t.Errorf("pi wiring planted a flipping key row %q — kyacode never plants these (ambient keys arrive via shell inheritance only)", row)
		}
	}

	// Half one through the real launch boundary: the dumped child env must
	// carry the ambient row AND the wire row.
	dump := filepath.Join(t.TempDir(), "env.txt")
	fake := composeSpec("KYA_ENV_DUMP", dump)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ambient-flip-proof")
	if err := Launch(fake, Endpoint{}, "", nil, nil, nil); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("child env sentinel never dumped: %v", err)
	}
	env := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !slices.Contains(env, "ANTHROPIC_API_KEY=sk-ambient-flip-proof") {
		t.Errorf("ambient ANTHROPIC_API_KEY did not reach the child env — the inheritance premise behind the default-pin design broke")
	}
	if !slices.Contains(env, "KYA_ENV_DUMP="+dump) {
		t.Errorf("child env missing the wire injected row: %v", env)
	}
}
