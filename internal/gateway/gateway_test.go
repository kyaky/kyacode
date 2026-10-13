package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The module's contract tests: one /v1 join, one keyed shape, typed
// redirect refusal, one (timestamped at freeze: ticket 05) error taxonomy
// for the models fetch, and the shared catalog success bar. These pins
// are the module half of "preflight and launch cannot disagree" — the
// doctor and launch halves pin their own consume sites.

// TestV1BaseJoinsExactlyOnce pins THE /v1-join rule (the shared suffix:
// trim trailing slashes, pass an already-/v1'd base through unchanged,
// never double the suffix). The table is transplanted from the old
// Endpoint.OpenAIBase pin — the algorithm must survive the move verbatim.
func TestV1BaseJoinsExactlyOnce(t *testing.T) {
	cases := map[string]string{
		"https://gw.example":       "https://gw.example/v1",
		"https://gw.example/":      "https://gw.example/v1",
		"https://gw.example/v1":    "https://gw.example/v1",
		"https://gw.example/v1/":   "https://gw.example/v1", // trailing slash trimmed before the /v1 check
		"https://gw.example/v1/v1": "https://gw.example/v1/v1",
		"https://gw.example?a=1":   "https://gw.example?a=1/v1", // joined verbatim: the rule is a string suffix rule
	}
	for in, want := range cases {
		if got := V1Base(in); got != want {
			t.Errorf("V1Base(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCanonicalBaseStripsExactlyOnce pins the strip half of the /v1 base
// grammar BESIDE its join half — the two complementary rules live in this
// package so one file answers "what is a gateway base", and any change
// re-evaluates both. The /v1/v1 edge is the deliberate asymmetry: the
// join keeps a caller-chosen /v1/v1 unchanged, the strip removes exactly
// ONE trailing /v1 (the config round-trip test pins the stored-form
// consequence). The compose property below is the cross-pin: strip ∘
// join is the identity on every canonical shape, doubled edge included.
func TestCanonicalBaseStripsExactlyOnce(t *testing.T) {
	cases := map[string]string{
		"https://gw.example":       "https://gw.example",
		"https://gw.example/":      "https://gw.example",
		"https://gw.example/v1":    "https://gw.example",
		"https://gw.example/v1/":   "https://gw.example",
		"https://gw.example/v1/v1": "https://gw.example/v1", // exactly one strip — the deliberate edge
		"https://gw.example?a=1":   "https://gw.example?a=1",
		"v1":                       "v1",  // no slash before the v1: HasSuffix is false regardless of the guard
		"/v1":                      "/v1", // the len>3 guard's actual protectee: "/v1" alone is not a suffix to strip
	}
	for in, want := range cases {
		if got := CanonicalBase(in); got != want {
			t.Errorf("CanonicalBase(%q) = %q, want %q", in, got, want)
		}
	}
	for in := range cases {
		if got := CanonicalBase(V1Base(in)); got != CanonicalBase(in) {
			t.Errorf("CanonicalBase(V1Base(%q)) = %q, want %q (strip∘join is the identity on the canonical form)", in, got, CanonicalBase(in))
		}
	}
}

// TestModelsTaxonomy drives the fetch through every error/success class
// and pins the exact returned message — the same sentence a launch degrades
// on and the doctor models row reports (the ticket's AC-6 "same message").
func TestModelsTaxonomy(t *testing.T) {
	t.Run("200 parses, sorts, drops empties, asserts Bearer and the joined path", func(t *testing.T) {
		var mu sync.Mutex
		var gotAuth, gotPath, gotCT string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			gotCT = r.Header.Get("Content-Type")
			mu.Unlock()
			w.Write([]byte(`{"object":"list","data":[{"id":"zeta"},{"id":""},{"id":"alpha"}]}`))
		}))
		t.Cleanup(s.Close)

		ids, err := Base{URL: s.URL, Token: "sk-t"}.Models()
		if err != nil {
			t.Fatalf("Models: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if gotAuth != "Bearer sk-t" {
			t.Errorf("Authorization = %q, want the one Bearer shape", gotAuth)
		}
		if gotPath != "/v1/models" {
			t.Errorf("path = %q, want /v1/models joined exactly once", gotPath)
		}
		if gotCT != "" {
			t.Errorf("Content-Type = %q, want absent on a bodyless request", gotCT)
		}
		if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "zeta" {
			t.Fatalf("ids = %v, want [alpha zeta] (sorted, empty dropped)", ids)
		}
	})

	t.Run("non-200 carries the status and body", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"bad key"}`))
		}))
		t.Cleanup(s.Close)
		_, err := Base{URL: s.URL, Token: "sk-t"}.Models()
		if err == nil || !strings.Contains(err.Error(), `gateway 401: {"error":"bad key"}`) {
			t.Fatalf("err = %v, want the non-200 taxonomy sentence with the body", err)
		}
	})

	t.Run("200 garbage body is a parse error", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`not-json`))
		}))
		t.Cleanup(s.Close)
		_, err := Base{URL: s.URL, Token: "sk-t"}.Models()
		if err == nil || !strings.Contains(err.Error(), "parse /v1/models:") {
			t.Fatalf("err = %v, want the parse error", err)
		}
	})

	t.Run("unreachable wraps the transport failure", func(t *testing.T) {
		_, err := Base{URL: "http://127.0.0.1:1", Token: "sk-t"}.Models()
		if err == nil || !strings.Contains(err.Error(), "gateway unreachable:") {
			t.Fatalf("err = %v, want the unreachable wrap", err)
		}
	})
}

// TestModelsRefusesRedirectTyped pins the typed refusal: the 3xx comes
// back as an errors.As-able *ErrRefusedRedirect (status and Location
// carried), the message is the ONE explanation sentence, a bare redirect
// does not render an empty "redirected to ", and the hop target records
// zero requests.
func TestModelsRefusesRedirectTyped(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"stolen"}]}`))
	}))
	t.Cleanup(target.Close)

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(s.Close)

	_, err := Base{URL: s.URL, Token: "sk-secret"}.Models()
	var refused *ErrRefusedRedirect
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a typed *ErrRefusedRedirect", err)
	}
	if refused.Status != http.StatusFound || refused.Location != target.URL {
		t.Errorf("refused = %+v, want status 302 and the hop target", refused)
	}
	if !strings.Contains(err.Error(), "redirect") || !strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %v, want the single refusal sentence", err)
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target received %d request(s) with the key attached; must record zero", n)
	}
}

// The no-Location form must not render an empty "redirected to " (the
// pinned behavior of the pre-module fetch error).
func TestModelsRefusesBareRedirectWithoutLocation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(s.Close)
	_, err := Base{URL: s.URL, Token: "sk-t"}.Models()
	var refused *ErrRefusedRedirect
	if !errors.As(err, &refused) || refused.Location != "" {
		t.Fatalf("err = %v, want a bare ErrRefusedRedirect", err)
	}
	if strings.Contains(err.Error(), "redirected to ") {
		t.Errorf("empty Location rendered as 'redirected to ': %v", err)
	}
}

// TestProbeHeaderShapeAndGuard pins the singular keyed shape and the
// anti-rekey guard: extras ride, but any extra naming a keyed/owned
// header (Authorization, X-Api-Key, Content-Type — case-insensitive) is
// refused client-side with no request sent.
func TestProbeHeaderShapeAndGuard(t *testing.T) {
	t.Run("Bearer shape, content-type with body, extras ride, no added keyed channel", func(t *testing.T) {
		var mu sync.Mutex
		var got http.Header
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got = r.Header.Clone()
			mu.Unlock()
			w.Write([]byte(`{"ok":true}`))
		}))
		t.Cleanup(s.Close)
		p := Base{URL: s.URL, Token: "sk-t"}.Probe(http.MethodPost, "/messages",
			`{"model":"probe"}`, map[string]string{"anthropic-version": "2023-06-01"})
		if p.Err != nil {
			t.Fatalf("Probe err: %v", p.Err)
		}
		if p.Status != http.StatusOK {
			t.Fatalf("status = %d, want 200", p.Status)
		}
		mu.Lock()
		defer mu.Unlock()
		if got.Get("Authorization") != "Bearer sk-t" {
			t.Errorf("Authorization = %q, want the one Bearer shape", got.Get("Authorization"))
		}
		if got.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want json beside a body", got.Get("Content-Type"))
		}
		if got.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version extra lost: %v", got)
		}
		// The header SET is part of the shape contract: no keyed header may
		// be ADDED alongside the module's Bearer shape (a dual-keyed shape
		// was exactly the doctor-passed/launch-403'd divergence), and on a
		// body-bearing probe the owned Content-Type is the only one.
		if _, present := got["X-Api-Key"]; present {
			t.Errorf("x-api-key added beside the Bearer shape: %v", got)
		}
	})

	network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the guard must refuse client-side; a request reached %s", r.URL.Path)
	}))
	t.Cleanup(network.Close)
	for _, k := range []string{"Authorization", "authorization", "X-Api-Key", "x-api-key", "Content-Type", "content-type"} {
		t.Run("guard refuses "+k, func(t *testing.T) {
			p := Base{URL: network.URL, Token: "sk-t"}.Probe(http.MethodPost, "/messages", "", map[string]string{k: "injected"})
			if p.Err == nil || !strings.Contains(p.Err.Error(), "refused") {
				t.Fatalf("extra %s: Probed.Err = %v, want the module-owns-the-shape refusal", k, p.Err)
			}
		})
	}
}

// TestProbeJoinsTheRouteSuffixOnce pins the suffix join: probe rows carry
// "/messages"-style suffixes and land on {V1Base}/suffix.
func TestProbeJoinsTheRouteSuffixOnce(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	p := Base{URL: s.URL + "/v1/", Token: "sk-t"}.Probe(http.MethodGet, "/models", "", nil)
	if p.Err != nil {
		t.Fatalf("Probe err: %v", p.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models (base already carried /v1 with a trailing slash)", gotPath)
	}
}

// TestProbeSurfacesRedirectAndUnreachable pins the unjudged exchange
// classes: a 3xx returns the typed refusal with the 3xx as Status, and a
// dead endpoint returns a wrapped unreachable error with Status 0.
func TestProbeSurfacesRedirectAndUnreachable(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	t.Cleanup(redirector.Close)

	p := Base{URL: redirector.URL, Token: "sk-t"}.Probe(http.MethodPost, "/messages", "{}", nil)
	var refused *ErrRefusedRedirect
	if !errors.As(p.Err, &refused) {
		t.Fatalf("Probe err = %v, want typed refusal", p.Err)
	}
	if p.Status != http.StatusMovedPermanently {
		t.Errorf("Probed.Status = %d, want the 3xx itself", p.Status)
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("hop target got %d requests; must record zero", n)
	}

	dead := Base{URL: "http://127.0.0.1:1", Token: "sk-t"}.Probe(http.MethodGet, "/models", "", nil)
	if dead.Status != 0 || dead.Err == nil || !strings.Contains(dead.Err.Error(), "unreachable") {
		t.Fatalf("dead-endpoint Probed = %+v, want Status 0 + unreachable Err", dead)
	}
}

// TestRefusedRedirectNeverTravels porting the old keyhttp client-test
// contract through the module's own seam: for every redirect code class,
// the caller receives the 3xx itself and the hop target records zero
// requests.
func TestRefusedRedirectNeverTravels(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var targetHits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetHits.Add(1)
			}))
			t.Cleanup(target.Close)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(code)
			}))
			t.Cleanup(gateway.Close)

			p := Base{URL: gateway.URL, Token: "sk-secret"}.Probe(http.MethodPost, "/messages", "", nil)
			var refused *ErrRefusedRedirect
			if !errors.As(p.Err, &refused) {
				t.Fatalf("code %d: Probed.Err = %v, want typed refusal", code, p.Err)
			}
			if p.Status != code {
				t.Errorf("code %d: Probed.Status = %d, want the redirect itself visible to the caller", code, p.Status)
			}
			if n := targetHits.Load(); n != 0 {
				t.Errorf("code %d: hop target got %d request(s); the key must never travel", code, n)
			}
		})
	}
}

// TestCatalogOK pins THE shared success bar (the exact negate of the
// launch-path degrade condition): a real list needs no error AND at least
// one id.
func TestCatalogOK(t *testing.T) {
	cases := []struct {
		ids []string
		err error
		ok  bool
	}{
		{[]string{"m1"}, nil, true},
		{[]string{"m1"}, fmt.Errorf("gateway 401: x"), false},
		{nil, nil, false}, // 200 with zero usable ids: NOT ok as catalog, policy may still annotate
		{nil, fmt.Errorf("x"), false},
		{[]string{"", ""}, nil, false}, // empty rows are dropped upstream, but the bar still guards
	}
	for i, c := range cases {
		if got := CatalogOK(c.ids, c.err); got != c.ok {
			t.Errorf("case %d: CatalogOK = %v, want %v", i, got, c.ok)
		}
	}
}
