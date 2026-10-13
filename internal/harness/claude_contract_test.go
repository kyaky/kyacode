package harness

import (
	"slices"
	"strings"
	"testing"
)

// Claude's credentials channel, pinned directly — the registry's first
// runner and the README quick-start's target, whose drift-prone details
// were only exercised through a passthrough shim before: the BARE base
// URL (claude appends /v1/messages itself, so the OpenAI-shaped /v1 base
// the shared endpoint helper produces would reach the gateway as
// /v1/v1/...), a set-but-EMPTY api-key export, and the model-alias pins.
func TestClaudeEnvCarriesTheBareBaseURL(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	env := claudeSpec.CarrierEnv(ep, "m1", Catalog{ids: []string{"m1"}, pin: "m1"})
	if !slices.Contains(env, "ANTHROPIC_BASE_URL=https://gw.example") {
		t.Errorf("env = %v, want the exact bare-base row (claude appends /v1/messages itself; an OpenAI-style /v1 base would double the suffix)", env)
	}
}

// The credential detail deliberately built: ANTHROPIC_API_KEY is
// SET-but-EMPTY (present as an empty value, never omitted). Some Claude
// Code flows consult ANTHROPIC_API_KEY before AUTH_TOKEN; exporting it
// empty — rather than letting an ambient shell value stand or dropping
// the row — keeps AUTH_TOKEN the single credential channel with the
// gateway.
func TestClaudeEnvSuppressesAmbientAPIKeyWithAnEmptyValue(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	env := claudeSpec.CarrierEnv(ep, "m1", Catalog{ids: []string{"m1"}, pin: "m1"})
	if !slices.Contains(env, "ANTHROPIC_API_KEY=") {
		t.Fatalf("env has no ANTHROPIC_API_KEY row at all: %v — the empty-export suppression must stay set, not omitted", env)
	}
	if !slices.Contains(env, "ANTHROPIC_AUTH_TOKEN=sk-test-12345") {
		t.Errorf("env = %v, want the gateway token as the one credential channel (exact row)", env)
	}
}

// The degraded single-row catalog (Resolve's outage shape) pins every
// alias slot to the launched id — the pre-carrier shape: an outage
// degrades the picker, never the wiring.
func TestClaudeCarrierEnvDegradesToLaunchedModelOnEveryAlias(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	env := claudeSpec.CarrierEnv(ep, "glm-5.3-flash-nvfp4", Catalog{ids: []string{"glm-5.3-flash-nvfp4"}, pin: "glm-5.3-flash-nvfp4"})
	joined := strings.Join(env, "\n")
	for _, row := range []string{
		"ANTHROPIC_MODEL=glm-5.3-flash-nvfp4",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=glm-5.3-flash-nvfp4",
		"ANTHROPIC_DEFAULT_SONNET_MODEL=glm-5.3-flash-nvfp4",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=glm-5.3-flash-nvfp4",
		"ANTHROPIC_DEFAULT_FABLE_MODEL=glm-5.3-flash-nvfp4",
		"CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3-flash-nvfp4",
	} {
		if !strings.Contains(joined, row) {
			t.Errorf("env missing the pin %q (got %v)", row, env)
		}
	}
}

// The carrier rotation is the picker feature: the launched model takes
// the opus slot (the entry choice) and heads the rows; sonnet/haiku/fable
// fill from the remaining chat rows in gateway order, skipping the
// non-chat families; every pinned row carries its label override so the
// picker shows the real id.
func TestClaudeCarrierEnvRotatesThePickerAliasesAcrossTheCatalog(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	cat := Catalog{ids: []string{
		"glm-5.3-flash",      // the launched model — Resolve puts it first
		"glm-5.3",            // operator-excluded id: filtered
		"bge-m3",             // non-chat family: filtered
		"qwen3.8-27b",        // kept
		"qwen3-asr-1.7b",     // non-chat family: filtered
		"qwen3.8-flash-next", // kept
	}, pin: "glm-5.3-flash"}
	env := claudeSpec.CarrierEnv(ep, "glm-5.3-flash", cat)
	joined := strings.Join(env, "\n")
	for _, row := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL=glm-5.3-flash",
		"ANTHROPIC_DEFAULT_SONNET_MODEL=qwen3.8-27b",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=qwen3.8-flash-next",
		"ANTHROPIC_DEFAULT_FABLE_MODEL=glm-5.3-flash", // rows exhausted — the entry model fills the last slot
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME=qwen3.8-27b",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_DESCRIPTION=KYA gateway model",
	} {
		if !strings.Contains(joined, row) {
			t.Errorf("env missing the rotated pin %q (got %v)", row, env)
		}
	}
	for _, leaked := range []string{"bge-m3", "qwen3-asr", "_SONNET_MODEL=glm-5.3\n", "_SONNET_MODEL=glm-5.3-fast"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("env leaked an excluded row into the picker rotation: %v", env)
		}
	}
}
