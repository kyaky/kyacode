package harness

import (
	"strings"
	"testing"
)

// The registry's declared-facts contract: a Spec must be coherent to
// register (a capability declared in data but absent as code — or vice
// versa — is a registration error, never a silently-lost capability at
// launch), and Launch dispatches on the declared Wiring class. Built for
// ticket 04: the optional-interface ladder (Runner → EnvRunner/Launcher →
// RequiresModel) died here; these pins carry the guarantees forward.

// goodEnvSpec builds the minimal valid WiringEnv spec; goodSandboxSpec the
// minimal valid WiringSandbox one. Both Find-miss with no installer, so
// any Launch that reaches the binary hunt ends at the deterministic
// "not installed" off-ramp — no process is ever spawned. Tests mutate
// one violation in per case.
func goodEnvSpec(name string) Spec {
	return Spec{
		Name:      name,
		Wiring:    WiringEnv,
		Find:      func() (string, bool) { return "", false },
		Env:       func(Endpoint, string) []string { return nil },
		ExtraArgs: func(string) []string { return nil },
	}
}

func goodSandboxSpec(name string) Spec {
	return Spec{
		Name:          name,
		Wiring:        WiringSandbox,
		SandboxPrefix: "probe",
		Find:          func() (string, bool) { return "", false },
		BuildEnv:      func(Endpoint, string) (Wire, error) { return Wire{}, nil },
	}
}

// sandboxRunners returns the registered specs of the declared
// WiringSandbox class — the one filter the wire/orphan pins iterate
// (three sites, one filter: a wiring-class change edits this once).
func sandboxRunners() []Spec {
	var out []Spec
	for _, s := range Runners() {
		if s.Wiring == WiringSandbox {
			out = append(out, s)
		}
	}
	return out
}

// TestRegisterRejectsIncoherentSpecs walks the whole coherence matrix at
// the registration seam: Launch can only ever see Specs these checks have
// passed, so a future runner's mistake fails at init with the reason,
// instead of silently losing a capability at launch (the round-5 hermes
// bug class: writing an Env method was dead code, and nothing could say
// so). Each case uses its own name so a validation bug that half-registers
// leaves a checkable (and visible) residue.
func TestRegisterRejectsIncoherentSpecs(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"empty name", func(s Spec) Spec { s.Name = ""; return s }(goodEnvSpec("probe-empty-name"))},
		{"nil Find", func(s Spec) Spec { s.Find = nil; return s }(goodEnvSpec("probe-nil-find"))},
		{"undeclared wiring", func(s Spec) Spec { s.Wiring = 0; return s }(goodEnvSpec("probe-zero-wiring"))},
		{"both wiring classes", func(s Spec) Spec { s.Wiring = WiringEnv | WiringSandbox; return s }(goodEnvSpec("probe-both-wiring"))},
		{"env runner without Env", func(s Spec) Spec { s.Env = nil; return s }(goodEnvSpec("probe-env-no-env"))},
		{"env runner declaring a sandbox prefix", func(s Spec) Spec { s.SandboxPrefix = "claude"; return s }(goodEnvSpec("probe-env-prefix"))},
		{"env runner declaring BuildEnv", func(s Spec) Spec { s.BuildEnv = func(Endpoint, string) (Wire, error) { return Wire{}, nil }; return s }(goodEnvSpec("probe-env-buildenv"))},
		{"sandbox runner without prefix", func(s Spec) Spec { s.SandboxPrefix = ""; return s }(goodSandboxSpec("probe-sbx-no-prefix"))},
		{"sandbox runner without BuildEnv", func(s Spec) Spec { s.BuildEnv = nil; return s }(goodSandboxSpec("probe-sbx-no-buildenv"))},
		{"sandbox runner declaring Env", func(s Spec) Spec { s.Env = func(Endpoint, string) []string { return nil }; return s }(goodSandboxSpec("probe-sbx-env"))},
		{
			// The dead-argv-channel case (round-5 prose bug: ExtraArgs ignored
			// for EnvArgSource runners): a sandbox runner's own argv rides
			// Wire.Args, so a declared ExtraArgs would be unconditionally
			// ignored — registration refuses the dead declaration instead.
			"sandbox runner declaring ExtraArgs",
			func(s Spec) Spec { s.ExtraArgs = func(string) []string { return nil }; return s }(goodSandboxSpec("probe-sbx-extraargs")),
		},
		{
			// Carrier facts are declared data too: a carrier channel beside
			// the plain channel of the same class is two launch paths for one
			// runner — registration refuses the ambiguity.
			"env carrier also declaring Env",
			func(s Spec) Spec {
				s.CarrierEnv = func(Endpoint, string, Catalog) []string { return nil }
				return s
			}(goodEnvSpec("probe-env-both-carrier")),
		},
		{
			"sandbox carrier also declaring BuildEnv",
			func(s Spec) Spec {
				s.CarrierBuild = func(Endpoint, string, Catalog) (Wire, error) { return Wire{}, nil }
				return s
			}(goodSandboxSpec("probe-sbx-both-carrier")),
		},
		{
			"env carrier declaring ExtraArgs",
			func(s Spec) Spec {
				s.Env = nil
				s.CarrierEnv = func(Endpoint, string, Catalog) []string { return nil }
				s.ExtraArgs = func(string) []string { return nil }
				return s
			}(goodEnvSpec("probe-env-carrier-extraargs")),
		},
		{
			"env spec declaring the sandbox carrier channel",
			func(s Spec) Spec {
				s.CarrierBuild = func(Endpoint, string, Catalog) (Wire, error) { return Wire{}, nil }
				return s
			}(goodEnvSpec("probe-env-carrierbuild")),
		},
		{
			"sandbox spec declaring the env carrier channel",
			func(s Spec) Spec { s.CarrierEnv = func(Endpoint, string, Catalog) []string { return nil }; return s }(goodSandboxSpec("probe-sbx-carrierenv")),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			panicked := func(f func()) (reason any) {
				defer func() { reason = recover() }()
				f()
				return nil
			}
			if reason := panicked(func() { Register(c.spec) }); reason == nil {
				t.Fatalf("Register accepted an incoherent spec (%+v); the launch path would trust declared data that was never checked", c.spec)
			}
			// Validation must fully precede the map write: a rejected
			// registration leaves no residue under this name (a ghost here
			// would shadow the real runner's facts — and trip later rows).
			if _, err := Lookup(c.spec.Name); err == nil {
				t.Errorf("a rejected registration left a residue for %q", c.spec.Name)
			}
		})
	}
}

// TestRegisterRejectsDuplicateSandboxPrefix pins the cross-spec anchor
// rule: the orphan sweep treats prefix and runner as a 1:1 match, so two
// sandbox runners sharing a prefix would put two runners' key-bearing
// dirs under one scan anchor. The first registration of a prefix is fine;
// the second stops the process at init.
func TestRegisterRejectsDuplicateSandboxPrefix(t *testing.T) {
	Register(goodSandboxSpec("probe-dup-first"))
	t.Cleanup(func() { delete(registry, "probe-dup-first") })
	second := goodSandboxSpec("probe-dup-second")
	panicked := func(f func()) (reason any) {
		defer func() { reason = recover() }()
		f()
		return nil
	}
	if reason := panicked(func() { Register(second) }); reason == nil {
		t.Fatalf("two sandbox runners registered the same prefix probe; the sweep's anchors would collide")
	}
	if _, err := Lookup("probe-dup-second"); err == nil {
		t.Errorf("the rejected duplicate left a residue")
	}
	// The first, valid registration must survive the rejection intact.
	if _, err := Lookup("probe-dup-first"); err != nil {
		t.Errorf("the valid first registration was disturbed: %v", err)
	}
}

// TestRegisterKeepsValidSpecs pins the positive path: a coherent spec of
// each wiring class registers, is discoverable with its declared facts
// intact, and the registry count reflects exactly one addition.
func TestRegisterKeepsValidSpecs(t *testing.T) {
	before := len(Names())
	Register(Spec{
		Name:          "probe-kept",
		Wiring:        WiringSandbox,
		SandboxPrefix: "probe-kept",
		Find:          func() (string, bool) { return "", false },
		BuildEnv:      func(Endpoint, string) (Wire, error) { return Wire{}, nil },
	})
	t.Cleanup(func() { delete(registry, "probe-kept") })
	if len(Names()) != before+1 {
		t.Fatalf("registry count = %d, want %d after one registration", len(Names()), before+1)
	}
	got, err := Lookup("probe-kept")
	if err != nil {
		t.Fatalf("registered spec is not discoverable: %v", err)
	}
	if got.Wiring != WiringSandbox || got.SandboxPrefix != "probe-kept" {
		t.Errorf("registered spec lost declared facts: %+v", got)
	}
}

// TestLaunchRejectsAnUndeclaredWiring pins the launch path's fail-closed
// contract for a Spec that bypassed registration (a zero-value or
// hand-built spec): the wiring class is declared DATA, and nothing
// launches without it — no optional-interface rescue attempt, no
// capability probing at the seam.
func TestLaunchRejectsAnUndeclaredWiring(t *testing.T) {
	bare := Spec{Name: "probe-bare", Find: func() (string, bool) { return "", false }}
	err := Launch(bare, Endpoint{}, "", nil, nil, nil)
	if err == nil {
		t.Fatal("Launch must fail closed for a spec with no declared wiring class")
	}
	if !strings.Contains(err.Error(), "neither injected env nor a sandbox cleanup contract") {
		t.Fatalf("Launch error = %v, want the fail-closed contract error", err)
	}
}

// TestLaunchRefusesIncoherentDeclaredFields pins the defensive half of
// validate: a hand-built spec that declares a wiring class but lacks the
// class-appropriate function fields must come back as the fail-closed
// error (a nil-call surface would panic mid-launch), never a spawn.
func TestLaunchRefusesIncoherentDeclaredFields(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{"env class, missing Env", Spec{Name: "probe-skel-env", Wiring: WiringEnv, Find: func() (string, bool) { return "", false }}, "declares WiringEnv with no Env"},
		{"env class, no Find at all", Spec{Name: "probe-skel-find", Wiring: WiringEnv, Env: func(Endpoint, string) []string { return nil }}, "declares no Find"},
		{"sandbox class, missing prefix", func(s Spec) Spec { s.SandboxPrefix = ""; return s }(goodSandboxSpec("probe-skel-prefix")), "declares WiringSandbox with no SandboxPrefix"},
		{"sandbox class, missing BuildEnv", func(s Spec) Spec { s.BuildEnv = nil; return s }(goodSandboxSpec("probe-skel-buildenv")), "declares WiringSandbox with no BuildEnv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Launch(c.spec, Endpoint{}, "", nil, nil, nil)
			if err == nil {
				t.Fatal("Launch must fail closed on incoherent declared fields, not nil-deref")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Launch error = %v, want it to carry %q", err, c.wantErr)
			}
		})
	}
}

// TestLaunchRequiresModelGateIsSpecData drives the gate through Launch for
// every wiring class: a declared RequiresModel runner refuses a model-less
// launch with the message the CLI has always printed, and the refusal
// happens at the seam's front — before the binary hunt (Find succeeding),
// before any sandbox build (the spy below), with "not installed" as the
// proven off-ramp for the declared-catalog rows. The gate reads declared
// data, so the old hand-pinned five-name registry snapshot is replaced by
// rows through the real seam.
func TestLaunchRequiresModelGateIsSpecData(t *testing.T) {
	// buildSpy is a BuildEnv that WOULD run if the launch ever got past the
	// gate and the binary hunt: a found Find is required for it to be
	// reachable at all, and the row asserts it never fired — a gate removed
	// or reordered behind resolveBin/BuildEnv turns this into a red row
	// instead of a green one.
	buildSpy := func(t *testing.T, fired *bool) func(Endpoint, string) (Wire, error) {
		return func(Endpoint, string) (Wire, error) {
			*fired = true
			t.Errorf("BuildEnv ran despite the model gate")
			return Wire{Cleanup: func() {}}, nil
		}
	}
	found := func() (string, bool) { return "probe-fake-bin", true }
	notFound := func() (string, bool) { return "", false }

	// requiresSbx is the spy row's spec: binary findable, so ONLY the gate
	// stands between the launch and the sandbox build.
	requiresSbx := goodSandboxSpec("gate-sbox")
	requiresSbx.RequiresModel = true
	requiresSbx.SandboxPrefix = "gate"
	requiresSbx.Find = found
	fired := false
	requiresSbx.BuildEnv = buildSpy(t, &fired)

	// requiresSbxReachable: same declared requirement, but the row intends
	// to WALK PAST the gate (model present) — firing the spy would prove
	// ordering, and the launch then ends at the fake binary's exec failure,
	// which is exit noise we do not want: notFound keeps it at the
	// deterministic off-ramp instead.
	requiresSbxModel := goodSandboxSpec("gate-sbox-model")
	requiresSbxModel.RequiresModel = true
	requiresSbxModel.Find = notFound

	// requiresEnv: the env-class row of the same gate — refusal must come
	// before the binary hunt, so the spec stays on the deterministic
	// not-installed off-ramp shape.
	requiresEnv := goodEnvSpec("gate-env")
	requiresEnv.RequiresModel = true

	cases := []struct {
		name    string
		spec    Spec
		model   string
		wantErr string
	}{
		{name: "requires, env, no model", spec: requiresEnv, wantErr: "requires a model"},
		{name: "requires, sandbox, no model", spec: requiresSbx, wantErr: "requires a model"},
		{name: "catalog, env, no model", spec: goodEnvSpec("gate-free-env"), wantErr: "not installed"},
		{name: "catalog, sandbox, no model", spec: goodSandboxSpec("gate-free-sbx"), wantErr: "not installed"},
		{name: "requires, sandbox, model present (walks past the gate)", spec: requiresSbxModel, model: "m1", wantErr: "not installed"},
	}
	if fired {
		t.Fatal("the sandbox build ran before the pin body started")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Launch(c.spec, Endpoint{}, c.model, nil, nil, nil)
			if err == nil {
				t.Fatal("expected an error (refusal, or the binary hunt's not-installed off-ramp)")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Launch error = %v, want it to carry %q", err, c.wantErr)
			}
		})
	}
}

// TestRegisteredSpecsDeclareTheWiringMatrix recreates the architecture
// review's capability matrix as a pinned table (ticket 04's spec note:
// "recreate as a table in this repo once Spec lands"). Every registered
// runner gets a row: name → wiring class, model requirement, composer
// shape, carrier channel, sandbox prefix. A row that is missing means a
// runner was registered without a conscious wire-protocol decision; a row
// that fired wrongly means a registered Spec drifted from its declared
// capability. Extend the table when adding a runner.
func TestRegisteredSpecsDeclareTheWiringMatrix(t *testing.T) {
	rows := []struct {
		name     string
		wiring   Wiring
		requires bool // single-model on the wire (the RequiresModel gate)
		scoped   bool // hands the harness the "kya/<id>" compose form
		carrier  bool // embeds the Model catalog (a carrier channel is set)
		hasArgs  bool // env path declares --model-style own argv
		install  bool // declares an official installer (nil Install: none)
		prefix   string
	}{
		{"claude", WiringEnv, true, false, true, false, true, ""},
		{"opencode", WiringEnv, false, true, true, false, true, ""},
		{"codex", WiringSandbox, true, false, true, false, true, "codex"},
		{"qwen", WiringEnv, true, false, false, true, false, ""},
		{"hermes", WiringSandbox, true, false, false, false, false, "hermes"},
		{"omp", WiringSandbox, false, true, true, false, true, "omp"},
		{"dsh", WiringSandbox, false, false, true, false, true, "dsh"},
		{"cline", WiringSandbox, true, false, false, false, true, "cline"},
		{"droid", WiringSandbox, false, false, true, false, true, "droid"},
		{"pi", WiringSandbox, false, true, true, false, true, "pi"},
		{"openclaw", WiringSandbox, false, true, true, false, false, "openclaw"},
	}
	seen := map[string]bool{}
	for _, row := range rows {
		s, err := Lookup(row.name)
		if err != nil {
			t.Fatalf("registered spec %q missing from the registry: %v", row.name, err)
		}
		seen[row.name] = true
		if s.Wiring != row.wiring {
			t.Errorf("%s: wiring = %v, want %v", row.name, s.Wiring, row.wiring)
		}
		if s.RequiresModel != row.requires {
			t.Errorf("%s: RequiresModel = %v, want %v", row.name, s.RequiresModel, row.requires)
		}
		if s.ScopedComposer != row.scoped {
			t.Errorf("%s: ScopedComposer = %v, want %v", row.name, s.ScopedComposer, row.scoped)
		}
		// Carrier-ness is declared by the channel field's presence (the
		// same nilness data read as hasArgs/install — never invoked).
		if got := s.IsCarrier(); got != row.carrier {
			t.Errorf("%s: carrier channel declared = %v, want %v", row.name, got, row.carrier)
		}
		if (s.ExtraArgs != nil) != row.hasArgs {
			t.Errorf("%s: ExtraArgs declared = %v, want %v", row.name, s.ExtraArgs != nil, row.hasArgs)
		}
		// A registry nilness DATA read (the hasArgs class): it never invokes
		// the closure, so the killed-design ban (re-deriving per-runner rows
		// from declarations) does not apply.
		if (s.Install != nil) != row.install {
			t.Errorf("%s: Install declared = %v, want %v", row.name, s.Install != nil, row.install)
		}
		if s.SandboxPrefix != row.prefix {
			t.Errorf("%s: SandboxPrefix = %q, want %q", row.name, s.SandboxPrefix, row.prefix)
		}
	}
	for _, name := range Names() {
		if !seen[name] {
			t.Errorf("registered runner %q has no row in the wiring matrix; extend the table when adding a runner", name)
		}
	}
}

// TestLaunchRefusesScopedModelArgBeforeTheBinaryHunt pins the
// provider-scope rung INSIDE the launch ladder (2026-09-16 deepening):
// harness.Launch consults the Spec's CheckModelArg after validate and
// before the binary hunt, so a direct library caller gets the same
// second-scope refusal the CLI pre-gate prints — the invariant is a seam
// property, not a caller's courtesy. The build spy would fire if the
// launch got past the gate; the found Find proves only the gate stands
// between the launch and the build.
func TestLaunchRefusesScopedModelArgBeforeTheBinaryHunt(t *testing.T) {
	buildSpy := func(t *testing.T, fired *bool) func(Endpoint, string, Catalog) (Wire, error) {
		return func(Endpoint, string, Catalog) (Wire, error) {
			*fired = true
			t.Errorf("CarrierBuild ran despite the provider-scope gate")
			return Wire{Cleanup: func() {}}, nil
		}
	}
	scopedSbx := goodSandboxSpec("scope-gate-sbx")
	scopedSbx.ScopedComposer = true
	scopedSbx.SandboxPrefix = "scope"
	// The Find spy is what pins the ORDER: if the binary hunt ran before
	// the gate, Find fires first and this row goes red — a successful
	// Find alone cannot tell gate-before-hunt from hunt-before-gate.
	findFired := false
	scopedSbx.Find = func() (string, bool) { findFired = true; return "probe-fake-bin", true }
	fired := false
	scopedSbx.BuildEnv = nil // the carrier channel replaces the plain one
	scopedSbx.CarrierBuild = buildSpy(t, &fired)

	err := Launch(scopedSbx, Endpoint{}, providerScopePrefix+"/m1", nil, nil, nil)
	if err == nil {
		t.Fatal("Launch must refuse a provider-scoped argument to a scoped composer")
	}
	if !strings.Contains(err.Error(), "provider-scoped model argument — type the bare id") {
		t.Fatalf("Launch error = %v, want the second-scope refusal", err)
	}
	if fired {
		t.Fatal("the carrier build ran before the scope gate refused")
	}
	if findFired {
		t.Fatal("the binary hunt ran before the provider-scope gate refused")
	}

	// A bare composer may receive a served id named like the scope: the
	// gate is runner-aware, so the launch walks past it to the binary
	// hunt's deterministic off-ramp.
	bareSbx := goodSandboxSpec("scope-gate-bare")
	bareSbx.SandboxPrefix = "scope2"
	err = Launch(bareSbx, Endpoint{}, providerScopePrefix+"/x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("bare composer + scope-named id = %v, want the walk-past to reach the not-installed off-ramp", err)
	}
}

// The refusal sentence is owned once (Spec.RequiresModelRefusal) and
// rendered byte-identical at both call sites - the tail beyond
// "requires a model" was unpinned until the 2026-10-13 review flagged a
// rewording that would pass every substring pin while silently changing
// the operator-facing usage shape (README.md documents the full form).
func TestRequiresModelRefusalOwnsTheSentence(t *testing.T) {
	spec := goodEnvSpec("refusal-env")
	spec.RequiresModel = true
	err := spec.RequiresModelRefusal("")
	if err == nil {
		t.Fatal("the gate must refuse a model-less launch")
	}
	want := spec.Name + " requires a model: kyacode launch " + spec.Name + " <model> [-- extra args...]"
	if err.Error() != want {
		t.Errorf("refusal = %q, want the owned sentence %q", err.Error(), want)
	}
	if err := spec.RequiresModelRefusal("m1"); err != nil {
		t.Errorf("a typed model must walk past the gate, got %v", err)
	}
}
