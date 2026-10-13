package harness

// Qwen wires Qwen Code via process env only (env-only, zero disk writes).
// Uses OpenAI-compatible gateway: OPENAI_BASE_URL + OPENAI_API_KEY.
var qwenSpec = Spec{
	Name:        "qwen",
	Description: "Qwen Code CLI (env injection, no disk writes)",
	Wiring:      WiringEnv,
	// RequiresModel: OPENAI_MODEL pins the session model; qwen has no picker
	// that would fix an empty one.
	RequiresModel: true,
	Find: func() (string, bool) {
		return findIn("qwen", homePaths("qwen", ".local/bin"))
	},
	ExtraArgs: qwenModelArgs,
	Env:       qwenEnv,
}

// qwenEnv: the injected qwen wiring rows. qwen's env auth-inference
// (getAuthTypeFromEnv) requires OPENAI_MODEL to exist, and the AUTH_ENV
// mappings consume it — so the given model rides OPENAI_MODEL verbatim.
// The model is always present here: qwen is RequiresModel, and both the
// CLI pre-gate and the canonical harness.Launch gate refuse a model-less
// qwen before qwenEnv can run (PR #9 fail-closed model gating). A model-
// less catalog-fetch branch lived at this site once (qwen's degrade-time
// placeholder export, the ticket-19 exception); it has been production-
// unreachable since that gating, and its deletion was re-ruled dated
// 2026-09-13 (the shared no-placeholder-picker-default rule is unchanged
// for its live catalog carriers — see Catalog.Pin / Resolve).
func qwenEnv(ep Endpoint, model string) []string {
	return []string{
		"OPENAI_BASE_URL=" + ep.OpenAIBase(),
		"OPENAI_API_KEY=" + ep.Token,
		"OPENAI_MODEL=" + model,
	}
}

// qwenModelArgs: --auth-type openai on EVERY invocation: selectedAuthType
// resolves argv.authType first, and persisted user settings
// (security.auth.selectedType) outrank env inference — without the pin, a
// saved qwen-oauth or gemini selection silently shadows the OPENAI_* env
// wiring.
func qwenModelArgs(model string) []string {
	args := []string{"--auth-type", "openai"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}
