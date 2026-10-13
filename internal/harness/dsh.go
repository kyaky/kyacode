package harness

import (
	"path/filepath"

	"kyacode/internal/harness/sandbox"
)

// dshProviderKey namespaces kyacode rows in the harness settings.
const dshProviderKey = "kya"

// dshPrefix is the one sandbox anchor dshSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const dshPrefix = "dsh"

// DSH wires DeepSeek Harness. Round-5 audit: DEEPSEEK_* env vars have no
// documented support. ollama instead writes settings.yaml with a
// schema-referenced `apiKeyEnv` plus a --patch redirect pointing the CLI
// at that settings file (the ollama deepseek-harness settings+patch
// shape). We mirror that shape in a sandbox: settings.yaml + patch yml,
// real key only in env.
//
// The patch redirect rides Wire.Args as a real return value. The return
// shape was fighting back before the Wire: an earlier revision parked the
// path in a package-global (which leaked a previous launch's stale
// sandbox path into a later launch, and suppressed the `web --patch`
// args entirely on a fresh process whose BuildEnv errored before
// writing), and after that smuggled it through the env slice for a
// second interface (ArgsFromEnv) to scavenge. Per-launch state on the
// Wire makes both transport tricks structurally impossible.
var dshSpec = Spec{
	Name:          "dsh",
	Description:   "DeepSeek Harness (sandboxed settings + patch, key by env reference)",
	Wiring:        WiringSandbox,
	SandboxPrefix: dshPrefix,
	Find: func() (string, bool) {
		return findIn("dsh", homePaths("dsh", ".local/bin"))
	},
	Install: func() (bin string, args []string) {
		return "npm", []string{"install", "-g", "@deepseek-ai/dsh@latest"}
	},
	CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
		// Key channel EnvRef — settings.yaml REFERENCES the key by the env
		// var's name (schema key `apiKeyEnv`, per ollama's DeepSeek-harness
		// settings shape) and the actual value rides process env only.
		// Catalog carrier: the launch boundary resolved the Model catalog
		// decision (cat) — this body renders its own schema from it and
		// never fetches.
		return buildSandbox(dshPrefix, sandbox.EnvRef, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			settingsPath := filepath.Join(sb.Root(), "settings.yaml")
			patchPath := filepath.Join(sb.Root(), "kya.cordis.yml")

			settings := map[string]any{
				"llm-pi-ai": map[string]any{
					"providers": map[string]any{
						dshProviderKey: map[string]any{
							"displayName": "KYA Gateway",
							"apiKeyEnv":   sandbox.KeyEnvName,
							"api":         "openai-completions",
							"baseURL":     ep.OpenAIBase(),
							"models":      cat.Rows(),
						},
					},
				},
			}
			// The startup default rides the resolved pin verdict — see
			// Catalog.Pin for the rule; site fact: agent-default-model
			// re-binds the default the harness applies once it restarts.
			// dsh is a bare composer: the pin rides the bare form.
			if bare, _, ok := cat.Pin(); ok {
				settings["agent-default-model"] = map[string]any{
					"provider": dshProviderKey,
					"model":    bare,
				}
			}
			if err := sb.WriteYAML("settings.yaml", settings); err != nil {
				return nil, nil, err
			}

			patch := []map[string]any{{
				"id": "settings",
				"config": map[string]any{
					"path": settingsPath,
				},
			}}
			if err := sb.WriteYAML("kya.cordis.yml", patch); err != nil {
				return nil, nil, err
			}

			// The DeepSeek harness reads `web --patch <path>` (the ollama
			// deepseek-harness CLI shape). The pair is this launch's Args on
			// the Wire — assembled here, from the patch path the wiring just
			// wrote. The env stays the credential row alone: the declared
			// EnvRef channel emits it, nothing else rides next to it. That
			// is also why no ExtraArgs exists on this spec: the launcher
			// path's argv channel is Wire.Args, and the patch pair IS the
			// runner's whole own argv.
			return sb.Credentials(), []string{"web", "--patch", patchPath}, nil
		})
	},
}
