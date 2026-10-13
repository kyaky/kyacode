package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"
)

// The /model picker serves the ACTIVE model catalog. For a custom
// provider without an explicit catalog that is codex's bundled OpenAI
// list — every row 404s against the gateway, which is why the picker was
// useless under kyacode. `model_catalog_json` replaces the catalog
// wholesale with a file the wiring writes, so the picker lists the
// gateway's own models and the per-model reasoning-effort selector works
// against real ids. Two facts make the file non-trivial:
//
//  1. A catalog model WITHOUT model_messages launches sessions with EMPTY
//     base instructions (render_model_instructions warns and returns ""
//     for a template-less model — there is no bundled fallback), and the
//     template text ships only inside the codex binary. So the entry
//     SHAPE is harvested from the installed codex itself (codex debug
//     models): the highest-priority picker-visible entry, cloned and
//     re-identitied per gateway row — every serde-required field rides
//     along, the instruction template is the one the installed codex
//     maintains, and the whole schema stays in lockstep with whatever
//     codex version is installed. No vendored catalog to drift.
//  2. The gateway serves no capability metadata, so the rows cannot be
//     trusted to be chat models: the same /v1/models lists embedding,
//     rerank, ASR and OCR specialists that would 404 on /v1/responses.
//     Until the gateway tags capabilities, the obvious non-chat families
//     are filtered from the picker rows (the explicitly launched model
//     always stays — the operator asked for it by name).
//
// A failed harvest degrades to NO catalog file: the launch proceeds
// exactly as before the catalog existed (bundled picker, fallback
// metadata), never blocked by an auxiliary enhancement. The harvest's
// latency is part of the launch latency contract recorded at
// gateway_models.go (two sequential bounded waits for a codex launch);
// the re-hunt acceptance note lives at codex.go, so each fact is stated
// in one place.

// codexEffortLevels is the reasoning-effort row set every catalog entry
// carries. The gateway accepts explicit effort values on the responses
// wire (verified against glm-5.3-flash with low and high), and the levels
// double as the /model picker's effort submenu. medium is the entry
// default, matching codex's own default reasoning level.
func codexEffortLevels() []map[string]string {
	return []map[string]string{
		{"effort": "low", "description": "Fast responses with lighter reasoning"},
		{"effort": "medium", "description": "Balances speed and reasoning depth for everyday tasks"},
		{"effort": "high", "description": "Greater reasoning depth for complex problems"},
	}
}

// codexCatalogJSON renders the gateway rows as a codex model catalog
// (the ModelsResponse shape load_catalog_json parses): one entry per
// chat-capable row, each a clone of the harvested entry re-identitied for
// the gateway (slug/display/description/priority overwritten, visibility
// and API support forced on, the effort rows ours). rows are the carrier
// handover's rows ({"id": id} maps, Resolve order — the CLI model first).
// cliModel is always emitted even when it matches a filtered family: the
// operator launched it by name. ok is false when there is nothing to
// write (no rows, no entry) and the caller skips the catalog entirely.
func codexCatalogJSON(rows []map[string]any, cliModel string, entry map[string]any) ([]byte, bool) {
	if entry == nil || len(rows) == 0 {
		return nil, false
	}
	entryBody, err := json.Marshal(entry)
	if err != nil {
		// The entry arrived from json.Unmarshal — JSON-native by
		// construction; the must-idiom contract the sandbox WriteJSON
		// bodies already hold.
		panic("codex catalog: re-marshal harvested entry: " + err.Error())
	}
	models := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		if id != cliModel && !GatewayChatModel(id) {
			continue
		}
		var clone map[string]any
		if err := json.Unmarshal(entryBody, &clone); err != nil {
			panic("codex catalog: clone harvested entry: " + err.Error())
		}
		clone["slug"] = id
		clone["display_name"] = id
		clone["description"] = "KYA gateway model"
		clone["visibility"] = "list"
		clone["supported_in_api"] = true
		clone["priority"] = len(models) + 1
		clone["default_reasoning_level"] = "medium"
		clone["supported_reasoning_levels"] = codexEffortLevels()
		// The harvested entry is a codex-backend model: its capability
		// fields describe what OpenAI's backend serves, and codex starts
		// emitting wire shapes for them the gateway refuses (observed:
		// "Unsupported Responses API input item type: 'additional_tools'"
		// — the experimental tools list alone did it). Reset every one to
		// the fallback shape — the metadata an unknown model gets, and
		// the shape every verified-working kyacode session used.
		clone["experimental_supported_tools"] = []any{}
		delete(clone, "tool_mode")
		delete(clone, "multi_agent_version")
		clone["node_repl_auto_review_required"] = false
		clone["node_repl_disabled"] = false
		clone["use_responses_lite"] = false
		// The entry's service tiers are OpenAI-backend offerings (priority
		// tier etc.); an inherited tier plus a catalog advertising tiers
		// made ollama's App warn on every launch (upstream #18879) — their
		// fix ships empty tiers with a null default, so do the same: the
		// gateway has no tier concept.
		clone["service_tiers"] = []any{}
		clone["default_service_tier"] = nil
		if _, ok := clone["model_messages"].(map[string]any); !ok {
			clone["model_messages"] = map[string]any{}
		}
		if _, ok := clone["model_messages"].(map[string]any)["instructions_template"]; !ok {
			clone["model_messages"].(map[string]any)["instructions_template"] = ""
		}
		models = append(models, clone)
	}
	if len(models) == 0 {
		return nil, false
	}
	body, err := json.MarshalIndent(map[string]any{"models": models}, "", "  ")
	if err != nil {
		// JSON-native values only — unreachable by construction; the
		// must-idiom contract the sandbox WriteJSON bodies already hold.
		panic("codex catalog: marshal: " + err.Error())
	}
	return body, true
}

// codexCatalogEntryHarvest is the seam the codex wiring tests stub: given
// a CODEX_HOME, acquire the catalog entry codex itself would feature (its
// own default-marking rule: lowest priority number among picker-visible
// entries carrying a non-empty instruction template) — locating the
// installed binary, rendering the dump, parsing it — and whether the
// harvest succeeded. The WHOLE acquisition lives behind the seam (the
// binary hunt included): a machine without codex installed must behave
// identically to one whose spawn failed, and a stub must cover both.
var codexCatalogEntryHarvest = func(home string) (map[string]any, bool) {
	codexPath, found := findIn("codex", nil)
	if !found {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codexPath, "debug", "models")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return parseCodexCatalogEntry(out)
}

// parseCodexCatalogEntry extracts the harvest target from a
// `codex debug models` dump. Pure so the selection rules are testable
// without spawning codex.
func parseCodexCatalogEntry(dump []byte) (map[string]any, bool) {
	var parsed struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(dump, &parsed); err != nil {
		return nil, false
	}
	var best map[string]any
	bestPriority := -1
	for _, model := range parsed.Models {
		if model["visibility"] != "list" {
			continue
		}
		messages, _ := model["model_messages"].(map[string]any)
		if messages == nil {
			continue
		}
		template, _ := messages["instructions_template"].(string)
		if template == "" {
			continue
		}
		priority, _ := model["priority"].(float64)
		if best == nil || int(priority) < bestPriority {
			bestPriority = int(priority)
			best = model
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}
