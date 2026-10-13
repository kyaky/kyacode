package harness

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"kyacode/internal/gatewaytest"
)

// The Launch dispatch pins: the one-Resolve rule (resolve the Catalog
// decision once IFF the Spec is a carrier, hand it to the declared
// channel) asserted from the RECEIVER side. The fakes are UNREGISTERED
// declared Spec values — the composeSpec precedent: Launch consumes spec
// data, so the doubles need no registry presence, and a registered fake
// would trip the extend-the-table guards. The fakes assert the FULL
// handed decision (IDs in Resolve's order AND the Pin verdict), so the
// dropped-Resolve cell the prior review mutation-proved silent (deleting
// the IsCarrier gate kept the suite green — the mirrors call Resolve
// themselves) reds here: a zero Catalog renders neither the sentinel row
// nor a pin verdict. The child is a do-nothing stub shim on every
// platform (an env-carrier has no argv channel, so a test-binary child
// would recursively run the whole suite).

// stubShim writes a do-nothing child executable into dir and returns its
// path (the .cmd/.sh split mirrors main_test.go's stubHarness).
func stubShim(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		shim := filepath.Join(dir, name+".cmd")
		if err := os.WriteFile(shim, []byte("@echo off\r\nexit /b 0\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return shim
	}
	shim := filepath.Join(dir, name)
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return shim
}

// carrierRecord is what the fake carrier closures capture.
type carrierRecord struct {
	ids     []string
	pinBare string
	pinOK   bool
}

func carrierSandboxFake(shim string, rec *carrierRecord) Spec {
	return Spec{
		Name:          "fake-carrier-sbx",
		Wiring:        WiringSandbox,
		SandboxPrefix: "fake-carrier",
		Find:          func() (string, bool) { return shim, true },
		CarrierBuild: func(ep Endpoint, model string, cat Catalog) (Wire, error) {
			rec.ids = cat.IDs()
			rec.pinBare, _, rec.pinOK = cat.Pin()
			return Wire{Env: []string{"FAKE_CARRIER=1"}, Cleanup: func() {}}, nil
		},
	}
}

func carrierEnvFake(shim string, rec *carrierRecord) Spec {
	return Spec{
		Name:   "fake-carrier-env",
		Wiring: WiringEnv,
		Find:   func() (string, bool) { return shim, true },
		CarrierEnv: func(ep Endpoint, model string, cat Catalog) []string {
			rec.ids = cat.IDs()
			rec.pinBare, _, rec.pinOK = cat.Pin()
			return []string{"FAKE_CARRIER_ENV=1"}
		},
	}
}

func nonCarrierEnvFake(shim string) Spec {
	return Spec{
		Name:      "fake-plain-env",
		Wiring:    WiringEnv,
		Find:      func() (string, bool) { return shim, true },
		Env:       func(Endpoint, string) []string { return []string{"FAKE_PLAIN=1"} },
		ExtraArgs: func(string) []string { return nil },
	}
}

// TestLaunchHandsTheDegradeDecisionToBothCarrierChannels drives the dead
// gateway through both carrier channels and asserts the FULL degrade
// decision each received: the sentinel row alone, no pin — the
// sentinel-row contract's two halves arriving at the receiver intact.
func TestLaunchHandsTheDegradeDecisionToBothCarrierChannels(t *testing.T) {
	dead := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	shim := stubShim(t, t.TempDir(), "fake-carrier")

	recSbx := &carrierRecord{}
	if err := Launch(carrierSandboxFake(shim, recSbx), dead, "", nil, nil, nil); err != nil {
		t.Fatalf("Launch (sandbox carrier, dead gateway): %v", err)
	}
	if len(recSbx.ids) != 1 || recSbx.ids[0] != PlaceholderModelID {
		t.Errorf("sandbox carrier received IDs %v, want the sentinel row alone", recSbx.ids)
	}
	if recSbx.pinOK {
		t.Errorf("sandbox carrier received a pin verdict on a degraded fetch; want the no-pin half")
	}

	recEnv := &carrierRecord{}
	if err := Launch(carrierEnvFake(shim, recEnv), dead, "", nil, nil, nil); err != nil {
		t.Fatalf("Launch (env carrier, dead gateway): %v", err)
	}
	if len(recEnv.ids) != 1 || recEnv.ids[0] != PlaceholderModelID {
		t.Errorf("env carrier received IDs %v, want the sentinel row alone", recEnv.ids)
	}
	if recEnv.pinOK {
		t.Errorf("env carrier received a pin verdict on a degraded fetch; want the no-pin half")
	}
}

// TestLaunchHandsTheFetchedDecisionWithTheTypedModel drives a LIVE
// gateway: the CLI model leads the handed rows, the rest arrive sorted,
// and the pin verdict is the typed model in its bare form.
func TestLaunchHandsTheFetchedDecisionWithTheTypedModel(t *testing.T) {
	s := gatewaytest.Models(t, "sk-test-12345", []string{"zeta-model", "alpha-model"})
	ep := Endpoint{BaseURL: s.URL, Token: "sk-test-12345"}
	shim := stubShim(t, t.TempDir(), "fake-carrier")

	rec := &carrierRecord{}
	if err := Launch(carrierSandboxFake(shim, rec), ep, "m1", nil, nil, nil); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	wantIDs := []string{"m1", "alpha-model", "zeta-model"}
	if len(rec.ids) != len(wantIDs) {
		t.Fatalf("IDs = %v, want %v", rec.ids, wantIDs)
	}
	for i, id := range wantIDs {
		if rec.ids[i] != id {
			t.Fatalf("IDs = %v, want %v (CLI model leads, rest sorted)", rec.ids, wantIDs)
		}
	}
	if !rec.pinOK || rec.pinBare != "m1" {
		t.Errorf("Pin verdict = (%q, %v), want the typed model", rec.pinBare, rec.pinOK)
	}
}

// TestLaunchNeverFetchesForANonCarrier pins the other half of the
// one-Resolve rule: a non-carrier's launch performs NO fetch at all —
// driven against a COUNTING gateway, because a dead endpoint cannot
// prove the fact (Resolve degrades silently and fast, making a
// wrongly-gated launch observably identical to a correct one).
func TestLaunchNeverFetchesForANonCarrier(t *testing.T) {
	var hits atomic.Int32
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	})
	ep := Endpoint{BaseURL: s.URL, Token: "sk-test-12345"}
	shim := stubShim(t, t.TempDir(), "fake-plain")

	if err := Launch(nonCarrierEnvFake(shim), ep, "m1", nil, nil, nil); err != nil {
		t.Fatalf("Launch (plain env): %v", err)
	}
	if err := Launch(composeSpec("FAKE_PLAIN_SBX", ""), ep, "m1", nil, nil, nil); err != nil {
		t.Fatalf("Launch (plain sandbox): %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the non-carrier launches fetched /v1/models %d time(s) — Resolve must run only for a declared carrier", n)
	}
}
