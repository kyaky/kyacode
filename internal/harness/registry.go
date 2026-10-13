// Package harness — the registry. A runner registers a Spec: its
// declared launch capability as DATA (wiring class, model requirement,
// sandbox prefix, composer shape) plus the functions that carry the
// launch. Launch dispatches on the declared facts instead of discovering
// capabilities by type assertion at the seam (ticket 04): a capability
// declared but absent as code — or declared and never read because the
// wiring class disagrees — is a registration error, not a silently lost
// one at launch.
package harness

import (
	"fmt"
	"sort"
)

// Wiring is a runner's declared child-wiring class. The two classes are
// exclusive by construction: a runner registers with exactly one bit
// (Register refuses both — a runner serving both would need two launch
// paths to disambiguate, which is the pre-Spec shape this type replaces).
type Wiring uint8

const (
	// WiringEnv: the wiring is pure injected process env (claude,
	// opencode, qwen) — spawning writes nothing to disk.
	WiringEnv Wiring = 1 << iota
	// WiringSandbox: the wiring materialises a throwaway sandbox whose
	// scope may leak disk state; BuildEnv returns the full launch Wire
	// (env rows, own argv, root, reclaim) and the launch boundary cleans
	// it up.
	WiringSandbox
)

// Spec declares one runner's launch capability. The wiring-class facts
// (Wiring, RequiresModel, ScopedComposer, SandboxPrefix) are the
// registry's load-bearing data — dispatch reads them — and the function
// fields are the launch surface Launch calls through. Exactly one of Env
// and BuildEnv is set, matching Wiring; a nil Install means the runner
// ships no official installer.
type Spec struct {
	Name        string
	Description string
	Wiring      Wiring

	// RequiresModel marks runners whose typed model is the entry point:
	// launch refuses these without an explicit model instead of silently
	// starting on whatever sorted first in their picker's catalog. The
	// catalog still serves in-session switching (codex and claude carry
	// pickers); the typed model is what the operator chose to start on.
	RequiresModel bool

	// ScopedComposer: the runner hands the harness the provider-scoped
	// "kya/<id>" model form, so a provider-scoped CLI argument would
	// compose a second scope layer (an id no gateway serves). The CLI
	// boundary refuses scoped arguments for these.
	ScopedComposer bool

	// SandboxPrefix is the dir-name prefix this runner's sandboxes use
	// ("codex", "cline", ...) — the orphan sweep derives its match list
	// from the registry instead of a hand-maintained copy, and the
	// creation site (sandbox.New) is inside the same BuildEnv body the
	// spec's prefix names, so the two cannot drift. Required for
	// WiringSandbox, forbidden for WiringEnv.
	SandboxPrefix string

	// Find locates the harness executable.
	Find func() (string, bool)
	// Install is the official installer when missing (nil: none).
	// Availability IS the declaration — a nil Install is the only
	// "cannot install" fact the launch reads: the registry's own
	// registration-error doctrine (a capability declared in data but
	// absent as code is a registration error, never a silently lost
	// one) leaves no room for a second, runtime-decoded channel of the
	// same fact.
	Install func() (bin string, args []string)
	// ExtraArgs is the harness's own argv for the pure-env path (nil:
	// none). The launcher path never reads it — the runner's own argv
	// rides Wire.Args — so only env-wired runners may set it.
	ExtraArgs func(model string) []string
	// Env returns the env rows to inject into the child (WiringEnv).
	Env func(ep Endpoint, model string) []string
	// BuildEnv materialises the launch's Wire, including the sandbox life
	// cycle (WiringSandbox).
	BuildEnv func(ep Endpoint, model string) (Wire, error)

	// CarrierBuild is BuildEnv for a Catalog carrier (WiringSandbox): the
	// launch boundary resolves the Model catalog decision ONCE (Resolve,
	// after the local refusals and the binary hunt) and hands it over —
	// the carrier renders its own config schema from the decision and
	// never fetches, branches on fetched-ness, or re-derives a pin from
	// the rows. Carrier-ness is declared by THIS field's presence (the
	// same declared-data mechanism as Wiring): validate refuses a spec
	// that sets it with the wrong class or alongside BuildEnv. Whether a
	// carrier may also declare RequiresModel is a carrier-body contract,
	// not a declaration check — the wiring must serve every catalog row
	// the carrier renders into its picker (see CarrierEnv's doc; codex
	// and claude both hold it: one provider and one key carry every row).
	CarrierBuild func(ep Endpoint, model string, cat Catalog) (Wire, error)
	// CarrierEnv is Env for an env-wired Catalog carrier (WiringEnv,
	// opencode's class): same one-Resolve handover as CarrierBuild, on
	// the pure-env path. The carrier contract a new author must hold:
	// the wiring this body emits must serve EVERY catalog row the
	// carrier renders into its harness's picker — the rows are
	// picker-switchable mid-session, so a carrier whose wiring is
	// row-specific must not be a carrier (claude's shape: the alias pins
	// map every picker row onto the wired base+token, and the gateway
	// serves each row's id). A WiringEnv carrier declares exactly one of Env
	// and CarrierEnv; ExtraArgs stays forbidden beside CarrierEnv (the
	// launcher would never read it — declared-but-unreadable is a
	// registration error).
	CarrierEnv func(ep Endpoint, model string, cat Catalog) []string
}

// IsCarrier reports whether the Spec declares a catalog-carrier channel
// — the CONTEXT "Catalog carrier" concept, named so the Resolve gate,
// validate, and the matrix and pin walks read it as the vocabulary
// instead of restating the two-field disjunction.
func (s Spec) IsCarrier() bool { return s.CarrierBuild != nil || s.CarrierEnv != nil }

// registry is the declared-fact table. Every production entry passes
// Register's init-time validation (validate + cross-spec anchor
// uniqueness); Launch still re-checks coherence defensively, so a
// hand-built Spec that bypassed registration fails closed instead of
// nil-panicking the process.
var registry = map[string]Spec{}

// init is the registration matrix: each runner's Spec literal lives in
// its own file (claude.go: claudeSpec, ...) and this block registers the
// whole set — the wiring matrix viewable in one page, enforced coherent
// by Register at init time.
func init() {
	for _, s := range []Spec{
		claudeSpec,
		opencodeSpec,
		codexSpec,
		qwenSpec,
		hermesSpec,
		ompSpec,
		dshSpec,
		clineSpec,
		droidSpec,
		piSpec,
		openclawSpec,
	} {
		Register(s)
	}
}

// wiringDeclared is the class-validity predicate, shared by Register
// (which panics on a violation) and Launch (which fails closed): exactly
// one wiring class may be declared.
func wiringDeclared(w Wiring) bool {
	return w == WiringEnv || w == WiringSandbox
}

// validate is the single coherence check of a declared Spec: a capability
// in data without the code — or code no launch can ever read because the
// declared class disagrees — is invalid, whoever validates. Register
// panics on its result; Launch (defensively, for a spec that bypassed
// registration) returns it as the fail-closed error.
func validate(s Spec) error {
	if s.Name == "" {
		return fmt.Errorf("harness: a spec must carry a name")
	}
	if s.Find == nil {
		return fmt.Errorf("harness: %q declares no Find", s.Name)
	}
	if !wiringDeclared(s.Wiring) {
		return fmt.Errorf("harness: %q declares wiring = %d, want WiringEnv or WiringSandbox (exactly one)", s.Name, s.Wiring)
	}
	// (The former blanket ban on RequiresModel beside any carrier channel
	// is gone, recorded here so it is not re-added reflexively: the
	// invariant it gestured at — the wiring must serve every catalog row
	// the picker offers — is a property of the carrier BODY, not of the
	// declaration, and both shapes that need the combination satisfy it
	// structurally. codex serves every row through one provider and one
	// key; claude's alias pins map the picker rows onto the wired
	// base+token. The contract lives on the CarrierEnv/CarrierBuild docs
	// where a new carrier author reads it.)
	if s.Wiring == WiringEnv {
		if s.Env == nil && s.CarrierEnv == nil {
			return fmt.Errorf("harness: %q declares WiringEnv with no Env", s.Name)
		}
		if s.Env != nil && s.CarrierEnv != nil {
			return fmt.Errorf("harness: %q declares WiringEnv with both Env and CarrierEnv — exactly one env channel may be declared", s.Name)
		}
		if s.BuildEnv != nil {
			return fmt.Errorf("harness: %q declares WiringEnv with a BuildEnv it could never reach", s.Name)
		}
		if s.CarrierBuild != nil {
			return fmt.Errorf("harness: %q declares WiringEnv with a CarrierBuild the launch can never reach (sandbox carriers declare CarrierBuild)", s.Name)
		}
		if s.CarrierEnv != nil && s.ExtraArgs != nil {
			return fmt.Errorf("harness: %q declares WiringEnv with CarrierEnv and ExtraArgs the launch can never read (own argv rides the env channel only for non-carriers)", s.Name)
		}
		if s.SandboxPrefix != "" {
			return fmt.Errorf("harness: %q declares WiringEnv with sandbox prefix %q it never materialises", s.Name, s.SandboxPrefix)
		}
		return nil
	}
	if s.BuildEnv == nil && s.CarrierBuild == nil {
		return fmt.Errorf("harness: %q declares WiringSandbox with no BuildEnv", s.Name)
	}
	if s.BuildEnv != nil && s.CarrierBuild != nil {
		return fmt.Errorf("harness: %q declares WiringSandbox with both BuildEnv and CarrierBuild — exactly one build channel may be declared", s.Name)
	}
	if s.Env != nil {
		return fmt.Errorf("harness: %q declares WiringSandbox with an Env the launch can never read (env rows ride Wire.Env)", s.Name)
	}
	if s.CarrierEnv != nil {
		return fmt.Errorf("harness: %q declares WiringSandbox with a CarrierEnv the launch can never read (env carriers declare CarrierEnv)", s.Name)
	}
	if s.SandboxPrefix == "" {
		return fmt.Errorf("harness: %q declares WiringSandbox with no SandboxPrefix — key-bearing dirs would escape the orphan sweep", s.Name)
	}
	if s.ExtraArgs != nil {
		return fmt.Errorf("harness: %q declares WiringSandbox with ExtraArgs the launch can never read (own argv rides Wire.Args)", s.Name)
	}
	return nil
}

// Register adds a runner's Spec to the registry. It fails LOUDLY on an
// incoherent declaration: the process must stop at init, not surface a
// silently-lost wiring half at launch later.
func Register(s Spec) {
	if s.Name == "" {
		panic("harness: a spec must carry a name")
	}
	if _, dup := registry[s.Name]; dup {
		panic(fmt.Sprintf("harness: duplicate registration for %q", s.Name))
	}
	if err := validate(s); err != nil {
		panic(err)
	}
	// Cross-spec anchor uniqueness: the orphan sweep treats prefix and
	// runner as a 1:1 match — a second runner sharing the prefix would put
	// two runners' key-bearing dirs under one anchor.
	if s.Wiring == WiringSandbox {
		for _, other := range registry {
			if other.Wiring == WiringSandbox && other.SandboxPrefix == s.SandboxPrefix {
				panic(fmt.Sprintf("harness: %q and %q both declare sandbox prefix %q — the orphan sweep's anchors would collide", other.Name, s.Name, s.SandboxPrefix))
			}
		}
	}
	registry[s.Name] = s
}

// Lookup returns the registered Spec for a harness name.
func Lookup(name string) (Spec, error) {
	s, ok := registry[name]
	if !ok {
		return Spec{}, fmt.Errorf("unknown harness %q (available: %v)", name, Names())
	}
	return s, nil
}

func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Runners returns every registered Spec, sorted by name (the registry is
// the declared wiring matrix; consumers iterate it — status table, orphan
// sweep, tests — rather than any hand-maintained list).
func Runners() []Spec {
	names := Names()
	out := make([]Spec, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
