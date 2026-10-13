package harness

import (
	"kyacode/internal/harness/sandbox"
)

// clinePrefix is the one sandbox anchor clineSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const clinePrefix = "cline"

// Cline wires Cline CLI via a sandboxed CLINE_DATA_DIR containing
// providers.json. The real key is embedded in the config; the sandbox is
// reclaimed on exit (same pattern as codex).
var clineSpec = Spec{
	Name:          "cline",
	Description:   "Cline (sandboxed CLINE_DATA_DIR, no disk writes to ~/.cline)",
	Wiring:        WiringSandbox,
	SandboxPrefix: clinePrefix,
	// RequiresModel: providers.json pins one modelId; without it cline
	// starts with no model selected.
	RequiresModel: true,
	Find: func() (string, bool) {
		return findIn("cline", homePaths("cline", ".local/bin"))
	},
	Install: func() (bin string, args []string) {
		return "npm", []string{"install", "-g", "cline"}
	},
	BuildEnv: func(ep Endpoint, model string) (Wire, error) {
		// Key channel FileOnly — the key lives in providers.json only; the
		// child env carries no credential row.
		return buildSandbox(clinePrefix, sandbox.FileOnly, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			providers := map[string]any{
				"openai": map[string]any{
					"baseUrl": ep.OpenAIBase(),
					"apiKey":  ep.Token,
					"enabled": true,
				},
			}
			if model != "" {
				providers["openai"].(map[string]any)["modelId"] = model
			}
			if err := sb.WriteJSON("data/settings/providers.json", providers); err != nil {
				return nil, nil, err
			}

			return []string{"CLINE_DATA_DIR=" + sb.Root()}, clineModelArgs(model), nil
		})
	},
}

// clineModelArgs is the launcher's own argv (built inside the BuildEnv
// closure: the launcher path's argv channel is Wire.Args).
func clineModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"-m", model}
}
