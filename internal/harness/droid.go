package harness

import (
	"strconv"

	"kyacode/internal/harness/sandbox"
)

// droidPrefix is the one sandbox anchor droidSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const droidPrefix = "droid"

// Droid wires Factory's Droid via a sandboxed home containing
// .factory/settings.json with a customModels entry.
var droidSpec = Spec{
	Name:          "droid",
	Description:   "Droid (Factory, sandboxed settings, no disk writes to ~/.factory)",
	Wiring:        WiringSandbox,
	SandboxPrefix: droidPrefix,
	Find: func() (string, bool) {
		return findIn("droid", homePaths("droid", ".local/bin"))
	},
	Install: func() (bin string, args []string) {
		// The published package is the bare "droid", not @factoryai/droid
		// (verified: @factoryai/droid is a registry 404; docs.factory.ai
		// quickstart lists npm as an official channel).
		return "npm", []string{"install", "-g", "droid"}
	},
	CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
		// Key channel Dual — the key value rides in .factory/settings.json
		// (apiKey) AND the process env (KYA_API_KEY); the Windows home
		// triad is appended by the sandbox module. Catalog carrier: the
		// launch boundary resolved the Model catalog decision (cat) — this
		// body renders its own schema from it and never fetches.
		return buildSandbox(droidPrefix, sandbox.Dual, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			// Enterprise-settings schema: each customModels row carries
			// identity fields — id ('custom:...' — droid merges rows by id),
			// integer index (display order), displayName. droid's rows are
			// a genuinely different schema than the shared {"id": id} rows,
			// so they build from the decision's IDs directly.
			customModels := make([]map[string]any, 0)
			for i, id := range cat.IDs() {
				customModels = append(customModels, map[string]any{
					"id":          "custom:kya-" + strconv.Itoa(i),
					"index":       i,
					"displayName": id,
					"model":       id,
					"provider":    "generic-chat-completion-api",
					"baseUrl":     ep.OpenAIBase(),
					"apiKey":      ep.Token,
				})
			}
			settings := map[string]any{
				"customModels": customModels,
			}
			// The org-style default lives at sessionDefaultSettings.model
			// (the top-level 'model' key is a user-settings field, not the
			// default carrier). The pin rides the resolved pin verdict —
			// see Catalog.Pin for the rule; the customModels rows keep the
			// documented list degrade either way. droid pins the bare form.
			if bare, _, ok := cat.Pin(); ok {
				settings["sessionDefaultSettings"] = map[string]any{
					"model": bare,
				}
			}

			if err := sb.WriteJSON(".factory/settings.json", settings); err != nil {
				return nil, nil, err
			}

			env := []string{"HOME=" + sb.Root()}
			env = append(env, sb.Credentials()...)
			// On Windows, os.homedir() and the .NET/Node home lookups do
			// not read HOME — so HOME=sandbox alone leaves the harness
			// writing to the real user profile (with the key embedded
			// there). The triad the sandbox module emits redirects every
			// home mechanism at the sandbox.
			return append(env, sb.HomeTriad()...), droidModelArgs(model), nil
		})
	},
}

// droidModelArgs is the launcher's own argv (built inside the BuildEnv
// closure: the launcher path's argv channel is Wire.Args). Bare id only:
// droid (0.211.0, verified empirically) rejects "provider/model" combos
// for custom models — "Invalid model: kya/<id>" — while the bare
// customModels[].model value passes the model gate. The settings default
// (BuildEnv) and the CLI flag must use the same bare form.
func droidModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"-m", model}
}
