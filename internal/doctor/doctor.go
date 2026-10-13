// Package doctor: pre-flight checks that gate the refactor and future
// launches. Asserts the gateway natively speaks all three wire protocols
// (Stage 0 of the plan — the single assumption for "no daemon / direct
// connect" design).
//
// Round-6 fixes: 401/403 are hard failures (never "route exists");
// probes run in PARALLEL with an overall budget (worst case ~10s, not
// 75s); every wire probe uses the same code predicate (200; 400; 404
// only with per-protocol probe-miss evidence; 401/403/000 fail) so the
// models row can't contradict the wire rows; GET /v1/models requires a
// real parsed catalog; and redirects are refused outright by the
// gateway module (typed ErrRefusedRedirect), so a redirecting gateway
// can't exfiltrate the key mid-probe.
//
// 2026-09-14 operator report (verdict fixed 2026-09-15): an
// Anthropic-compatible gateway answers
// the probe-model miss with Anthropic's DOCUMENTED error shape —
// top-level type:"error", error.type "not_found_error", no OpenAI
// error.code (platform.claude.com/docs/en/api/errors) — and the old
// predicate (the OpenAI-only substring "model_not_found") red-flagged
// both anthropic rows on a healthy gateway. The probe-miss verdict is now
// per-protocol (probeMiss): Anthropic accepts not_found_error with the
// probe model echoed somewhere in the body (the echo separates "the
// gateway looked the model up" from "the route does not exist" —
// Anthropic's not_found_error covers BOTH, so shape alone cannot
// confirm), and OpenAI accepts the spec'd error.code "model_not_found".
// request_id is NEVER consulted: it is a debugging aid, not part of any
// validity contract, and field order is irrelevant (parsed fields, not
// positions).
//
// Beta-header tolerance is RELATIVE and NON-BLOCKING (2026-09-15): the
// row answers "does the anthropic-beta header change the gateway's
// answer?" — equal answers mean the header changed nothing (which does
// NOT prove beta features work upstream; the row says so), and any
// observed difference against a healthy plain probe (a different status
// with plain 200, or the same status with a body that lost the
// probe-miss evidence the plain row had) WARNS instead of failing:
// kyacode's own launch wiring sends no beta header, so a rejecting
// gateway cannot fail a launch kyacode wires, and the official API
// itself returns 400 for an invalid or unauthorized beta name
// (platform.claude.com/docs/en/api/beta-headers) — a strict gateway is
// protocol-compliant, not broken. Warn is structural, not a rendering
// choice: a warn row keeps Pass=true, so the aggregate, the CLI verdict,
// and the row's own "does not block" sentence cannot disagree.
//
// Ticket 05: the keyed transport, the /v1 join, the body limit, and the
// models fetch are the internal/gateway module's — doctor composes
// gateway.Base at its scalar boundary and keeps only probe POLICY: the
// routes and bodies, the probe-specific headers, the verdict ladder, and
// the relative beta comparison. The models row IS the launch path's fetch
// (same keyed request, same parse, same error sentence), so a preflight
// verdict cannot disagree with what a launch will actually do.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"kyacode/internal/gateway"
)

// Check is one probe result. Warn marks attention WITHOUT blocking, and
// it is structural: a warn row keeps Pass=true, so Result.AllPassed
// ignores it by construction — the aggregate, the CLI verdict, and the
// row's own "does not block" sentence cannot disagree.
type Check struct {
	Name string
	Pass bool
	Warn bool
	Info string
}

// Result bundles all checks.
type Result struct {
	Checks    []Check
	AllPassed bool
	Warned    bool // at least one row carries a Warn
}

// Probe-row names are shared facts (the glued rows below resolve each
// other, and doctor_test pins by the same strings) — one declaration so a
// rename is one edit instead of a silent vacated pin.
const (
	nameMessagesPlain = "anthropic /v1/messages"
	nameBetaTolerance = "beta header tolerance"
	nameModelsRow     = "list /v1/models"
)

// The two entry protocols a wire probe speaks. The 404 error body is
// classified PER PROTOCOL (probeMiss) — Anthropic's documented error
// shape (top-level type:"error", error.type, error.message) and OpenAI's
// (error.code, error.type, error.param) are different contracts.
const (
	protoAnthropic = "anthropic"
	protoOpenAI    = "openai"
)

// probeModel is the deliberate nonexistent model every wire probe sends:
// a NEGATIVE probe that costs no tokens — the gateway's answer to it is
// the route-exists evidence. A gateway that looked the model up echoes
// the id somewhere in its error, which is exactly what the Anthropic 404
// classifier keys on (probeMiss).
const probeModel = "__doctor_probe__"

const probeBody = `{"model":"` + probeModel + `","max_tokens":8,"messages":[{"role":"user","content":"ping"}]}`

const responsesProbeBody = `{"model":"` + probeModel + `","input":"ping","max_output_tokens":8}`

// probeRow is one declared wire probe: policy the ticket keeps with
// doctor (routes, bodies, probe-specific headers) over the module's
// transport.
type probeRow struct {
	name        string
	proto       string // the entry protocol the route speaks — decides how a 404 body is classified
	method      string
	routeSuffix string // joined against the module's one /v1 rule; doctor composes no URLs
	body        string
	extra       map[string]string
}

// gatewayErr is the union error shape across the two wire protocols:
// Anthropic's {"type":"error","error":{"type","message"}} and OpenAI's
// {"error":{"code","message","type","param"}} share the error object
// nesting, so one lenient struct reads both. Only the fields a verdict
// READS are modeled: error.type and error.code. The top-level "type",
// OpenAI's "param", and the prose message are deliberately absent — the
// probe-model echo is searched in the RAW body (probeMiss), so it lands
// wherever the gateway puts it, and a field parsed but never read is
// dead surface.
type gatewayErr struct {
	Error struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"error"`
}

// probeMiss classifies a 404 body as (evidence, confirmed, phrase):
// evidence = the body is a per-protocol model-not-found error at all (the gateway
// parsed the request); confirmed = the evidence is specifically the
// PROBE MODEL's lookup, not a path 404. Anthropic's not_found_error
// covers both the path and the resource (the errors doc: "Check the
// endpoint path and any resource IDs in the request URL"), so shape
// alone cannot confirm — the probe model id echoed ANYWHERE in the body
// (case-insensitive, order-independent, wording-independent: the echo is
// client-chosen data, not fixed prose) is what separates the two; an
// echo-less not_found_error comes back unconfirmed and the row FAILS
// with an "unconfirmed" annotation rather than passing on a guess. The
// OpenAI route accepts the spec'd error.code "model_not_found" — that
// code IS the model-lookup claim, no echo needed (the old contract, now
// field-parsed instead of substring). An anthropic route MAY also carry
// an OpenAI-shaped model_not_found (shape-translating proxies do): the
// same evidence bar as the openai rows applies. The third return is the
// confirmation KIND — the phrase the classified display renders — keyed
// on WHICH branch confirmed, because an anthropic-protocol 404 carrying
// an OpenAI-shaped body confirms via the code, not the not_found_error
// echo. A body that is not
// parseable JSON falls back to the raw quoted tokens — the same tokens,
// so truncation or trailing noise cannot turn a real probe-miss into a
// "route absent" verdict (and vice versa: plain-text and HTML 404s carry
// neither token and stay route-absent failures).
func probeMiss(proto, body string) (evidence, confirmed bool, phrase string) {
	var e gatewayErr
	parsed := json.Unmarshal([]byte(body), &e) == nil
	low := strings.ToLower(body)
	// Case discipline is deliberate per role: the enum tokens
	// ("not_found_error", "model_not_found") are spec'd field VALUES and
	// match case-SENSITIVELY as quoted JSON tokens, while the echoed
	// probe-model id is gateway-handled data and matches
	// case-INSENSITIVELY against the lowercased body. The third return is
	// the confirmation KIND — the display phrase for the classified row —
	// keyed on WHICH branch confirmed, not merely the protocol: an
	// anthropic-protocol 404 carrying an OpenAI-shaped body (a
	// shape-translating proxy) confirms via the code, so its display must
	// say so, not claim the not_found_error echo.
	switch proto {
	case protoAnthropic:
		if parsed && e.Error.Type == "not_found_error" {
			if strings.Contains(low, probeModel) {
				return true, true, "the gateway named the probe model in its not_found_error"
			}
			return true, false, ""
		}
		if e.Error.Code == "model_not_found" || strings.Contains(body, `"model_not_found"`) {
			return true, true, "error.code model_not_found"
		}
		if !parsed && strings.Contains(body, `"not_found_error"`) {
			if strings.Contains(low, probeModel) {
				return true, true, "the gateway named the probe model in its not_found_error"
			}
			return true, false, ""
		}
		return false, false, ""
	case protoOpenAI:
		if parsed && e.Error.Code == "model_not_found" {
			return true, true, "error.code model_not_found"
		}
		if strings.Contains(body, `"model_not_found"`) {
			return true, true, "error.code model_not_found"
		}
		return false, false, ""
	}
	// An unknown proto value fails CLOSED (no evidence = route-absent
	// verdict) — a misspelled protocol can never silently inherit the
	// OpenAI branch.
	return false, false, ""
}

// verdict maps a module exchange to (pass, annotated info). The ladder:
// 200 = fully OK; 400 = route exists (the probe request shape was still
// parsed); 404 = route exists only with per-protocol probe-miss evidence
// (probeMiss — a bare, HTML, or echo-less not_found_error 404 means the
// route is absent or unconfirmable and FAILS); 401/403 = auth failure,
// NEVER a pass; 429/5xx and transport failures (including typed redirect
// refusals) fail with the module's own error sentence. The status+body
// info keeps every class distinguishable: unreachable, auth, path-404,
// expected probe-miss, 400 shape, 429, 5xx.
func verdict(p probeRow, pr gateway.Probed) Check {
	pass := false
	info := infoOf(pr)
	if pr.Err == nil {
		switch pr.Status {
		case http.StatusOK, http.StatusBadRequest:
			pass = true
		case http.StatusNotFound:
			evidence, confirmed, phrase := probeMiss(p.proto, pr.Body)
			if evidence && confirmed {
				// The EXPECTED outcome is displayed as a classified
				// conclusion, not the raw exchange: the confirmation KIND
				// is named (keyed on which probeMiss branch confirmed),
				// and the full body stays on the rows that need debugging
				// (unconfirmed, failing) — the raw 404 JSON repeated on
				// every expected-miss row was noise.
				pass = true
				info = fmt.Sprintf("%d — probe miss confirmed (%s)", pr.Status, phrase)
			} else if evidence {
				info += " — probe-model miss unconfirmed: the 404 names no probe model"
			}
		}
	}
	return Check{Name: p.name, Pass: pass, Info: info}
}

// infoOf renders one exchange: the module's error sentence for failures
// that never completed or were refused, the status+body taxonomy
// otherwise. 401/403 keep their identity — never masked to 502, so
// callers can distinguish "bad key" from "gateway down".
func infoOf(pr gateway.Probed) string {
	if pr.Err != nil {
		return pr.Err.Error()
	}
	if pr.Status == http.StatusOK {
		return fmt.Sprintf("200 (%d bytes)", len(pr.Body))
	}
	return fmt.Sprintf("%d: %s", pr.Status, pr.Body)
}

// modelCheck renders the models row. Its Info on failure is the fetch's
// OWN error sentence — byte-identical to what `kyacode models` prints and
// exactly the error class a launch's catalog fetch hits (AC-6: the
// preflight and the launch fail with the same message). A well-formed
// 200 with zero usable ids stays GREEN with an annotation: the standing
// honest-list degrade (the sentinel row) has launches proceeding on such
// a gateway BY DESIGN, so a red verdict would declare broken what the
// system deliberately tolerates — but doctor must not be silent about
// it either. (Garbage-body 200s still fail: parse errors are real
// fetch errors, and a launch degrades silently on them — exactly the
// case this row exists to surface.)
func modelCheck(ids []string, err error) Check {
	if err != nil {
		return Check{Name: nameModelsRow, Pass: false, Info: err.Error()}
	}
	// The green-annotated branch IS the shared bar's negative half — the
	// bar is evaluated ONCE (gateway.CatalogOK), so doctor's pass/fail
	// arithmetic and the launch degrade condition cannot drift apart; the
	// policy difference is the annotation, not separate predicate code.
	if !gateway.CatalogOK(ids, err) {
		return Check{Name: nameModelsRow, Pass: true, Info: "gateway 200, 0 models served — model-less launches degrade to the placeholder"}
	}
	// The names ride the row: the fetch already parsed them, and the
	// operator's "what can I use" question is answered at preflight time.
	// The canonical full listing stays `kyacode models`. Long lists are
	// capped to keep the row one-line.
	names := ""
	if len(ids) <= 8 {
		names = ": " + strings.Join(ids, ", ")
	} else {
		names = fmt.Sprintf(": %s, …(+%d more — `kyacode models`)", strings.Join(ids[:8], ", "), len(ids)-8)
	}
	return Check{Name: nameModelsRow, Pass: true, Info: fmt.Sprintf("gateway 200, %d models%s", len(ids), names)}
}

// wireProbes is the declared probe battery. It lives at package level so
// the SECOND-INSTANCE review could pin the exact (name, method, route,
// protocol, body) tuples directly against raw literals
// (doctor_direct_test.go's frozen-table test): the run-through-Run fakes
// could otherwise drift TOGETHER with a mis-tabled probe — a copied
// routeSuffix or a probeModel shrunk to a substring stayed green across
// the whole suite in the 2026-09-16 audit, and a table frozen against
// literals is the only surface that reds that (checked-not-derived, the
// gatewaytest doctrine's twin).
var wireProbes = []probeRow{
	{
		name:        nameMessagesPlain,
		proto:       protoAnthropic,
		method:      http.MethodPost,
		routeSuffix: "/messages",
		body:        probeBody,
		extra: map[string]string{
			"anthropic-version": "2023-06-01",
		},
	},
	{
		name:        "openai /v1/chat/completions",
		proto:       protoOpenAI,
		method:      http.MethodPost,
		routeSuffix: "/chat/completions",
		body:        probeBody,
	},
	{
		name:        "responses /v1/responses",
		proto:       protoOpenAI,
		method:      http.MethodPost,
		routeSuffix: "/responses",
		body:        responsesProbeBody,
	},
	{
		// The beta probe sends the SAME route and body as the plain
		// messages probe plus one fixed sentinel anthropic-beta value.
		// The value's validity is NOT guaranteed across gateways (the
		// official API may 400 an invalid name — which is correct
		// behavior), which is exactly why the row's verdict is the
		// relative comparison (reconcileBeta), never a standalone hard
		// bar.
		name:        nameBetaTolerance,
		proto:       protoAnthropic,
		method:      http.MethodPost,
		routeSuffix: "/messages",
		body:        probeBody,
		extra: map[string]string{
			"anthropic-version": "2023-06-01",
			"anthropic-beta":    "max-tokens-3-14-24",
		},
	},
}

// Run executes all gateway probes against gwBase (no /v1 suffix) in
// parallel, authKey = the stored gateway key. The config-token boundary
// stays in cmd (the ticket's confirmed-correct split): (base, key)
// become the gateway module's Base exactly here.
func Run(gwBase, authKey string) Result {
	b := gateway.Base{URL: gwBase, Token: authKey}

	probes := wireProbes

	var wg sync.WaitGroup
	checks := make([]Check, len(probes))
	codes := make([]int, len(probes))
	bodies := make([]string, len(probes))
	for i, p := range probes {
		wg.Add(1)
		go func(i int, p probeRow) {
			defer wg.Done()
			pr := b.Probe(p.method, p.routeSuffix, p.body, p.extra)
			codes[i] = pr.Status
			bodies[i] = pr.Body
			checks[i] = verdict(p, pr)
		}(i, p)
	}

	// The models row is the LAUNCH PATH'S OWN FETCH — not a probe copy:
	// same keyed request, same parse, same error taxonomy, one sentence.
	var modelIDs []string
	var modelErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		modelIDs, modelErr = b.Models()
	}()

	wg.Wait()

	if !reconcileBeta(probes, checks, codes, bodies) {
		return Result{AllPassed: false, Checks: []Check{{Name: "doctor", Pass: false, Info: "internal: doctor probe list lost the plain/beta messages pair"}}}
	}
	checks = append(checks, modelCheck(modelIDs, modelErr))

	res := Result{AllPassed: true}
	for _, c := range checks {
		if c.Warn {
			res.Warned = true
		}
		res.Checks = append(res.Checks, c)
		if !c.Pass {
			res.AllPassed = false
		}
	}
	return res
}

// reconcileBeta applies the RELATIVE beta-header verdict, judged against
// the plain messages probe — extracted from Run by the 2026-09-16 second
// instance so the whole policy (name resolution, the pair-lost guard, the
// rescue, both warn shapes) is pinnable by direct table tests instead of
// living inline behind a five-goroutine fan-out it can never reach.
//
// Beta-header tolerance answers "does the anthropic-beta header change
// the gateway's answer?". Equal answers mean the header changed nothing —
// the beta row adopts the plain row's verdict. That adoption is
// EVIDENCE-scoped: same status as a plain row that carried probe-miss
// evidence admits exactly two beta-row reds — an unconfirmed echo (the
// same not_found_error, wording variance) adopts the plain verdict, while
// a body with NO probe-miss evidence at all means the header changed the
// answer's KIND (a different error shape entirely) and is WARNED, never
// silently matched. A different status against a HEALTHY plain probe
// (plain 200) is the same warn. Warn is structural: Pass=true travels
// with it, so the row, the aggregate, and the CLI verdict cannot disagree
// with the "does not block" sentence — the launch wiring sends no beta
// header, and the official API's own 400 for an invalid beta name is
// correct behavior. When the plain probe itself failed (a 429 rate-limit
// burst, say) the mismatch cannot be blamed on the header: each row keeps
// its own diagnosis.
//
// The pair is resolved by NAME, not slice position, so a probe reorder
// fails loudly (returns false, and Run renders the internal-consistency
// red) instead of silently comparing the wrong two rows. It mutates the
// beta row of checks in place.
func reconcileBeta(probes []probeRow, checks []Check, codes []int, bodies []string) bool {
	iPlain, iBeta := -1, -1
	for i, c := range checks {
		switch c.Name {
		case nameMessagesPlain:
			iPlain = i
		case nameBetaTolerance:
			iBeta = i
		}
	}
	if iPlain < 0 || iBeta < 0 {
		return false
	}
	const warnTail = "possible beta-header rejection — does not block: kyacode's launch wiring sends no beta header"
	if codes[iPlain] != codes[iBeta] {
		if codes[iPlain] == http.StatusOK {
			checks[iBeta].Pass = true
			checks[iBeta].Warn = true
			checks[iBeta].Info = fmt.Sprintf("beta probe %d: %s while plain messages probe 200 (%s)", codes[iBeta], gateway.CutBody(bodies[iBeta]), warnTail)
		}
		// else: the plain probe itself failed — the mismatch cannot be
		// blamed on the header; each row keeps its own diagnosis.
	} else if checks[iPlain].Pass {
		if !checks[iBeta].Pass {
			// Reachable only at 404 (200/400 pass unconditionally): the
			// plain row classified its body as a confirmed probe-miss, so
			// the beta row is red either for losing the echo (wording
			// variance — adopt the plain verdict) or for answering with a
			// body that carries no probe-miss evidence at all (the header
			// changed the answer's kind — warn).
			evidence, _, _ := probeMiss(probes[iBeta].proto, bodies[iBeta])
			if evidence {
				// RE-WRITTEN, not appended: the row's own unconfirmed
				// sentence would read against the adoption ("unconfirmed
				// … matches the plain probe") — the rescued row states
				// what it now is, over its own status+body evidence.
				checks[iBeta].Pass = true
				checks[iBeta].Info = fmt.Sprintf("%d: %s — matches the plain probe (wording variance; beta feature support not proven)", codes[iBeta], gateway.CutBody(bodies[iBeta]))
			} else {
				checks[iBeta].Pass = true
				checks[iBeta].Warn = true
				checks[iBeta].Info = fmt.Sprintf("beta probe %d: %s carries no probe-miss evidence while the plain probe's did (%s)", codes[iBeta], gateway.CutBody(bodies[iBeta]), warnTail)
			}
		} else {
			checks[iBeta].Info += " — matches the plain probe (beta feature support not proven)"
		}
	}
	return true
}

// Render prints the checks to w (the caller's report channel — the
// command bodies report through writer parameters, not globals) and
// returns the overall pass state.
func Render(w io.Writer, res Result) bool {
	for _, c := range res.Checks {
		mark := "✔"
		if c.Warn {
			mark = "⚠"
		} else if !c.Pass {
			mark = "✘"
		}
		fmt.Fprintf(w, "  %s %s  %s\n", mark, c.Name, c.Info)
	}
	return res.AllPassed
}
