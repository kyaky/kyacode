package harness

import (
	"kyacode/internal/harness/sandbox"
)

// openclawPrefix is the one sandbox anchor openclawSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const openclawPrefix = "openclaw"

// OpenClaw wires OpenClaw via a sandboxed HOME containing
// .openclaw/openclaw.json with a provider entry.
var openclawSpec = Spec{
	Name:          "openclaw",
	Description:   "OpenClaw (sandboxed openclaw.json, no disk writes to ~/.openclaw)",
	Wiring:        WiringSandbox,
	SandboxPrefix: openclawPrefix,
	// ScopedComposer: the primary pin and --model args carry the
	// provider-scoped "kya/<id>" form.
	ScopedComposer: true,
	Find: func() (string, bool) {
		return findIn("openclaw", homePaths("openclaw", ".local/bin"))
	},
	CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
		// Key channel Dual — the key value rides in .openclaw/openclaw.json
		// (apiKey) AND the process env (KYA_API_KEY); the Windows home
		// triad is appended by the sandbox module. Catalog carrier: the
		// launch boundary resolved the Model catalog decision (cat) — this
		// body renders its own schema from it and never fetches.
		return buildSandbox(openclawPrefix, sandbox.Dual, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			cfg := map[string]any{
				"models": map[string]any{
					"mode": "merge",
					"providers": map[string]any{
						providerScopePrefix: map[string]any{
							"baseUrl": ep.OpenAIBase(),
							"apiKey":  ep.Token,
							"api":     "openai-completions",
							"models":  cat.Rows(),
						},
					},
				},
			}
			// The startup default rides the resolved pin verdict — see
			// Catalog.Pin for the rule; site fact:
			// agents.defaults.model.primary is openclaw's picker default
			// carrier. openclaw is a scoped composer: the pin rides the
			// scoped form.
			if _, scoped, ok := cat.Pin(); ok {
				cfg["agents"] = map[string]any{
					"defaults": map[string]any{
						"model": map[string]any{
							"primary": scoped,
						},
					},
				}
			}
			if err := sb.WriteJSON(".openclaw/openclaw.json", cfg); err != nil {
				return nil, nil, err
			}

			env := []string{"HOME=" + sb.Root()}
			env = append(env, sb.Credentials()...)
			// Windows resolves home via USERPROFILE and the drive/path
			// pair, not HOME — the redirect triad is emitted by the sandbox
			// module.
			return append(env, sb.HomeTriad()...), openclawModelArgs(model), nil
		})
	},
}

// openclawModelArgs is the launcher's own argv (built inside the BuildEnv
// closure: the launcher path's argv channel is Wire.Args).
func openclawModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", scopedModelID(model)}
}
