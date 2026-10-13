package harness

import (
	"path/filepath"

	"kyacode/internal/harness/sandbox"
)

// ompPrefix is the one sandbox anchor ompSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const ompPrefix = "omp"

// OMP wires Oh-My-Pi. The npm package is `oh-my-pi`, whose installed
// binary is `omp` — Find probes the canonical binary name under its home
// paths (and PATH); no alternate-name probe exists, and the earlier note
// promising `oh-my-pi`-name and Windows-shim fallbacks described lookup
// behavior that was never implemented, with no sign of real user impact,
// so it is not promised here anymore. And env `OPENAI_BASE_URL` has no
// documented support — ollama instead writes
// PI_CODING_AGENT_DIR/.../models.yml with an auth:"none" provider
// (server-side credentials; ours: sandbox + real key in the provider
// apiKey, ollama-scoped auth none == equivalent shape).
var ompSpec = Spec{
	Name:          "omp",
	Description:   "OMP (Oh My Pi, sandboxed models.yml, no disk writes to ~/.omp)",
	Wiring:        WiringSandbox,
	SandboxPrefix: ompPrefix,
	// ScopedComposer: modelRoles pins and --model args carry the
	// provider-scoped "kya/<id>" form.
	ScopedComposer: true,
	Find: func() (string, bool) {
		return findIn("omp", homePaths("omp", ".local/bin", ".bun/bin"))
	},
	Install: func() (bin string, args []string) {
		return "npm", []string{"install", "-g", "oh-my-pi@latest"}
	},
	CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
		// Key channel FileOnly — the key lives in models.yml only; the
		// child env carries no credential row. Catalog carrier: the launch
		// boundary resolved the Model catalog decision (cat) — this body
		// renders its own schema from it and never fetches.
		return buildSandbox(ompPrefix, sandbox.FileOnly, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			agentDir := filepath.Join(sb.Root(), "agent")

			// providers.kya per ollama omp.go:365-373 (api=openai-responses,
			// auth shape) except baseUrl points at our gateway and apiKey
			// carries the user's real key (gateway requires Bearer; ollama's
			// local server accepted none). Every gateway model is listed
			// inline so OMP's picker can switch freely; a degraded fetch
			// leaves the CLI model / placeholder row. No discovery key:
			// discovery.type "list" is not one of the schema's enum values,
			// which failed the whole models.yml loudly and made OMP fall back
			// to built-in providers — inline models do not need discovery at
			// all.
			providers := map[string]any{
				providerScopePrefix: map[string]any{
					"baseUrl": ep.OpenAIBase(),
					"api":     "openai-responses",
					"apiKey":  ep.Token,
					"models":  cat.Rows(),
				},
			}
			if err := sb.WriteYAML("agent/models.yml", map[string]any{"providers": providers}); err != nil {
				return nil, nil, err
			}

			// The root-level defaultModel key was dead (nothing reads
			// cfg.defaultModel): the default priority lives in settings
			// modelRoles, so the launched model is pinned there. The pin
			// rides the resolved pin verdict — see Catalog.Pin for the rule
			// (omp's #08 fetched-ness branch IS that rule's origin). omp is
			// a scoped composer: the pin rides the scoped form.
			if _, scoped, ok := cat.Pin(); ok {
				if err := sb.WriteYAML("agent/config.yml", map[string]any{
					"modelRoles": map[string]any{"default": scoped},
				}); err != nil {
					return nil, nil, err
				}
			}

			return []string{"PI_CODING_AGENT_DIR=" + agentDir}, ompModelArgs(model), nil
		})
	},
}

// ompModelArgs is the launcher's own argv (built inside the BuildEnv
// closure: the launcher path's argv channel is Wire.Args).
func ompModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", scopedModelID(model)}
}
