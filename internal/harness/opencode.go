package harness

import (
	"encoding/json"
	"fmt"
)

// OpenCode wires opencode via the OPENCODE_CONFIG_CONTENT environment
// variable (its documented inline-config mechanism): provider.kya points
// straight at the gateway with the user's real key.
var opencodeSpec = Spec{
	Name:        "opencode",
	Description: "opencode (OPENCODE_CONFIG_CONTENT, no disk writes)",
	Wiring:      WiringEnv,
	// ScopedComposer: the inline config's "model" key carries the
	// provider-scoped "kya/<id>" form, so a provider-scoped CLI argument
	// would compose a second scope layer. (Re-verified 2026-09-16
	// against anomalyco/opencode@e03db9b: the cfg model key resolves
	// name-based against the provider map. The OPENCODE_MODEL export
	// below is RETAINED but INERT — opencode's code contains no
	// OPENCODE_MODEL handling at all; its fate belongs to the held
	// default-pin decision, not to this comment.)
	ScopedComposer: true,
	Find: func() (string, bool) {
		return findIn("opencode", homePaths("opencode", ".opencode/bin"))
	},
	Install: opencodeInstall,
	// Catalog carrier, env-wired: the launch boundary resolves the Model
	// catalog decision once and hands it over — this body renders the
	// inline config from it and never fetches.
	CarrierEnv: func(ep Endpoint, model string, cat Catalog) []string {
		env := []string{"OPENCODE_CONFIG_CONTENT=" + openCodeInlineConfig(ep, model, cat)}
		if model != "" {
			env = append(env, "OPENCODE_MODEL="+scopedModelID(model))
		}
		return env
	},
}

func opencodeInstall() (bin string, args []string) {
	if runtimeGOOS() == "windows" {
		return "npm", []string{"install", "-g", "opencode-ai@latest"}
	}
	return "bash", []string{"-c", "curl -fsSL https://opencode.ai/install | bash"}
}

// openCodeInlineConfig: inline JSON (documented OPENCODE_CONFIG_CONTENT
// shape). baseURL = gateway + /v1; apiKey = user's real key. The models
// map lists every gateway model when the catalog fetch succeeded (full
// picker in /models), degrading to the CLI model (or a hint entry) when
// the gateway is unreachable — never the undocumented "*" wildcard.
func openCodeInlineConfig(ep Endpoint, model string, cat Catalog) string {
	models := map[string]any{}
	for _, id := range cat.IDs() {
		models[id] = map[string]any{"name": id}
	}
	if len(models) == 0 {
		// Resolve never returns an empty list today; guard the
		// config shape anyway so the provider row stays valid.
		models["gateway"] = map[string]any{"name": "type any gateway model id"}
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			providerScopePrefix: map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "KYA Gateway",
				"options": map[string]any{
					"baseURL": ep.OpenAIBase(),
					"apiKey":  ep.Token,
				},
				"models": models,
			},
		},
	}
	// The inline config's model key rides only a TYPED CLI model.
	// Routing it (and OPENCODE_MODEL) through the resolved pin like the
	// five pinning carriers is HELD — the decision is visible as data
	// now (opencode is a declared carrier that simply never reads
	// cat.Pin): the absent-default empirics found the routing
	// NOT behavior-neutral (opencode boots on the SORT-winner, not
	// first-served) — the pin's honest shape needs primary-source
	// evidence of what that does to the boot pick, not a guess.
	if model != "" {
		cfg["model"] = scopedModelID(model)
	}
	return string(mustMarshalJSON(cfg))
}

// mustMarshalJSON is opencode's member of the must-marshal family: the
// inline-config marshal has no error channel (env-wired Env returns env
// rows, not errors — opencode writes nothing to disk), so an
// unmarshalable value must fail LOUDLY: an empty-silence config would
// send the harness away with the key unused.
func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal: %v", err))
	}
	return b
}
