package harness

import (
	"kyacode/internal/harness/sandbox"
)

// piPrefix is the one sandbox anchor piSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const piPrefix = "pi"

// Pi wires Pi coding agent via a sandboxed HOME containing
// .pi/agent/models.json with a provider entry. Every gateway model is
// listed so Ctrl+P / --model can select any of them; the CLI model
// argument (when given) is written first and becomes pi's startup
// default via settings.json — and so does the fetched catalog's first
// served id when no model was typed (the shared DefaultModel
// predicate; see BuildEnv for why that half matters).
var piSpec = Spec{
	Name:          "pi",
	Description:   "Pi (sandboxed models.json, no disk writes to ~/.pi)",
	Wiring:        WiringSandbox,
	SandboxPrefix: piPrefix,
	// ScopedComposer: --model args carry the provider-scoped "kya/<id>"
	// form.
	ScopedComposer: true,
	Find: func() (string, bool) {
		return findIn("pi", homePaths("pi", ".local/bin"))
	},
	Install: func() (bin string, args []string) {
		return "npm", []string{"install", "-g", "@earendil-works/pi-coding-agent"}
	},
	CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
		// Key channel Dual — the key value rides in .pi/agent/models.json
		// (apiKey) AND the process env (KYA_API_KEY); the Windows home
		// triad is appended by the sandbox module. Catalog carrier: the
		// launch boundary resolved the Model catalog decision (cat) — this
		// body renders its own schema from it and never fetches.
		return buildSandbox(piPrefix, sandbox.Dual, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			// Every gateway model goes into the provider so the in-TUI
			// model selector can switch freely. A degraded fetch is
			// non-fatal: the decision carries the single CLI model (or the
			// kya-model placeholder) so a transient /v1/models outage
			// cannot block the launch entirely.
			models := cat.Rows()
			provider := map[string]any{
				"baseUrl": ep.OpenAIBase(),
				"api":     "openai-completions",
				"apiKey":  ep.Token,
				"models":  models,
			}
			providers := map[string]any{providerScopePrefix: provider}
			if err := sb.WriteJSON(".pi/agent/models.json", map[string]any{"providers": providers}); err != nil {
				return nil, nil, err
			}
			// pi's startup default comes from the resolved pin verdict
			// (joined to the shared rule 2026-09-16): the CLI model when
			// typed; the fetched catalog's first served id when not; NO
			// pin on a degraded fetch without a model — the paired half
			// of the sentinel-row contract (ADR-0001), so the placeholder
			// row stays picker-bait, never a pin at a nonexistent model.
			//
			// Pinning the fetched case is the point. Absent a saved
			// default, pi's step-5 selection scans its built-in known-
			// provider tables FIRST, and the child env inherits the
			// operator's shell wholesale — one stray
			// ANTHROPIC_API_KEY/OPENAI_API_KEY and pi boots OFF-gateway
			// (source-verified against pi's selection order; omp's own
			// issue trail #3569/#6162 names the same hazard). The pin
			// makes step 4 answer before step 5 ever runs. The live
			// reproduction of the flip remains an open empirical
			// follow-up.
			//
			// Settings contract (pi settings.md, Model & Thinking
			// table): a saved default is honored only as the PAIR
			// defaultProvider + defaultModel — defaultModel carries the
			// BARE model id, which the served ids already are. The
			// 'kya/<id>' compose form stays the --model CLI path's
			// (piModelArgs), where pi infers the provider from the slash.
			// pi is a ScopedComposer runner that pins BARE: the pin form
			// is placement knowledge, which is why Pin renders both.
			if bare, _, ok := cat.Pin(); ok {
				settings := map[string]any{
					"defaultProvider": providerScopePrefix,
					"defaultModel":    bare,
				}
				if err := sb.WriteJSON(".pi/agent/settings.json", settings); err != nil {
					return nil, nil, err
				}
			}

			env := []string{"HOME=" + sb.Root()}
			env = append(env, sb.Credentials()...)
			// Windows resolves home via USERPROFILE and the drive/path
			// pair, not HOME — the redirect triad is emitted by the sandbox
			// module.
			return append(env, sb.HomeTriad()...), piModelArgs(model), nil
		})
	},
}

// piModelArgs is the launcher's own argv (built inside the BuildEnv
// closure: the launcher path's argv channel is Wire.Args).
func piModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", scopedModelID(model)}
}
