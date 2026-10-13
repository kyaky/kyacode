package harness

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"kyacode/internal/gatewaytest"
)

// The launch seam's model fetch — Endpoint.Base().Models(), the gateway
// module call every catalog-carrying runner and `kyacode models` ride —
// error paths and shaping asserted at the launch side, while the exact
// error taxonomy itself is pinned in internal/gateway. The fetch wrapper
// these tests previously exercised was deleted with ticket 05.
func TestEndpointBaseModelsSortsIDs(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-t" {
			t.Errorf("Authorization = %q, want Bearer sk-t", auth)
		}
		w.Write([]byte(`{"object":"list","data":[{"id":"zeta"},{"id":"alpha"},{"id":""}]}`))
	})
	ids, err := (Endpoint{BaseURL: s.URL, Token: "sk-t"}).Base().Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "zeta" {
		t.Fatalf("ids = %v, want [alpha zeta] (sorted, empty dropped)", ids)
	}
}

func TestEndpointBaseModelsNon200IsError(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	})
	_, err := (Endpoint{BaseURL: s.URL, Token: "sk-t"}).Base().Models()
	if err == nil {
		t.Fatal("401 must be an error — an empty list would silently leave the sandbox without models")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want it to carry the status code", err)
	}
}

func TestEndpointBaseModelsBadJSONIsError(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not-json`))
	})
	_, err := (Endpoint{BaseURL: s.URL, Token: "sk-t"}).Base().Models()
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("err = %v, want a parse error", err)
	}
}

func TestEndpointBaseModelsUnreachable(t *testing.T) {
	_, err := (Endpoint{BaseURL: "http://127.0.0.1:1", Token: "sk-t"}).Base().Models()
	if err == nil {
		t.Fatal("unreachable gateway must error")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, want the unreachable diagnosis", err)
	}
}

func TestEndpointBaseModelsJoinsV1Once(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Write([]byte(`{"data":[]}`))
	})
	// Trailing-slash base must not double the /v1 segment (the V1Base
	// rule exactly once; also pinned in the gateway module's own table).
	_, err := (Endpoint{BaseURL: s.URL + "/v1/", Token: "sk-t"}).Base().Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q, want /v1/models exactly once", gotPath)
	}
}
