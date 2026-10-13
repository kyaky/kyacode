// Package gateway owns everything keyed about speaking to the KYA
// gateway: the /v1 base grammar (the join AND the canonical strip — both
// halves cross-pinned together), the keyed request shape, the
// response-body safety, the /v1/models catalog fetch, and the probe
// surface doctor preflights with. One module, so the preflight verdict
// and the launch path cannot disagree again (ticket 05): a gateway that
// authenticates only via x-api-key, a malformed body, or a redirect
// corrupts doctor and a launch identically.
//
// Redirects are refused outright and typed: Go strips the Authorization
// header only on cross-host hops while re-sending other headers and
// re-sends everything on same-host port/scheme changes, so a 3xx hop is
// itself a key-exfiltration channel. The refusal comes back as a typed
// ErrRefusedRedirect and the hop target receives zero requests.
//
// (base, token) reaches this module from exactly two construction sites:
// the launch path composes it through harness.Endpoint.Base() (the
// declared endpoint), doctor composes it from its scalar boundary inside
// Run. New callers must go through Base.Models/Base.Probe — never build
// their own keyed http.Client; the module owns the one timeout, the two
// read budgets (the whole-catalog budget for the list fetch, the bounded
// budget for probes), and the one header shape.
package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The module's owned transport constants. There is no parameterized
// constructor — 10s-vs-15s and 1MiB-vs-4KiB drift is what this ownership
// exists to prevent (doctor's preflight budget becomes the launch fetch's
// budget: one probe, run in parallel with the rest).
//
// The two read budgets are deliberately different and deliberately at
// this one site: the MODELS fetch must read a whole catalog (1 MiB caps a
// hostile or bloated list), while a PROBE never needs EOF — its verdict
// is status + a bounded body, and a bounded read returns the moment the
// budget is met, so a misbehaving gateway that streams/drips/holds its
// 200 body open cannot turn a preflight row into a timeout failure. The
// probe budget is large enough that a real per-protocol error body (long
// messages included) survives UNTRUNCATED into doctor's verdict — the
// old 256 B cap could cut a JSON error body into an unparseable
// fragment and turn a real probe-miss into a "route absent" verdict.
const (
	// RequestTimeout is the module's owned request timeout — exported so
	// consumers that NAME the budget in prose (the CLI usage text) render
	// the value instead of hand-copying "10s" and drifting with the
	// constant.
	RequestTimeout  = 10 * time.Second
	modelsBodyLimit = 1 << 20
	probeBodyLimit  = 4 << 10 // Cross-seam: doctor's probe-miss verdict (internal/doctor probeMiss) parses THIS body as the protocol's error JSON — a real error body must arrive untruncated. A bounded read still never needs EOF, so a body-streaming gateway cannot stall a probe that already has its verdict evidence.
	// errorBodyCut bounds a body quoted inside an ERROR sentence (the
	// "gateway %d: %s" taxonomy): the sentence is the same one a launch
	// fails with and `kyacode models` prints, so the cut is applied at the
	// source — no caller can render megabytes of an apology page into a
	// terminal or log.
	errorBodyCut = 256
)

// CutBody bounds a body quoted inside an error sentence — the module's
// one cut, one constant (errorBodyCut), shared with the callers that
// quote bodies into their own verdict sentences (doctor's probe rows)
// so the two presentations cannot drift apart.
func CutBody(body string) string {
	if len(body) > errorBodyCut {
		return body[:errorBodyCut] + "…(truncated)"
	}
	return body
}

// client is the one gateway transport. Every keyed kyacode request rides
// it, and it refuses every redirect hop outright: the 3xx response comes
// back to the caller and the hop target receives zero requests, so no
// header — least of all the key — travels.
var client = &http.Client{
	Timeout: RequestTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ErrRefusedRedirect is the typed refusal of a gateway's 3xx hop. The
// refusal explanation is written ONCE here (both the with-Location and
// the bare-redirect form) — callers render the error, never reword it,
// so the preflight and the launch message stay the same sentence.
type ErrRefusedRedirect struct {
	Status   int    // the 3xx status the gateway returned
	Location string // the hop target when the gateway supplied one, else ""
}

func (e *ErrRefusedRedirect) Error() string {
	if e.Location == "" {
		return fmt.Sprintf("gateway %d redirect without Location (refused: the hop would carry the key)", e.Status)
	}
	return fmt.Sprintf("gateway redirected to %s (refused: the hop would carry the key)", e.Location)
}

// V1Base is THE /v1-join rule, implemented once: trailing slashes are
// trimmed before the check, an already-/v1'd base is returned unchanged
// ("gw/v1/" and "gw/v1" both yield "gw/v1"), and a base ending in "/v1/v1"
// stays as-is (the caller composed that URL deliberately). Accessing
// /v1/* on a base that carries its own /v1 must not double the suffix —
// the doubled /v1/v1/... 404 is what a second join implementation would
// reintroduce.
func V1Base(base string) string {
	b := base
	for len(b) > 0 && b[len(b)-1] == '/' {
		b = b[:len(b)-1]
	}
	if len(b) > 3 && b[len(b)-3:] == "/v1" {
		return b
	}
	return b + "/v1"
}

// CanonicalBase is the strip half of the /v1 base grammar — the two
// complementary rules live in THIS module so one file answers "what is a
// gateway base" (the WHY of the canonical no-/v1 form lives on the
// Gateway base definition and V1Base's doc above): the canonical STORED
// form is the base WITHOUT /v1, and users habitually paste the full
// OpenAI-style URL — accepting it here and trimming it prevents the
// doubled /v1/v1/… 404 that surfaces as Claude Code's misleading "model
// may not exist" error.
//
// THE /v1/v1 EDGE IS DELIBERATE and the two halves disagree on it by
// design: the JOIN half (V1Base) keeps "gw/v1/v1" as the caller-chosen
// URL (a second join must never compound), while the STRIP half removes
// EXACTLY ONE trailing /v1, so a hand-edited or env-overridden
// "gw/v1/v1" is stored as "gw/v1" — which claude then extends to
// /v1/v1/messages. That residual path is accepted (pinned in the
// config round-trip test); the grammar's two halves are cross-pinned
// together in this package's tests so any change re-evaluates both.
func CanonicalBase(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") && len(b) > 3 {
		return b[:len(b)-3]
	}
	return b
}

// Base is the resolved gateway destination: the two declared facts every
// keyed call carries. A plain composite literal — no constructor, no
// options (the keying is not configurable; drift lives in the call sites
// this module exists to unify).
type Base struct {
	URL   string // the stored gateway base WITHOUT /v1; the /v1 suffix is joined here, once
	Token string // the stored key, composed as Bearer by the one header shape
}

// Models fetches GET {V1Base}/models with the single keying shape and
// returns the sorted non-empty model ids. The error taxonomy is the
// contract the launch path degrades on and the doctor models row reports:
// transport failure wraps "gateway unreachable", a 3xx hop is a typed
// *ErrRefusedRedirect, a non-200 is "gateway %d: %s" with the body, and a
// 200 with an unparsable OpenAI list shape is "parse /v1/models: %w". An
// empty list is (empty, nil) — caller policy (grilled for ticket 05:
// doctor annotates it green; launches degrade to the placeholder row).
func (b Base) Models() ([]string, error) {
	status, raw, err := b.exchange(http.MethodGet, V1Base(b.URL)+"/models", "", nil, modelsBodyLimit)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		// The body is quoted inside the sentence a launch fails with (and
		// the doctor models row reports verbatim) — bounded at the source.
		return nil, fmt.Errorf("gateway %d: %s", status, CutBody(string(raw)))
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse /v1/models: %w", err)
	}
	out := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Probed is one executed exchange in the module's own vocabulary,
// unjudged — verdict policy stays with the caller. Status 0 with a non-nil
// Err means no exchange completed (transport failure or a guard
// rejection); a 3xx completes the exchange with ErrRefusedRedirect as
// Err and the 3xx as Status.
type Probed struct {
	Status int
	Body   string
	Err    error
}

// Probe executes one keyed exchange against a route suffix joined ONCE by
// the module's /v1 rule (doctor composes no URLs). The header shape is
// singular: Authorization Bearer, Content-Type application/json iff a
// body rides, then the caller's extras. An extra naming Authorization,
// X-Api-Key, or Content-Type is refused by the module — a second keyed
// channel composed from the extras table is exactly the
// preflight-vs-launch divergence ticket 05 deletes, so it can never be
// requested here (the guard lives in exchange, the one executor, so it
// inherits every path that carries extras) — and comes back as a Probed
// with Err set and no request sent. The read is bounded by
// probeBodyLimit: a probe never needs EOF, so a gateway that holds/body-
// streams its response cannot time out a probe that already has its
// verdict evidence.
func (b Base) Probe(method, routeSuffix, body string, extra map[string]string) Probed {
	status, raw, err := b.exchange(method, V1Base(b.URL)+routeSuffix, body, extra, probeBodyLimit)
	return Probed{Status: status, Body: strings.TrimSpace(string(raw)), Err: err}
}

// exchange is the one executor Models and Probe share: one header shape,
// one body budget, one 3xx→ErrRefusedRedirect site, and the anti-rekey
// guard (method-neutral wording here — the promise "an extra can never
// compose a second keyed channel" holds for every future caller, not
// just Probe's door). Returns the body bytes raw; the few that need to
// ride a sentence are stringified once by the caller.
func (b Base) exchange(method, fullURL, reqBody string, extra map[string]string, limit int64) (int, []byte, error) {
	for k := range extra {
		switch strings.ToLower(k) {
		case "authorization", "x-api-key", "content-type":
			return 0, nil, fmt.Errorf("extra header %q is refused: the module owns the keyed header shape", k)
		}
	}
	var rd io.Reader
	if reqBody != "" {
		rd = strings.NewReader(reqBody)
	}
	req, err := http.NewRequest(method, fullURL, rd)
	if err != nil {
		return 0, nil, err
	}
	// The one keyed header shape: Bearer only. An x-api-key gateway (the
	// divergence the ticket opened with) is the gateway's problem to
	// declare — a second keyed channel composed here is what made doctor
	// pass while launches 403'd.
	req.Header.Set("Authorization", "Bearer "+b.Token)
	if reqBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("gateway unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("gateway %d body: %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		return resp.StatusCode, raw, &ErrRefusedRedirect{Status: resp.StatusCode, Location: loc}
	}
	return resp.StatusCode, raw, nil
}

// CatalogOK is THE success bar for a fetched catalog: a real list (no
// error, at least one usable id). It is the exact predicate the launch
// path's degrade branch is written against — fetchCatalog degrades
// precisely when this is false — so callers composing a pass/fail verdict
// share the bar instead of re-deriving two slightly different ones.
// Models() never returns empty-id rows, but the bar judges usable ids, so
// a caller handing it a row of empty strings still gets "not a catalog".
func CatalogOK(ids []string, err error) bool {
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id != "" {
			return true
		}
	}
	return false
}
