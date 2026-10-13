// Package gatewaytest shares one fake gateway across the test suites
// that pin gateway-shaped behavior (gateway, harness, doctor, cmd). One
// helper so the fake cannot drift between packages — several suites each
// invented their own /v1/models server (doctor_test, pi_gateway_test,
// gateway_models_test, omp_env_test, main_test), the test-side half of
// the preflight/launch disagreement (ticket 05 AC-7). production code
// never imports this package. What the MADE helper pins is the Bearer
// value on /v1/models (a wrong or missing key 401s); the header SET the
// module must not add to is pinned in the module's own shape test, not
// duplicated here.
package gatewaytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Raw runs an arbitrary gateway handler for the lifetime of the test.
func Raw(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// modelsList writes the OpenAI list shape for the ids — one encoding so
// every fake serving the models row cannot drift (the round-1 review
// killed exactly this drift the other way: four suites each inventing
// their own /v1/models server).
func modelsList(w http.ResponseWriter, ids []string) {
	data := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]string{"id": id})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// Models runs a fake gateway serving GET /v1/models with the given ids in
// the OpenAI list shape and asserting the Bearer token (a wrong or missing
// key 401s — the literal "Bearer " string is checked, not derived from the
// gateway module, so the fake catches a REPLACED keyed shape instead of
// silently agreeing with it). Other paths 404.
func Models(t *testing.T, wantToken string, ids []string) *httptest.Server {
	t.Helper()
	return Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		modelsList(w, ids)
	})
}

// The byte-exact bodies of the 2026-09-14 operator report — one
// declaration so the unit seam and the CLI seam cannot drift apart (the
// whole reason this package exists).
const (
	AnthropicProbeMiss = `{"error":{"message":"model __doctor_probe__ does not exist or is not available","type":"not_found_error"},"type":"error"}`

	OpenAIProbeMiss = `{"error":{"code":"model_not_found","message":"model __doctor_probe__ does not exist or is not available","param":"model","type":"invalid_request_error"}}`
)

// ReportedAnthropicGateway serves the 2026-09-14 operator-reported
// gateway: per-entry-protocol probe-miss bodies on a healthy gateway —
// anthropic routes carry Anthropic's documented error shape (top-level
// type:"error", error.type "not_found_error", NO OpenAI error.code, the
// probe model echoed in the message), the openai routes carry
// error.code "model_not_found", /v1/models lists six ids. Every keyed
// hit asserts the literal Bearer under the same discipline as Models.
func ReportedAnthropicGateway(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	return Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			modelsList(w, []string{"m1", "m2", "m3", "m4", "m5", "m6"})
		case strings.HasPrefix(r.URL.Path, "/v1/messages"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(AnthropicProbeMiss))
		case r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/responses":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(OpenAIProbeMiss))
		default:
			http.NotFound(w, r)
		}
	})
}

// DoctorServing is the route-capable fake doctor's green pin needs:
// Models alone 404s the four wire probes by design (it owns one route),
// so this fake serves the doctor probe set — messages (plain + beta),
// chat/completions, responses — plus the models row. Every keyed hit
// asserts the literal Bearer under the same discipline as Models, so a
// replaced keyed shape cannot sail through the e2e through the probe
// routes instead.
func DoctorServing(t *testing.T, wantToken string, ids []string) *httptest.Server {
	t.Helper()
	return Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			modelsList(w, ids)
			return
		}
		switch r.URL.Path {
		case "/v1/messages", "/v1/chat/completions", "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	})
}
