package harness

// Claude wires Claude Code via process env only (ollama-style, zero disk
// writes). All vars are Claude Code's officially supported switches; they
// die with the child process. Direct connect: the user's own key is passed
// through as ANTHROPIC_AUTH_TOKEN (Bearer) against the gateway.
//
// The model story is one model, the typed one: ANTHROPIC_MODEL carries the
// session default, ANTHROPIC_DEFAULT_MODEL pins the picker's Default row,
// ANTHROPIC_SMALL_FAST_MODEL pins the legacy small-model channel (both are
// also read from the ambient env, so leaving them unset would let an
// inherited value name a model the operator never launched), all four
// picker aliases (opus/sonnet/haiku/fable, each with its label overrides)
// pin the same id, and the subagent default follows it. Every channel
// Claude Code can call on its own, background tasks included, therefore
// names a model the operator's session is already running; no row can
// outlive the model it names. CLAUDE_CODE_SUBAGENT_MODEL_FORCE is
// deliberately NOT emitted: a subagent's authored model is an
// operator-written choice, not a stray default, and alias-named subagent
// models already resolve through the pins above. An earlier design
// rotated the non-entry slots through the fetched catalog's sorted
// remainder: that filled slots with models the operator never chose, and
// a slot whose model the gateway later dropped died mid-session. The
// all-pinned shape is that design's degraded shape promoted to the only
// shape, and claude is no longer a catalog carrier: its launch performs
// no fetch at all, so a gateway outage can neither slow nor shape a
// claude launch. The /model picker still renders (one row per pinned
// alias, all naming the entry model), and switching among rows stays on
// the wired base+token.
//
// The native gateway-discovery fetch is NOT enabled: its usability filter
// keeps only /claude|anthropic/i ids, which would silently drop every
// gateway row.
var claudeSpec = Spec{
	Name:        "claude",
	Description: "Anthropic Claude Code (env injection, no disk writes)",
	Wiring:      WiringEnv,
	// RequiresModel: the typed model is the entry point and the only model
	// the wiring names anywhere; a model-less launch has no model to pin.
	RequiresModel: true,
	Find: func() (string, bool) {
		return findIn("claude", homePaths("claude", ".local/bin", ".claude/local"))
	},
	Install: claudeInstall,
	Env:     claudeEnv,
}

// claudeEnv is claude's one row-builder. The typed model rides
// ANTHROPIC_MODEL (the documented session default, above settings but
// below /model), fills the picker's Default row and the legacy small-model
// channel, all four picker aliases, and the subagent default: one id
// everywhere.
func claudeEnv(ep Endpoint, model string) []string {
	env := []string{
		"ANTHROPIC_BASE_URL=" + ep.BaseURL,
		"ANTHROPIC_AUTH_TOKEN=" + ep.Token,
		"ANTHROPIC_API_KEY=",
	}
	if model != "" {
		env = append(env,
			"ANTHROPIC_MODEL="+model,
			// Both are read from the ambient env too (verified in the
			// 2.1.319 binary): an inherited value naming another model
			// would let the picker's Default choice or a background small
			// call reach a model the launch never chose.
			"ANTHROPIC_DEFAULT_MODEL="+model,
			"ANTHROPIC_SMALL_FAST_MODEL="+model,
		)
		for _, alias := range []string{"OPUS", "SONNET", "HAIKU", "FABLE"} {
			env = append(env,
				"ANTHROPIC_DEFAULT_"+alias+"_MODEL="+model,
				"ANTHROPIC_DEFAULT_"+alias+"_MODEL_NAME="+model,
				"ANTHROPIC_DEFAULT_"+alias+"_MODEL_DESCRIPTION=KYA gateway model",
			)
		}
		env = append(env, "CLAUDE_CODE_SUBAGENT_MODEL="+model)
	}
	return env
}

func claudeInstall() (bin string, args []string) {
	if runtimeGOOS() == "windows" {
		return "powershell", []string{"-NoProfile", "-Command", "irm https://claude.ai/install.ps1 | iex"}
	}
	return "bash", []string{"-c", "curl -fsSL https://claude.ai/install.sh | bash"}
}
