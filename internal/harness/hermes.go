package harness

import (
	"kyacode/internal/harness/sandbox"
)

// hermesProviderKey namespaces everything kyacode writes in the user's
// hermes config.yaml (ollama's hermes.go uses "ollama-launch"; the launch-
// owned key lets the user's /model picker align with the managed entry).
const hermesProviderKey = "kya-launch"

// hermesPrefix is the one sandbox anchor hermesSpec.SandboxPrefix declares
// and the local BuildEnv's sandbox.New call uses — two readers, one
// constant (the pairings and anti-drift rationale live on the Spec
// field's doc in registry.go).
const hermesPrefix = "hermes"

// Hermes wires Nous Research's Hermes agent. env names like CUSTOM_BASE_URL
// are NOT supported by hermes (that was the round-5 H-NEW1 bug: the gateway
// key would ride a default api.openai.com base URL). ollama's hermes.go
// instead writes ~/.hermes/config.yaml with namespaced keys — we mirror that
// exactly, but scope writes to a sandbox dir so the user file is untouched
// (the HERMES_HOME env row keeps hermes reading our sandbox).
//
// Key channel, file value (readme-audit: placeholder-api-key): hermes has
// no documented env override for its key, so the real key is written into the
// sandboxed config.yaml like the other key-carrying sandbox runners (droid,
// pi, openclaw): the dir is 0700, holds nothing else, and the sandbox
// handle reclaims it when the launch returns. The env rows (HERMES_HOME and
// the model pin) ride the Wire like every sandbox runner's — the wiring
// class is sandbox, declared at registration.
var hermesSpec = Spec{
	Name:          "hermes",
	Description:   "Hermes (Nous Research, sandboxed config.yaml with the real key, no disk writes to ~/.hermes)",
	Wiring:        WiringSandbox,
	SandboxPrefix: hermesPrefix,
	// RequiresModel: model.default pins the session model (hermes honors it
	// strictly); the in-session /model picker only switches between what the
	// config already declares.
	RequiresModel: true,
	Find: func() (string, bool) {
		return findIn("hermes", homePaths("hermes", ".local/bin"))
	},
	BuildEnv: func(ep Endpoint, model string) (Wire, error) {
		// Key channel FileOnly — the real key lives in config.yaml only;
		// the child env carries no credential row.
		return buildSandbox(hermesPrefix, sandbox.FileOnly, ep.Token, func(sb *sandbox.Sandbox) ([]string, []string, error) {
			if err := hermesConfig(sb, ep, model); err != nil {
				return nil, nil, err
			}

			env := []string{"HERMES_HOME=" + sb.Root()}
			if model != "" {
				env = append(env, "HERMES_INFERENCE_MODEL="+model)
			}
			// Windows: hermes.exe resolves home via HOME vs LOCALAPPDATA,
			// but LOCALAPPDATA cannot point at our sandbox — hermes appends
			// "hermes" under it. Rely on HERMES_HOME, which takes precedence
			// (ollama hermes.go:497-500); no LOCALAPPDATA override is needed.
			return env, hermesModelArgs(model), nil
		})
	},
}

// hermesConfig writes the sandbox config.yaml (ollama hermes.go:268-312
// shape): model.{provider,default,base_url,api_key} + namespaced provider row.
// The real key rides in the sandboxed file (issue: placeholder-api-key): the
// sandbox is a 0700 dir reclaimed on exit, so credentialed-file risk equals
// the droid/pi/openclaw runners. The placeholder lives on only as the client
// shape reference; it must never reach a hermes config as a credential.
//
// The two default pins (model.default and the provider row's default_model)
// are written only when a real model rides in — see the shared rule on
// Catalog.Pin (hermes is not a carrier: it never resolves a catalog);
// hermes's site facts: single-model wiring with
// NO catalog fetch, and the RequiresModel gate means real launches always
// pass a model — a model-less library launch IS the degraded case here, so
// it writes no defaults instead of pinning both at the placeholder id.
// Source-verified: with no defaults on a custom provider, hermes boots
// with NO session model
// (its documented "not configured yet" sentinel — not a built-in
// fallback, and not an empty picker either); the session is unsendable
// until a model is chosen, which is the honest degrade.
func hermesConfig(sb *sandbox.Sandbox, ep Endpoint, model string) error {
	cfg := map[string]any{}

	modelSection := map[string]any{
		"provider": hermesProviderKey,
		"base_url": ep.OpenAIBase(),
		"api_key":  ep.Token,
	}
	providerRow := map[string]any{
		"name": ("KYA Gateway"),
		// Modern (config v12) resolver contract (hermes-agent
		// runtime_provider.py:825-851): for a named providers entry, api IS
		// the base-URL field the resolver reads (legacy custom_providers
		// lists used base_url; the entry must carry the URL itself).
		"api": ep.OpenAIBase(),
		// The named-custom credential channel is the ENTRY's api_key —
		// inline, not model.api_key (which only matters on the bare
		// custom/auto trust path) and not an env name (key_env indirection
		// runs through hermes's secret-scope wrapper and can be defeated by
		// hermes-side config; the inline literal inside the 0700 sandbox
		// reclaimed on exit cannot).
		"api_key": ep.Token,
		// Pin the transport instead of trusting URL sniffing
		// (_detect_api_mode_for_url reclassifies any base URL ending in
		// /anthropic or /anthropic/v1 as the Anthropic messages API).
		"transport": "openai_chat",
	}
	if model != "" {
		// Mirror model.default so startup resolves the launched model.
		modelSection["default"] = model
		providerRow["default_model"] = model
	}
	cfg["model"] = modelSection
	providers := map[string]any{
		hermesProviderKey: providerRow,
	}
	cfg["providers"] = providers

	return sb.WriteYAML("config.yaml", cfg)
}

// hermesModelArgs is the launcher's own argv (built inside the BuildEnv
// closure: the launcher path's argv channel is Wire.Args).
func hermesModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}
