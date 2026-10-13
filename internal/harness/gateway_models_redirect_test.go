package harness

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A redirecting gateway is itself an exfiltration channel for the stored
// token, so the /v1/models fetch must refuse the hop: an error naming the
// redirect comes back and the hop target records zero requests. Programs
// that degrade on fetch failure (Resolve) then launch with the CLI
// model instead of silently following into an unknown endpoint.
func TestEndpointBaseModelsRefusesRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gw-model"}]}`))
	}))
	t.Cleanup(target.Close)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(gateway.Close)

	ids, err := (Endpoint{BaseURL: gateway.URL, Token: "sk-secret"}).Base().Models()
	if err == nil {
		t.Fatalf("redirecting gateway must error; got ids %v", ids)
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error should name the refused redirect, got %q", err.Error())
	}
	if targetHits.Load() != 0 {
		t.Errorf("redirect target received %d request(s) with the token attached; must record zero", targetHits.Load())
	}
}

// A 3xx with no Location header refuses all the same — and the error must
// not render an empty "redirected to " for the missing hop URL.
func TestEndpointBaseModelsRefusesRedirectWithoutLocation(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(gateway.Close)

	_, err := (Endpoint{BaseURL: gateway.URL, Token: "sk-secret"}).Base().Models()
	if err == nil {
		t.Fatal("bare 3xx must error")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error should still name the refused redirect, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "redirected to ") {
		t.Errorf("empty Location should not render as 'redirected to ', got %q", err.Error())
	}
}
