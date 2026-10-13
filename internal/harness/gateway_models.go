package harness

import (
	"fmt"
	"strings"

	"kyacode/internal/gateway"
)

const (
	// PlaceholderModelID is the degradation target when the gateway
	// catalog is unreachable and no CLI model was given: a transient
	// /v1/models outage must not block a launch that could otherwise
	// work, so the degraded config still carries a catalog entry the
	// harness can see (never a hardcoded real id it may not serve).
	// Wire-visible: every degraded config carries this exact string, so a
	// rename is a wire change — fixture tests pin the byte shapes.
	PlaceholderModelID = "kya-model"

	// providerScopePrefix is the provider key that scopes the gateway's
	// models: some harnesses only accept the "<prefix>/<id>" compose form
	// in provider-scoped model references (MPI/omp modelRoles, --model
	// args, provider-priced defaults). It is a harness-internal fact —
	// the CLI boundary asks its Spec (CheckModelArg), it does not
	// re-derive the shape against an exported constant.
	// Wire-visible: every scoped composer's written provider key AND every
	// composed "<prefix>/<id>" reference carry this exact string, so a
	// rename is a wire change — fixture tests pin the byte shapes.
	providerScopePrefix = "kya"
)

// CheckModelArg is the provider-scope gate over a CLI-typed model
// argument (ticket 21's rule, owned here with the vocabulary it guards):
// a model already shaped "<prefix>/<id>" would compose a SECOND scope
// layer for the runners that hand the harness the scoped form —
// producing kya/kya/…, an id no gateway serves. Bare-composing runners
// may genuinely receive a served id named like the scope, so the refusal
// is runner-aware (declared fact: Spec.ScopedComposer). The empty model
// is every gate's business, not this one.
//
// The gate sits in harness.Launch's gate order (checked after validate,
// before the requires-model gate and the binary hunt) and in the CLI's
// pre-endpoint pre-gate (cmdLaunch, so the actionable refusal wins over
// an endpoint error) — one sentence, rendered here, at both sites.
func (s Spec) CheckModelArg(model string) error {
	if model == "" || !s.ScopedComposer {
		return nil
	}
	if strings.HasPrefix(model, providerScopePrefix+"/") {
		return fmt.Errorf("provider-scoped model argument — type the bare id (%s scopes it itself); see `kyacode help`", s.Name)
	}
	return nil
}

// scopedModelID composes the provider-scoped form of a gateway model id
// (the "<prefix>/<id>" shape). Single compose rule for every harness —
// the prefix used to exist as a bare literal repeated across runner
// files, which is how drifts happen.
func scopedModelID(id string) string {
	return providerScopePrefix + "/" + id
}

// Catalog is the launch's resolved Model catalog decision: the merged
// rows a carrier embeds (the CLI model first, then every gateway model,
// sorted — the Model catalog shape) plus the default-pin verdict. The
// value is OPAQUE to carriers by construction — the fields are
// unexported and built only by Resolve — because the sentinel-row
// contract's two halves (the degraded LIST keeps the placeholder row;
// the default PIN is withheld on degrade) are decided once here and must
// never be re-derived from the rows a carrier received (CONTEXT's Wire
// rule, applied to the catalog: a carrier branching on fetched-ness or
// re-computing a pin from IDs would fork the pairing).
//
// The pin verdict arrives in both rendered forms a carrier may need —
// bare and provider-scoped (Pin) — because the FORM is placement
// knowledge: pi writes the bare id into its settings pair while omp and
// openclaw compose the scoped form into their pickers, and both are
// ScopedComposer runners. Choosing the form stays per-runner; deriving
// the VALUE does not.
type Catalog struct {
	ids []string // carrier order, never empty (Degrade keeps the sentinel row)
	pin string   // "" = write no default pin (degraded fetch, no CLI model)
}

// Resolve builds the launch's Model catalog decision. Any fetch failure
// degrades to the CLI model (or PlaceholderModelID) — a transient
// /v1/models outage must not block a launch that could otherwise work.
// Resolve never errors: Degrade is by design.
//
// THE SENTINEL-ROW CONTRACT (standing, 2026-09-07): the degraded LIST
// deliberately keeps the placeholder row — it is the only selectable
// row an auto-selecting harness (pi/omp/openclaw pick a startup model
// from whatever rows exist) can land on, so it keeps their implicit
// degrade selection ON the gateway. Paired with the pin rule below: the
// pin is honest, the row is the picker-bait — verified per-harness
// against primary sources (pi/omp/openclaw are auto-selectors; dsh and
// droid revert to their built-in, non-gateway defaults on absence).
// Removing the row is a STANDING REJECTED design: if either half
// changes, both halves are re-evaluated together against those
// per-harness facts.
//
// THE PIN RULE (ticket 19, ex-DefaultModel): the resolved pin is
//
//   - cliModel when one was typed (ok=true always — the user chose it).
//   - on a fetched Model catalog with no CLI model: the first id the
//     catalog serves that is not named like the placeholder (ok=true);
//     when the gateway serves ONLY a placeholder-named id, that id comes
//     back (an explicitly served id is real whatever it is named — but
//     ok=true does NOT mean the value "isn't the placeholder": a sole
//     placeholder-named entry yields it; classify by value, never by ok).
//   - on a degraded fetch with no CLI model: no pin at all (ok=false).
//     A pin at PlaceholderModelID would point the harness's own picker
//     at a nonexistent model (the omp rule from #08). The rule has no
//     exceptions: the one recorded exception (qwen's degrade-time
//     OPENAI_MODEL placeholder export) was re-ruled moot 2026-09-13 —
//     qwen is RequiresModel, so a model-less qwen is
//     production-unreachable and never resolves a catalog.
//
// LATENCY CONTRACT (accepted, 2026-09-06; extended 2026-10-13): the
// launch boundary resolves the catalog SYNCHRONOUSLY inside wiring
// build — one Resolve per launch, performed only for a Catalog carrier —
// bounded by the gateway module's request timeout (the one transport
// constant it owns; ticket 05). A carrier BODY may add its own
// synchronous acquisition inside the wiring build: today exactly one
// does — codex's catalog-entry harvest spawns the installed codex under
// its own independent 10-second budget (an intentional literal, not the
// gateway constant; the negative-leverage note on threading the binary
// path through the carrier signatures lives at codex.go). A blackhole
// gateway (packet-drop, never refusing) stalls such a launch the FULL
// gateway timeout, and a hung codex spawn then adds its own: the two
// sequential waits can consume roughly twenty seconds, excluding other
// launch work — a rough sum, never a hard ceiling (a descendant
// retaining the spawn's output pipes can outlive its cancellation). No
// error is surfaced on the gateway wait (the degrade is by design).
// That tradeoff is deliberate today: an asynchronous or cached catalog
// is the honest-list-degrade design territory, not something env
// assembly can opt out of per site.
//
// EMPIRICS: what each harness actually does at boot WITHOUT a default
// was verified against primary sources before this design was accepted
// — do not reason from the "no default is honest" stance here without
// that per-harness table: two carriers (dsh, droid) revert to their
// built-in, non-gateway defaults on absence, which is ticket 24's
// design territory, not a local claim.
func Resolve(ep Endpoint, cliModel string) Catalog {
	ids, err := ep.Base().Models()
	if !gateway.CatalogOK(ids, err) {
		if cliModel == "" {
			// Degrade, no CLI model: the sentinel row stays (the list
			// half); no pin (the pin half) — the pairing decided here,
			// once.
			return Catalog{ids: []string{PlaceholderModelID}}
		}
		// Degrade with a typed model: the model wins both halves — it is
		// the only row AND the pin (a degraded fetch never blocks a
		// launch that could otherwise work).
		return Catalog{ids: []string{cliModel}, pin: cliModel}
	}
	out := make([]string, 0, len(ids)+1)
	if cliModel != "" {
		out = append(out, cliModel)
	}
	for _, id := range ids {
		if id == cliModel {
			continue
		}
		out = append(out, id)
	}
	pin := ""
	if cliModel != "" {
		pin = cliModel
	} else {
		for _, id := range out {
			if id != "" && id != PlaceholderModelID && id != scopedModelID(PlaceholderModelID) {
				pin = id
				break
			}
		}
		if pin == "" {
			pin = PlaceholderModelID
		}
	}
	return Catalog{ids: out, pin: pin}
}

// IDs returns the carrier-order rows (the CLI model first when typed,
// then every gateway model sorted; the degraded list is the sentinel row
// alone or the CLI model alone). The slice is a copy: mutating it cannot
// reach the launch's decision.
func (c Catalog) IDs() []string {
	out := make([]string, len(c.ids))
	copy(out, c.ids)
	return out
}

// Rows maps the catalog ids to the bare model entries ({"id": id}) that
// pi, omp, dsh and openclaw config carriers list. One shared builder:
// the shape used to be copied inline in every carrier (and pi's
// extraction never got applied back), which is how per-runner drift
// happens. droid's customModels rows are a genuinely different
// enterprise schema (identity fields per row) and opencode's models map
// is a name-keyed object — both build from IDs() instead.
func (c Catalog) Rows() []map[string]any {
	out := make([]map[string]any, 0, len(c.ids))
	for _, id := range c.ids {
		out = append(out, map[string]any{"id": id})
	}
	return out
}

// Pin returns the default-pin verdict in both rendered forms: the bare
// id and the provider-scoped compose. ok is false exactly on a degraded
// fetch with no CLI model — write NO default pin then (the sentinel-row
// contract's second half). ok=true does not mean the value "isn't the
// placeholder": a gateway serving ONLY a placeholder-named id yields it
// (an explicitly served id is real, whatever it is named) — classify by
// value, never by ok.
func (c Catalog) Pin() (bare, scoped string, ok bool) {
	if c.pin == "" {
		return "", "", false
	}
	return c.pin, scopedModelID(c.pin), true
}

// gatewayNonChatFragments are the model-id substrings kyacode filters
// from harness picker rows. The gateway serves no capability metadata, so
// ids cannot be trusted to be chat models: these families are embedding /
// rerank / speech / OCR specialists with no chat route, and a picker row
// that can only produce errors is noise at best and a mis-click trap at
// worst. Stopgap until the gateway publishes capability metadata.
var gatewayNonChatFragments = []string{
	"bge-", "reranker", "whisper", "-asr", "aligner", "mineru",
}

// GatewayChatModel reports whether a gateway model id belongs to a family
// a harness can actually serve on its chat wire. Exported because two
// carriers consume it (the codex catalog renderer and the claude alias
// rotation); the fragments are gateway-wide facts, not per-harness ones.
// Operator exclusions (claude's Mistral pair) live with the carrier that
// mandated them — the codex catalog deliberately shows every chat-capable
// id the key can reach, dynamically.
func GatewayChatModel(id string) bool {
	lowered := strings.ToLower(id)
	for _, fragment := range gatewayNonChatFragments {
		if strings.Contains(lowered, fragment) {
			return false
		}
	}
	return true
}
