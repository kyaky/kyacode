package harness

import (
	"slices"
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
	env := claudeSpec.Env(ep, "m1")
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
	env := claudeSpec.Env(ep, "m1")
	if !slices.Contains(env, "ANTHROPIC_API_KEY=") {
		t.Fatalf("env has no ANTHROPIC_API_KEY row at all: %v — the empty-export suppression must stay set, not omitted", env)
	}
	if !slices.Contains(env, "ANTHROPIC_AUTH_TOKEN=sk-test-12345") {
		t.Errorf("env = %v, want the gateway token as the one credential channel (exact row)", env)
	}
}

// The model contract is ONE model, the typed one: ANTHROPIC_MODEL, the
// picker's Default row, the legacy small-model channel, all four picker
// aliases (each with its label overrides) and the subagent default pin
// the same id, and the builder takes no catalog at all. The rotation this
// replaced filled the non-entry slots from the fetched catalog's sorted
// remainder, models the operator never chose; a slot whose model the
// gateway later dropped died mid-session. The pinned shape can only name
// a model the operator's launch already runs, so the [1m] context suffix
// rides through on every row verbatim.
func TestClaudeEnvPinsEveryAliasAndTheSubagentToTheTypedModel(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	env := claudeSpec.Env(ep, "glm-5.3-flash[1m]")
	for _, row := range []string{
		"ANTHROPIC_MODEL=glm-5.3-flash[1m]",
		"ANTHROPIC_DEFAULT_MODEL=glm-5.3-flash[1m]",
		"ANTHROPIC_SMALL_FAST_MODEL=glm-5.3-flash[1m]",
		"CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3-flash[1m]",
	} {
		if !slices.Contains(env, row) {
			t.Errorf("env missing the pinned row %q (got %v)", row, env)
		}
	}
	for _, alias := range []string{"OPUS", "SONNET", "HAIKU", "FABLE"} {
		for _, row := range []string{
			"ANTHROPIC_DEFAULT_" + alias + "_MODEL=glm-5.3-flash[1m]",
			"ANTHROPIC_DEFAULT_" + alias + "_MODEL_NAME=glm-5.3-flash[1m]",
			"ANTHROPIC_DEFAULT_" + alias + "_MODEL_DESCRIPTION=KYA gateway model",
		} {
			if !slices.Contains(env, row) {
				t.Errorf("env missing the pinned row %q (got %v)", row, env)
			}
		}
	}
}

// The model-choice rows are emitted UNCONDITIONALLY: the child inherits
// the operator's ambient shell env, and an inherited ANTHROPIC_DEFAULT_MODEL
// or ANTHROPIC_SMALL_FAST_MODEL naming another valid gateway id would let
// the picker's Default choice or a background small call reach a model the
// launch never chose. The builder never reads the ambient env; Go's
// os/exec keeps the last value for a duplicated key, so the pinned rows
// always win over whatever the shell exported.
func TestClaudeEnvModelRowsSurviveAmbientInheritance(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "claude-sonnet-4-5")
	t.Setenv("ANTHROPIC_DEFAULT_MODEL", "qwen3.8-27b")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "qwen3.8-flash-next")
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	env := claudeSpec.Env(ep, "glm-5.3-flash[1m]")
	for _, row := range []string{
		"ANTHROPIC_MODEL=glm-5.3-flash[1m]",
		"ANTHROPIC_DEFAULT_MODEL=glm-5.3-flash[1m]",
		"ANTHROPIC_SMALL_FAST_MODEL=glm-5.3-flash[1m]",
	} {
		if !slices.Contains(env, row) {
			t.Errorf("an ambient model var leaked past the pin: env = %v, want %q present", env, row)
		}
	}
}

// A model-less claude launch is refused upstream (RequiresModel), so the
// builder's model-less shape is the credential rows alone: no half-pinned
// aliases pointing at a model nobody typed.
func TestClaudeEnvWithoutAModelCarriesOnlyTheCredentialRows(t *testing.T) {
	ep := Endpoint{BaseURL: "https://gw.example", Token: "sk-test-12345"}
	env := claudeSpec.Env(ep, "")
	if len(env) != 3 {
		t.Fatalf("env = %v, want exactly the three credential rows", env)
	}
}
