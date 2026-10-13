package harness

// Claude wires Claude Code via process env only (ollama-style, zero disk
// writes). All vars are Claude Code's officially supported switches; they
// die with the child process. Direct connect: the user's own key is passed
// through as ANTHROPIC_AUTH_TOKEN (Bearer) against the gateway.
//
// Catalog carrier (the env class): the resolved gateway rows drive the
// /model picker through the ANTHROPIC_DEFAULT_*_MODEL pins — per the
// model-config docs, a pinned alias "shows one row for that model in
// place of the family's built-in rows" — so the picker's
// opus/sonnet/haiku/fable rows become real gateway models, labeled by the
// _NAME/_DESCRIPTION overrides, and switching among them stays on the
// wired base+token (the gateway serves every chat row on /v1/messages;
// verified across the chat families). Claude renders a BOUNDED selection:
// four alias slots, the launched model in the opus one — it is the entry
// model the operator chose — and sonnet/haiku/fable filled from the
// catalog's sorted remainder. The sorted order carries no capability
// meaning (the gateway publishes none), so no slot promises a cheaper
// model; the assignment is deterministic, not tiered. A degraded
// single-row catalog pins every alias to the launched model — the
// pre-carrier shape — so an outage degrades the picker, never the
// wiring. The native gateway-discovery fetch is NOT enabled: its
// usability filter keeps only /claude|anthropic/i ids, which would
// silently drop every gateway row.
var claudeSpec = Spec{
	Name:        "claude",
	Description: "Anthropic Claude Code (env injection, no disk writes)",
	Wiring:      WiringEnv,
	// RequiresModel: the typed model is the entry point — it takes the
	// opus picker slot and the session default; a model-less launch has
	// no entry row to offer.
	RequiresModel: true,
	Find: func() (string, bool) {
		return findIn("claude", homePaths("claude", ".local/bin", ".claude/local"))
	},
	Install:    claudeInstall,
	CarrierEnv: claudeCarrierEnv,
}

// claudeCarrierEnv is the env carrier's one row-builder. The model rides
// ANTHROPIC_MODEL (the documented session default, above settings but
// below /model); the four picker aliases rotate through the carrier rows
// as documented on the spec; the subagent model follows the entry choice.
func claudeCarrierEnv(ep Endpoint, model string, cat Catalog) []string {
	env := []string{
		"ANTHROPIC_BASE_URL=" + ep.BaseURL,
		"ANTHROPIC_AUTH_TOKEN=" + ep.Token,
		"ANTHROPIC_API_KEY=",
	}
	if model != "" {
		env = append(env, "ANTHROPIC_MODEL="+model)
	}
	rows := claudePickerRows(cat.Rows(), model)
	for i, alias := range []string{"OPUS", "SONNET", "HAIKU", "FABLE"} {
		id := model
		if i < len(rows) {
			id = rows[i]
		}
		if id == "" {
			continue
		}
		env = append(env,
			"ANTHROPIC_DEFAULT_"+alias+"_MODEL="+id,
			"ANTHROPIC_DEFAULT_"+alias+"_MODEL_NAME="+id,
			"ANTHROPIC_DEFAULT_"+alias+"_MODEL_DESCRIPTION=KYA gateway model",
		)
	}
	if model != "" {
		env = append(env, "CLAUDE_CODE_SUBAGENT_MODEL="+model)
	}
	return env
}

// claudeExcludedIDs are model ids the operator has kept out of claude's
// DEFAULT alias mapping (2026-10-12: glm-5.3 and glm-5.3-fast — the
// Mistral-backed pair; the codex picker still lists every chat-capable id
// dynamically). Exact-id set, deliberately NOT a fragment match:
// "glm-5.3" is a prefix of glm-5.3-flash, which stays served. An
// explicit launch of an excluded id is still honored — the operator
// asked for that model by name; the exclusion governs the auto-generated
// rotation, not the typed argument.
var claudeExcludedIDs = map[string]bool{
	"glm-5.3":      true,
	"glm-5.3-fast": true,
}

// claudePickerRows extracts the chat-family ids in carrier order — the
// launched model first (Resolve prepends it), then the catalog's sorted
// remainder — deduped, for the alias rotation: the launched model's own
// family is always kept even when it matches a filtered one, because the
// operator launched it by name.
func claudePickerRows(rows []map[string]any, cliModel string) []string {
	out := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" || seen[id] {
			continue
		}
		if id != cliModel && (!GatewayChatModel(id) || claudeExcludedIDs[id]) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func claudeInstall() (bin string, args []string) {
	if runtimeGOOS() == "windows" {
		return "powershell", []string{"-NoProfile", "-Command", "irm https://claude.ai/install.ps1 | iex"}
	}
	return "bash", []string{"-c", "curl -fsSL https://claude.ai/install.sh | bash"}
}
