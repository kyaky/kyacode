package doctor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kyacode/internal/gateway"
	"kyacode/internal/gatewaytest"
)

func TestDoctorAllPassOnPerfectGateway(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	})
	res := Run(s.URL, "sk-test-12345")
	if !res.AllPassed {
		t.Errorf("expected all pass, got: %+v", res.Checks)
	}
}

// 401 from the gateway must be a hard fail on EVERY probe (H-5).
func TestDoctorRejects401(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	})
	res := Run(s.URL, "sk-expired")
	if res.AllPassed {
		t.Errorf("401 gateway must fail pre-flight; got pass on: %+v", res.Checks)
	}
	for _, c := range res.Checks {
		if c.Pass {
			t.Errorf("%s: 401 must not count as a pass", c.Name)
		}
	}
}

// Unreachable gateway fails everything with "unreachable" info.
func TestDoctorUnreachable(t *testing.T) {
	res := Run("http://127.0.0.1:1", "sk-x")
	if res.AllPassed {
		t.Error("unreachable gateway must fail pre-flight")
	}
	if !strings.Contains(res.Checks[0].Info, "unreachable") &&
		!strings.Contains(res.Checks[0].Info, "refused") {
		t.Errorf("info should diagnose unreachability: %s", res.Checks[0].Info)
	}
}

// A redirecting endpoint is a key-exfiltration channel (Go re-sends some
// keyed headers on hops), so doctor must refuse the hop: probes fail and
// the info names the redirect target, while the target itself records
// zero requests.
func TestDoctorRefusesARedirectingGateway(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(target.Close)

	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	})
	res := Run(s.URL, "sk-test-12345")
	if res.AllPassed {
		t.Errorf("redirecting gateway must fail pre-flight; got pass on: %+v", res.Checks)
	}
	if targetHits != 0 {
		t.Errorf("redirect target received %d requests with the key attached; must record zero", targetHits)
	}
	for _, c := range res.Checks {
		if !strings.Contains(c.Info, "redirect") {
			t.Errorf("%s: info should name the refused redirect, got %q", c.Name, c.Info)
		}
	}
}

// A bare 404 — no model_not_found body — means the route does not exist
// on this gateway. "Route exists" evidence must come from the body.
func TestDoctorFailsABare404(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`404 page not found`))
	})
	res := Run(s.URL, "sk-x")
	if res.AllPassed {
		t.Errorf("bare 404 must fail (route absent); got pass on: %+v", res.Checks)
	}
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "list") {
			continue
		}
		if c.Pass {
			t.Errorf("%s: bare 404 must not pass", c.Name)
		}
	}
}

// 404 with the model_not_found body IS the route-exists proof: the
// gateway parsed the request and looked up the probe model. Keep
// passing (the old contract, scoped to real evidence).
func TestDoctorPasses404WithModelNotFoundBody(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":"model_not_found"}}`))
	})
	res := Run(s.URL, "sk-x")
	if !res.AllPassed {
		t.Errorf("404+model_not_found should pass; got %+v", res.Checks)
	}
}

// GET /v1/models has no "model not found" semantics — the catalog is
// either listable (200) or the doctor row must fail.
func TestDoctorModelListingRequires200(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"model_not_found"}}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	res := Run(s.URL, "sk-x")
	if res.AllPassed {
		t.Errorf("404 on /v1/models must fail the models row; got %+v", res.Checks)
	}
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "list") && c.Pass {
			t.Errorf("%s: listing probe with 404 must not pass", c.Name)
		}
	}
}

// 403 stays a hard failure on every probe.
func TestDoctorHardFails403(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	res := Run(s.URL, "sk-x")
	if res.AllPassed {
		t.Errorf("403 must fail pre-flight; got pass on: %+v", res.Checks)
	}
}

// A rejecting gateway answers the beta probe differently from the plain
// messages probe. Tolerant gateways answer both the same. A mismatch
// against a healthy plain probe is a WARNING, not a failure (2026-09-15
// policy): kyacode's launch wiring sends no beta header, and the official
// API itself may 400 an invalid beta name — correct behavior, so a strict
// gateway must not be declared broken — but the row must not pass
// silently either.
func TestDoctorDetectsBetaHeaderRejection(t *testing.T) {
	rejected := false
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.Header.Get("anthropic-beta") != "" {
			rejected = true
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"unsupported beta header"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	res := Run(s.URL, "sk-x")
	if !rejected {
		t.Fatalf("fake gateway was never asked the beta probe")
	}
	if !res.AllPassed {
		t.Errorf("a beta-header mismatch must not BLOCK pre-flight (launch wiring sends no beta header); got %+v", res.Checks)
	}
	if !res.Warned {
		t.Errorf("a beta-header mismatch must surface as a warning: %+v", res.Checks)
	}
	row, ok := rowNamed(t, res, nameBetaTolerance)
	if !ok {
		t.Fatal("the beta row vanished from the result")
	}
	if !row.Warn || !row.Pass {
		t.Errorf("beta row must be warn-and-pass on a mismatch: %+v", row)
	}
	if !strings.Contains(row.Info, "possible beta-header rejection") {
		t.Errorf("beta row info %q must name the possible rejection", row.Info)
	}
}

// The Authorization header shape: Bearer must reach the gateway. The
// handler records behind a mutex — the doctor's probes hit this
// handler more than once, and the race arm's first flare (v4.3.0
// campaign run) proved the bare shared write is a live data race on
// overlapping probe goroutines.
func TestDoctorSendsBearer(t *testing.T) {
	var mu sync.Mutex
	var got string
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Get("Authorization")
		mu.Unlock()
		w.Write([]byte(`{}`))
	})
	Run(s.URL, "sk-test-12345")
	mu.Lock()
	defer mu.Unlock()
	if got != "Bearer sk-test-12345" {
		t.Errorf("Authorization = %q", got)
	}
}

// AC-6 case 1: a gateway authenticating only via x-api-key made the OLD doctor
// pass green (it sent BOTH headers) while every launch (Bearer only)
// 403'd — the exact preflight/launch disagreement ticket 05 exists for.
// Doctor now sends the module's single Bearer shape, so this gateway
// preflights RED, and the models row's info is the message byte-identical
// to what the launch path's fetch fails with.
func TestDoctorRedWhenGatewayServesOnlyXApiKey(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		// Lowercase getter form (Go canonicalizes header names at the
		// wire — only the gateway's requirement matters here).
		if r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"authentication requires x-api-key"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	res := Run(s.URL, "sk-test-12345")
	if res.AllPassed {
		t.Fatalf("an x-api-key-only gateway must preflight red; got pass: %+v", res.Checks)
	}
	_, err := gateway.Base{URL: s.URL, Token: "sk-test-12345"}.Models()
	if err == nil {
		t.Fatal("the launch path must fail the same gateway")
	}
	row, ok := rowNamed(t, res, nameModelsRow)
	if !ok {
		t.Fatal("the models row vanished from the result")
	}
	if row.Pass {
		t.Errorf("models row passed on an x-api-key-only gateway")
	}
	if row.Info != err.Error() {
		t.Errorf("models row info %q must equal the launch-path error %q (AC-6: same message)", row.Info, err.Error())
	}
}

// The reported gateway (2026-09-14 operator report): an
// Anthropic-compatible gateway answers the probe-model miss per the
// ENTRY protocol — the anthropic routes carry Anthropic's documented
// error shape (top-level type:"error", error.type "not_found_error",
// NO OpenAI error.code — platform.claude.com/docs/en/api/errors), while
// the openai routes carry error.code "model_not_found". The probe model
// id is echoed inside both messages. The byte-exact bodies live ONCE in
// gatewaytest (ReportedAnthropicGateway) so this unit seam and the CLI
// seam cannot drift. Per the report all five rows must resolve to a
// green pre-flight: the anthropic row and the beta row are expected
// probe-misses, not failures.
func TestDoctorReportedAnthropicGatewayShapePasses(t *testing.T) {
	s := gatewaytest.ReportedAnthropicGateway(t, "sk-test-12345")
	res := Run(s.URL, "sk-test-12345")
	if !res.AllPassed {
		t.Fatalf("the reported gateway must pass pre-flight; got %+v", res.Checks)
	}
	for _, name := range []string{nameMessagesPlain, nameBetaTolerance} {
		row, ok := rowNamed(t, res, name)
		if !ok {
			t.Fatalf("the %s row vanished", name)
		}
		if !row.Pass {
			t.Errorf("%s: the reported Anthropic probe-miss must not fail the row: %+v", name, row)
		}
	}
}

// The verdict's probe budget must not re-introduce the old truncation
// trap: a real error body LONGER than the old 256-byte cap — its echoed
// probe model sitting past byte 256 — still parses and still passes.
// Under the old budget this exact body truncated mid-JSON and verdicted
// "route absent".
func TestDoctor404VerdictSurvivesALongErrorBody(t *testing.T) {
	long := `{"type":"error","error":{"type":"not_found_error","message":"` + strings.Repeat("x", 270) + ` model __doctor_probe__ does not exist"}}`
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/messages"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(long))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(gatewaytest.OpenAIProbeMiss))
		}
	})
	res := Run(s.URL, "sk-test-12345")
	if !res.AllPassed {
		t.Fatalf("a >256-byte error body must survive into the verdict: %+v", res.Checks)
	}
	row, ok := rowNamed(t, res, nameMessagesPlain)
	if !ok || !row.Pass {
		t.Fatalf("anthropic row must pass on the long body: %+v", row)
	}
}

// The OpenAI evidence bar stays exact: an OpenAI-shaped 404 WITHOUT the
// spec'd error.code "model_not_found" carries no model-lookup claim and
// must fail (the message prose alone was never evidence, old predicate
// included).
func TestDoctorFailsOpenAIShape404WithoutTheCode(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/messages"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(gatewaytest.AnthropicProbeMiss))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"model __doctor_probe__ does not exist","type":"invalid_request_error"}}`))
		}
	})
	res := Run(s.URL, "sk-test-12345")
	if res.AllPassed {
		t.Fatal("an OpenAI-shape 404 without error.code must fail")
	}
	for _, name := range []string{"openai /v1/chat/completions", "responses /v1/responses"} {
		row, ok := rowNamed(t, res, name)
		if !ok {
			t.Fatalf("the %s row vanished", name)
		}
		if row.Pass {
			t.Errorf("%s passed a code-less OpenAI 404: %+v", name, row)
		}
	}
}

// A beta body that changed SHAPE at the same status (HTML where the
// plain probe got a probe-miss error) is evidence the header changed the
// answer's kind — WARNED, never silently matched and never blocking.
func TestDoctorWarnsWhenTheBetaBodyShapeChanges(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case r.URL.Path == "/v1/messages":
			w.WriteHeader(http.StatusNotFound)
			if r.Header.Get("anthropic-beta") != "" {
				w.Write([]byte(`<html><body>502-ish gateway error page</body></html>`))
				return
			}
			w.Write([]byte(gatewaytest.AnthropicProbeMiss))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(gatewaytest.OpenAIProbeMiss))
		}
	})
	res := Run(s.URL, "sk-test-12345")
	if !res.AllPassed {
		t.Fatalf("a beta body-shape change must not BLOCK (launch wiring sends no beta header): %+v", res.Checks)
	}
	row, ok := rowNamed(t, res, nameBetaTolerance)
	if !ok {
		t.Fatal("the beta row vanished")
	}
	if !row.Warn || !row.Pass {
		t.Errorf("beta row must be warn-and-pass on a shape change: %+v", row)
	}
	if !strings.Contains(row.Info, "possible beta-header rejection") || !strings.Contains(row.Info, "does not block") {
		t.Errorf("beta row info %q must name the possible rejection and its non-blocking status", row.Info)
	}
}

// The path-404 half of the taxonomy must stay RED under every shape a
// missing route actually produces: plain text, an HTML error page, and —
// the trap — Anthropic's OWN not_found_error shape without the probe
// model echoed (Anthropic's 404 covers "endpoint path and any resource
// IDs", so shape alone cannot confirm; the row fails with an
// "unconfirmed" annotation instead of passing on a guess). A 404 whose
// body is not JSON at all is likewise a fail.
func TestDoctorFailsEveryPathNotFoundShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantIn  string // required info fragment on the anthropic row
		wantOut string // forbidden info fragment
	}{
		{"plain text", `404 page not found`, "", ""},
		{"html page", "<html><body><h1>404 Not Found</h1></body></html>", "", ""},
		{
			"anthropic not_found_error without probe-model echo",
			`{"type":"error","error":{"type":"not_found_error","message":"The requested resource could not be found."}}`,
			"unconfirmed", "",
		},
		{"invalid json", `{"error": broken`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					w.Write([]byte(`{"data":[{"id":"m1"}]}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(tc.body))
			})
			res := Run(s.URL, "sk-x")
			if res.AllPassed {
				t.Fatalf("path-404 shape %q must fail pre-flight", tc.name)
			}
			row, ok := rowNamed(t, res, nameMessagesPlain)
			if !ok {
				t.Fatal("the anthropic row vanished")
			}
			if row.Pass {
				t.Errorf("anthropic row passed a path-404: %+v", row)
			}
			if tc.wantIn != "" && !strings.Contains(row.Info, tc.wantIn) {
				t.Errorf("anthropic row info %q must carry %q", row.Info, tc.wantIn)
			}
			if tc.wantOut != "" && strings.Contains(row.Info, tc.wantOut) {
				t.Errorf("anthropic row info %q must not carry %q", row.Info, tc.wantOut)
			}
		})
	}
}

// The verdict is order- and request_id-independent: the SAME reported
// anthropic miss with reordered keys and a request_id added still passes,
// and the echoed probe model may sit anywhere in the body (message,
// param, wherever) — the echo is data, never prose or position.
func TestDoctorVerdictIgnoresFieldOrderAndRequestID(t *testing.T) {
	reordered := `{"request_id":"req_011CSHoEeqs5C35K2UUqR7Fy","type":"error","error":{"type":"not_found_error","message":"model __doctor_probe__ does not exist or is not available"}}`
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/messages"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(reordered))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"model_not_found","param":"model","type":"invalid_request_error"}}`))
		}
	})
	res := Run(s.URL, "sk-x")
	if !res.AllPassed {
		t.Fatalf("reordered keys + request_id must not change the verdict: %+v", res.Checks)
	}
	row, ok := rowNamed(t, res, nameMessagesPlain)
	if !ok || !row.Pass {
		t.Fatalf("anthropic row must pass: %+v", row)
	}
	if !strings.Contains(row.Info, "probe miss confirmed") {
		t.Errorf("a confirmed probe-miss must be annotated as such: %+v", row)
	}
}

// 429 and 5xx are never passes on any wire row — transient and server
// failures keep their identity (the models row and the launches fail with
// the same taxonomy).
func TestDoctorFails429And5xxOnWireRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"429", http.StatusTooManyRequests},
		{"500", http.StatusInternalServerError},
		{"502", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":"upstream"}`))
			})
			res := Run(s.URL, "sk-x")
			if res.AllPassed {
				t.Fatalf("%s must fail pre-flight", tc.name)
			}
			for _, c := range res.Checks {
				if c.Pass {
					t.Errorf("%s: %s must not pass", tc.name, c.Name)
				}
			}
		})
	}
}

// The equal-answers rescue: when the beta probe answers with the SAME
// status as a plain probe that carries probe-miss evidence, a body
// wording variance on the beta row must not block (same route, same
// body, same status — the plain row is the evidence).
func TestDoctorBetaRowDoesNotBlockOnAWordingVariance(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case r.URL.Path == "/v1/messages":
			w.WriteHeader(http.StatusNotFound)
			if r.Header.Get("anthropic-beta") != "" {
				// Same miss, no probe-model echo in the message.
				w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model does not exist"}}`))
				return
			}
			w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model __doctor_probe__ does not exist"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"model_not_found","type":"invalid_request_error"}}`))
		}
	})
	res := Run(s.URL, "sk-x")
	if !res.AllPassed {
		t.Fatalf("a wording variance on the beta row must not block: %+v", res.Checks)
	}
	row, ok := rowNamed(t, res, nameBetaTolerance)
	if !ok || !row.Pass {
		t.Fatalf("beta row must be rescued to pass: %+v", row)
	}
	if !strings.Contains(row.Info, "matches the plain probe") {
		t.Errorf("beta row info %q must state the relative basis", row.Info)
	}
}

// rowNamed locates one check row by name and requires its presence — a
// name-filtered loop alone passes vacuously if the row ever vanishes.
func rowNamed(t *testing.T, res Result, name string) (Check, bool) {
	t.Helper()
	for _, c := range res.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

// AC-6 case 2: a 200 with a malformed body passed the OLD doctor (status
// only) while every catalog-fetching launch degraded silently to the
// placeholder. Doctor now runs the launch fetch itself, so the row is
// red with the parse error — the same sentence a launch would hit.
func TestDoctorRedWhenModelsBodyIsGarbage(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			// 200 + garbage: the dual-misleading case.
			w.Write([]byte(`not-json`))
			return
		}
		// Wire routes stay healthy: 400 proves the route exists under
		// doctor's ladder, isolating the failure to the models row.
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"probe shape"}`))
	})
	res := Run(s.URL, "sk-test-12345")
	if res.AllPassed {
		t.Fatal("a garbage-body catalog must preflight red")
	}
	_, err := gateway.Base{URL: s.URL, Token: "sk-test-12345"}.Models()
	if err == nil {
		t.Fatal("the launch path must fail the same gateway")
	}
	row, ok := rowNamed(t, res, nameModelsRow)
	if !ok {
		t.Fatal("the models row vanished from the result")
	}
	if row.Pass {
		t.Error("models row passed with an unparsable body")
	}
	if row.Info != err.Error() {
		t.Errorf("models row info %q must equal the launch-path error %q (AC-6: same message)", row.Info, err.Error())
	}
	if !strings.Contains(row.Info, "parse /v1/models:") {
		t.Errorf("models row info should be the parse error: %+v", row)
	}
}

// The grilled policy (2026-09-08): a well-formed 200 with ZERO usable ids
// stays a green row with an annotation — launches proceed on such a
// gateway by design (the standing honest-list degrade), so a red verdict
// would declare broken what the system deliberately tolerates; the
// annotation keeps doctor from being silent about it.
func TestDoctorAnnotatesAWellFormedEmptyCatalogAsGreen(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":""}]}`))
			return
		}
		w.Write([]byte(`{}`))
	})
	res := Run(s.URL, "sk-test-12345")
	row, ok := rowNamed(t, res, nameModelsRow)
	if !ok {
		t.Fatal("the models row vanished from the result")
	}
	if !row.Pass {
		t.Errorf("a well-formed zero-id catalog is annotated green, not red: %+v", row)
	}
	if !strings.Contains(row.Info, "0 models served") || !strings.Contains(row.Info, "placeholder") {
		t.Errorf("models row info %q must carry the grilled annotation", row.Info)
	}
}

// The guarded re-blame (review round): a mismatch where the PLAIN probe
// itself failed is not evidence about the beta header — a rate-limited
// burst (plain 429, beta accepted with 200) must keep the beta row's own
// green verdict instead of re-labeling it "possible beta-header
// rejection"; the plain row carries its own diagnosis.
func TestDoctorDoesNotBlameTheBetaHeaderWhenThePlainProbeFailed(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-beta") != "" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	})
	res := Run(s.URL, "sk-test-12345")
	if res.AllPassed {
		t.Fatal("a rate-limited plain probe must fail pre-flight")
	}
	beta, ok := rowNamed(t, res, nameBetaTolerance)
	if !ok {
		t.Fatal("the beta row vanished from the result")
	}
	if !beta.Pass {
		t.Errorf("beta accepted (200) while plain hit a rate limit — the beta row must keep its own green verdict, got: %+v", beta)
	}
	if strings.Contains(beta.Info, "beta-header rejection") {
		t.Errorf("beta row info %q re-blames the header for an unrelated plain failure", beta.Info)
	}
}
