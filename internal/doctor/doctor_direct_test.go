package doctor

// ---- direct policy pins (2026-09-16 second instance) -------------------
// The Run-through-httptest fakes pin what the fan-out can reach. These
// tables pin the policy AT its own interface: the fail-closed branch the
// probe table structurally hides, the exact sentence forms (equality, not
// Contains — the Contains-only pins were an audit finding), and the
// frozen probe battery the fakes could otherwise drift WITH.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"kyacode/internal/gateway"
	"kyacode/internal/gatewaytest"
)

func TestProbeMissClassifiesEveryProtocolShape(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		proto                     string
		body                      string
		wantEvidence, wantConfirm bool
		wantPhrase                string
	}{
		{"anthropic confirmed miss", protoAnthropic, `{"error":{"message":"model __doctor_probe__ does not exist","type":"not_found_error"},"type":"error"}`, true, true, "the gateway named the probe model in its not_found_error"},
		{"anthropic unconfirmed (path-404 shape)", protoAnthropic, `{"type":"error","error":{"type":"not_found_error","message":"The requested resource could not be found."}}`, true, false, ""},
		{"anthropic request_id + reordered keys", protoAnthropic, `{"request_id":"req_x","type":"error","error":{"message":"model __doctor_probe__ unavailable","type":"not_found_error"}}`, true, true, "the gateway named the probe model in its not_found_error"},
		{"anthropic carrying openai code (shape-translating proxy)", protoAnthropic, `{"error":{"code":"model_not_found"}}`, true, true, "error.code model_not_found"},
		{"anthropic other error type at 404", protoAnthropic, `{"type":"error","error":{"type":"invalid_request_error","message":"nope"}}`, false, false, ""},
		{"anthropic truncated JSON, quoted token + echo", protoAnthropic, `{"error":{"type":"not_found_error","message":"model __doctor_probe__ gone`, true, true, "the gateway named the probe model in its not_found_error"},
		{"openai confirmed", protoOpenAI, `{"error":{"code":"model_not_found","message":"model __doctor_probe__ does not exist","param":"model","type":"invalid_request_error"}}`, true, true, "error.code model_not_found"},
		{"openai codeless — message prose is never evidence", protoOpenAI, `{"error":{"message":"model __doctor_probe__ does not exist","type":"invalid_request_error"}}`, false, false, ""},
		{"openai truncated JSON but quoted code token", protoOpenAI, `{"error":{"code":"model_not_found"`, true, true, "error.code model_not_found"},
		{"html page at 404", protoAnthropic, `<html><body>404 Not Found</body></html>`, false, false, ""},
		{"unknown proto fails CLOSED (no silent openai classification)", "anthropic-chat", `{"error":{"code":"model_not_found"}}`, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, conf, phrase := probeMiss(tc.proto, tc.body)
			if ev != tc.wantEvidence || conf != tc.wantConfirm {
				t.Errorf("probeMiss(%q, ...) = (%v,%v), want (%v,%v)", tc.proto, ev, conf, tc.wantEvidence, tc.wantConfirm)
			}
			if conf && phrase != tc.wantPhrase {
				t.Errorf("%s: confirmed phrase = %q, want %q", tc.name, phrase, tc.wantPhrase)
			}
		})
	}
}

func TestVerdictLadderPinsFullSentences(t *testing.T) {
	p := probeRow{name: nameMessagesPlain, proto: protoAnthropic}
	cases := []struct {
		name     string
		pr       gateway.Probed
		wantPass bool
		wantInfo string
	}{
		{"200", gateway.Probed{Status: 200, Body: `{"ok":true}`}, true, "200 (11 bytes)"},
		{"400 route exists", gateway.Probed{Status: 400, Body: `{"type":"error","error":{"type":"invalid_request_error","message":"shape"}}`}, true, `400: {"type":"error","error":{"type":"invalid_request_error","message":"shape"}}`},
		{"404 expected probe-miss", gateway.Probed{Status: 404, Body: gatewaytest.AnthropicProbeMiss}, true, "404 — probe miss confirmed (the gateway named the probe model in its not_found_error)"},
		{"404 unconfirmed", gateway.Probed{Status: 404, Body: `{"type":"error","error":{"type":"not_found_error","message":"resource not found"}}`}, false, `404: {"type":"error","error":{"type":"not_found_error","message":"resource not found"}} — probe-model miss unconfirmed: the 404 names no probe model`},
		{"401 auth never passes", gateway.Probed{Status: 401, Body: `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`}, false, `401: {"type":"error","error":{"type":"authentication_error","message":"bad key"}}`},
		{"429 upstream class fails", gateway.Probed{Status: 429, Body: `{}`}, false, "429: {}"},
		{"500 server error fails its own fall-through", gateway.Probed{Status: 500, Body: `{"error":{"message":"boom","type":"api_error"},"type":"error"}`}, false, `500: {"error":{"message":"boom","type":"api_error"},"type":"error"}`},
		{"503 server error fails its own fall-through", gateway.Probed{Status: 503, Body: `<html>unavailable</html>`}, false, "503: <html>unavailable</html>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := verdict(p, tc.pr)
			if c.Name != p.name {
				t.Errorf("row name = %q, want the probe row's", c.Name)
			}
			if c.Pass != tc.wantPass || c.Info != tc.wantInfo {
				t.Errorf("verdict = (pass %v, info %q), want (pass %v, info %q)", c.Pass, c.Info, tc.wantPass, tc.wantInfo)
			}
			if c.Warn {
				t.Errorf("verdict never warns — warn is reconcileBeta's alone: %+v", c)
			}
		})
	}
	// Transport failures keep the module's own sentence, unmasked.
	c := verdict(p, gateway.Probed{Err: errors.New("gateway unreachable: dial boom")})
	if c.Pass || c.Info != "gateway unreachable: dial boom" {
		t.Errorf("transport verdict = %+v, want the module sentence verbatim", c)
	}
}

func TestReconcileBetaPolicyDirect(t *testing.T) {
	miss := `{"error":{"message":"model __doctor_probe__ does not exist","type":"not_found_error"},"type":"error"}`
	noEcho := `{"type":"error","error":{"type":"not_found_error","message":"The requested resource could not be found."}}`
	html := `<html>502-ish gateway error page</html>`
	pPlain, pBeta := wireProbes[0], wireProbes[3]

	// pair hands over the two rows exactly as Run's fan-out does.
	pair := func(t *testing.T, plain, beta gateway.Probed) ([]probeRow, []Check, []int, []string) {
		t.Helper()
		return []probeRow{pPlain, pBeta},
			[]Check{verdict(pPlain, plain), verdict(pBeta, beta)},
			[]int{plain.Status, beta.Status},
			[]string{plain.Body, beta.Body}
	}

	t.Run("equal confirmed miss annotates the scope honestly", func(t *testing.T) {
		probes, checks, codes, bodies := pair(t, gateway.Probed{Status: 404, Body: miss}, gateway.Probed{Status: 404, Body: miss})
		if !reconcileBeta(probes, checks, codes, bodies) {
			t.Fatal("pair lost on the declared table")
		}
		want := "404 — probe miss confirmed (the gateway named the probe model in its not_found_error) — matches the plain probe (beta feature support not proven)"
		if b := checks[1]; !b.Pass || b.Warn || b.Info != want {
			t.Errorf("beta row = %+v, want pass + the exact sentence %q", b, want)
		}
	})

	t.Run("wording variance rescues to the plain verdict", func(t *testing.T) {
		probes, checks, codes, bodies := pair(t, gateway.Probed{Status: 404, Body: miss}, gateway.Probed{Status: 404, Body: noEcho})
		if !reconcileBeta(probes, checks, codes, bodies) {
			t.Fatal("pair lost on the declared table")
		}
		want := "404: " + noEcho + " — matches the plain probe (wording variance; beta feature support not proven)"
		if b := checks[1]; !b.Pass || b.Warn || b.Info != want {
			t.Errorf("rescued beta row = %+v, want pass (never warn) + the re-written sentence %q", b, want)
		}
	})

	t.Run("body-shape change warns and does not block", func(t *testing.T) {
		probes, checks, codes, bodies := pair(t, gateway.Probed{Status: 404, Body: miss}, gateway.Probed{Status: 404, Body: html})
		if !reconcileBeta(probes, checks, codes, bodies) {
			t.Fatal("pair lost on the declared table")
		}
		want := "beta probe 404: " + html + " carries no probe-miss evidence while the plain probe's did (possible beta-header rejection — does not block: kyacode's launch wiring sends no beta header)"
		if b := checks[1]; !b.Pass || !b.Warn || b.Info != want {
			t.Errorf("shape-change beta row = %+v, want pass+warn + %q", b, want)
		}
	})

	t.Run("mismatch against a healthy plain probe warns", func(t *testing.T) {
		betaBody := `{"type":"error","error":{"type":"authentication_error","message":"only the beta route is refused"}}`
		probes, checks, codes, bodies := pair(t, gateway.Probed{Status: 200, Body: `{"ok":true}`}, gateway.Probed{Status: 401, Body: betaBody})
		if !reconcileBeta(probes, checks, codes, bodies) {
			t.Fatal("pair lost on the declared table")
		}
		want := "beta probe 401: " + betaBody + " while plain messages probe 200 (possible beta-header rejection — does not block: kyacode's launch wiring sends no beta header)"
		if b := checks[1]; !b.Pass || !b.Warn || b.Info != want {
			t.Errorf("beta row against a healthy plain = %+v, want the non-blocking warn — the warn sentence and Pass must AGREE", b)
		}
	})

	t.Run("mismatch with a failed plain probe keeps both own diagnoses", func(t *testing.T) {
		probes, checks, codes, bodies := pair(t, gateway.Probed{Status: 429, Body: `{}`}, gateway.Probed{Status: 200, Body: `{"ok":true}`})
		if !reconcileBeta(probes, checks, codes, bodies) {
			t.Fatal("pair lost on the declared table")
		}
		if p := checks[0]; p.Pass {
			t.Errorf("plain 429 must keep its own red: %+v", p)
		}
		if b := checks[1]; !b.Pass || b.Warn || b.Info != "200 (11 bytes)" {
			t.Errorf("an accepted beta during a plain rate-limit must keep its own green, untouched: %+v", b)
		}
	})

	t.Run("pair resolved by name; a lost pair fails loudly", func(t *testing.T) {
		rogue := []probeRow{{name: "x"}, {name: "y"}}
		checks := []Check{{Name: "x", Pass: true}, {Name: "y", Pass: true}}
		if reconcileBeta(rogue, checks, []int{200, 200}, []string{"{}", "{}"}) {
			t.Fatal("reconcile accepted a table without the plain/beta pair — the loud-red guard is gone")
		}
	})
}

func TestWireProbesTableIsTheDeclaredFour(t *testing.T) {
	// The want-table is RAW LITERALS, not the package's own constants:
	// comparing row fields against the very identifiers the rows use
	// (nameMessagesPlain, protoAnthropic, probeBody…) would be
	// const-vs-const — a pin that can never red. Checked, never derived.
	want := []probeRow{
		{name: "anthropic /v1/messages", method: http.MethodPost, routeSuffix: "/messages", proto: "anthropic", body: `{"model":"__doctor_probe__","max_tokens":8,"messages":[{"role":"user","content":"ping"}]}`},
		{name: "openai /v1/chat/completions", method: http.MethodPost, routeSuffix: "/chat/completions", proto: "openai", body: `{"model":"__doctor_probe__","max_tokens":8,"messages":[{"role":"user","content":"ping"}]}`},
		{name: "responses /v1/responses", method: http.MethodPost, routeSuffix: "/responses", proto: "openai", body: `{"model":"__doctor_probe__","input":"ping","max_output_tokens":8}`},
		{name: "beta header tolerance", method: http.MethodPost, routeSuffix: "/messages", proto: "anthropic", body: `{"model":"__doctor_probe__","max_tokens":8,"messages":[{"role":"user","content":"ping"}]}`},
	}
	if len(wireProbes) != len(want) {
		t.Fatalf("probe battery changed size: %d rows", len(wireProbes))
	}
	for i, w := range want {
		g := wireProbes[i]
		if g.name != w.name || g.method != w.method || g.routeSuffix != w.routeSuffix || g.proto != w.proto || g.body != w.body {
			t.Errorf("probe row %d drifted: got (%q %s %s proto %q), want (%q %s %s proto %q)",
				i, g.name, g.method, g.routeSuffix, g.proto, w.name, w.method, w.routeSuffix, w.proto)
		}
	}
	// The negative probe identity is FROZEN to the literal the shared
	// fakes and the echo classifier key on: a routeSuffix copied between
	// rows and a probeModel shrunk to a substring both stayed green
	// through the whole Run-level fleet in the 2026-09-16 audit — this
	// table is the surface that reds them (checked, never derived).
	if probeModel != "__doctor_probe__" {
		t.Errorf("probeModel = %q, want the frozen negative-probe id", probeModel)
	}
	for _, body := range []string{probeBody, responsesProbeBody} {
		if !strings.Contains(body, `"model":"`+probeModel+`"`) {
			t.Errorf("probe body %q must carry the model field naming the frozen probeModel verbatim", body)
		}
	}
	if wireProbes[0].extra["anthropic-version"] != "2023-06-01" || wireProbes[3].extra["anthropic-version"] != "2023-06-01" {
		t.Error("the messages pair must send anthropic-version 2023-06-01 (the beta row is the SAME request plus one header)")
	}
	if wireProbes[3].extra["anthropic-beta"] != "max-tokens-3-14-24" {
		t.Error("the beta probe sentinel value changed — its validity is NOT guaranteed across gateways; that is why the row is relative and warn-level")
	}
	if len(wireProbes[1].extra) != 0 || len(wireProbes[2].extra) != 0 {
		t.Error("the openai routes must carry no anthropic headers")
	}
}

// The classified display's per-branch phrases, pinned at the verdict
// ladder: the anthropic not_found_error+echo branch renders the echo
// phrase, and the shape-translating-proxy case (an anthropic-protocol
// 404 carrying an OpenAI-shaped body) renders the CODE phrase — the
// display must never claim evidence the body does not carry.
func TestVerdictLadderPinsTheConfirmationKinds(t *testing.T) {
	cases := []struct {
		name     string
		pr       gateway.Probed
		wantInfo string
	}{
		{"anthropic not_found_error + echo", gateway.Probed{Status: 404, Body: gatewaytest.AnthropicProbeMiss}, "404 — probe miss confirmed (the gateway named the probe model in its not_found_error)"},
		{"anthropic carrying openai code (shape-translating proxy)", gateway.Probed{Status: 404, Body: `{"error":{"code":"model_not_found"}}`}, "404 — probe miss confirmed (error.code model_not_found)"},
		{"openai code", gateway.Probed{Status: 404, Body: `{"error":{"code":"model_not_found","message":"gone","param":"model","type":"invalid_request_error"}}`}, "404 — probe miss confirmed (error.code model_not_found)"},
	}
	p := probeRow{name: nameMessagesPlain, proto: protoAnthropic}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := verdict(p, tc.pr)
			if !check.Pass || check.Info != tc.wantInfo {
				t.Errorf("verdict Info = %q (pass %v), want %q", check.Info, check.Pass, tc.wantInfo)
			}
		})
	}
}

// The models row's names form, pinned at the cap boundaries: exactly 8
// names join without the overflow marker, 9 names trigger the
// (+N more — kyacode models) pointer, and a single model renders bare.
func TestModelCheckPinsTheNamesForm(t *testing.T) {
	ids := make([]string, 0, 9)
	for i := 1; i <= 9; i++ {
		ids = append(ids, fmt.Sprintf("m%02d", i))
	}
	eight := modelCheck(ids[:8], nil)
	if got, want := eight.Info, "gateway 200, 8 models: m01, m02, m03, m04, m05, m06, m07, m08"; got != want {
		t.Errorf("8-model Info = %q, want %q (no overflow marker at the boundary)", got, want)
	}
	nine := modelCheck(ids, nil)
	if got, want := nine.Info, "gateway 200, 9 models: m01, m02, m03, m04, m05, m06, m07, m08, …(+1 more — `kyacode models`)"; got != want {
		t.Errorf("9-model Info = %q, want the capped overflow form %q", got, want)
	}
	one := modelCheck([]string{"m01"}, nil)
	if got, want := one.Info, "gateway 200, 1 models: m01"; got != want {
		t.Errorf("1-model Info = %q, want %q", got, want)
	}
}
