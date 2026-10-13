// Package harness: launch coding harnesses with spawn-time wiring to the
// KYA gateway. Nothing is written to the user's real harness config: env
// runners inject ephemeral process env (claude/anthropic vars,
// OPENCODE_CONFIG_CONTENT); sandbox runners materialise a throwaway dir
// (e.g. an isolated CODEX_HOME) that is removed when the launch returns.
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"kyacode/internal/gateway"
	"kyacode/internal/ladder"
)

// Endpoint is the resolved gateway endpoint handed to runners.
type Endpoint struct {
	BaseURL string // gateway base WITHOUT /v1 (claude appends /v1/messages itself)
	Token   string
}

// OpenAIBase returns base + /v1 for OpenAI-compatible SDKs. It is a
// one-line delegate to the gateway module's V1Base — THE /v1-join rule is
// implemented once (ticket 05); this remains so the ten runner files and
// their pins read as before (a file can call it twice — hermes does — so
// "files", not "call sites", is the defensible count).
func (e Endpoint) OpenAIBase() string { return gateway.V1Base(e.BaseURL) }

// Base is the launch-side composition of the gateway module's keyed
// destination: the resolved endpoint is exactly the two declared facts
// every keyed call carries (one of the module's two construction sites —
// doctor composes its own from the scalar boundary).
func (e Endpoint) Base() gateway.Base {
	return gateway.Base{URL: e.BaseURL, Token: e.Token}
}

// Wire is the spawn wiring one launch carries to its child: Env is the
// injected child-env rows, Args the runner's own argv contribution
// (complete — dsh's --patch pair rides here as a real return value), Root
// the sandbox dir the wiring materialised ("" on a runner that built no
// sandbox), and Cleanup the idempotent reclaim of that dir. On a failed
// build the return is the zero Wire: there is nothing to enforce cleanup
// against — the builder reclaimed whatever it had half-materialised.
type Wire struct {
	Root    string
	Env     []string
	Args    []string
	Cleanup func()
}

// resolveBin returns the path to s's executable, running the official
// installer once if the first Find misses. ensureInstalled returns the
// Find-verified path from its own final check, so resolveBin does exactly
// one Find per outcome — no redundant re-Find after the installer.
func resolveBin(s Spec, confirm func(string) bool) (string, error) {
	path, ok := s.Find()
	if !ok {
		var err error
		path, err = ensureInstalled(s, confirm)
		if err != nil {
			return "", err
		}
	}
	return path, nil
}

// Launch is THE launch entry point (ticket 04): one exported path for
// every wiring class, dispatched on the Spec's DECLARED wiring instead of
// optional-interface probing. The gate order is the sequence of earlier
// failures: an undeclared wiring (a spec that bypassed Register) fails
// closed with the contract sentence, then incoherent declared fields fail
// closed via validate (Register would have panicked on these two first —
// this seam re-checks because a hand-built Spec may bypass it), then the
// provider-scope gate refuses a scoped CLI argument to a scoped composer
// (the Spec answers for its own compose shape — the same CheckModelArg the
// CLI pre-gate runs for its pre-endpoint ordering), then the
// requires-model gate refuses before the binary hunt or any sandbox
// build, then the binary is resolved (with the official-installer
// prompt), then the declared wiring builds. A Catalog carrier's wiring
// build starts with the launch boundary resolving the Model catalog
// decision ONCE (Resolve — synchronous, bounded by the gateway module's
// timeout; never for a non-carrier, whose launch performs no fetch at
// all) and handing the decision to the declared carrier channel.
//
// The child argv is the runner's own contribution followed by the CLI's
// extra args — ONE merge site, shared by both wiring classes: on the
// launcher path the runner's own contribution is Wire.Args (complete —
// dsh's patch pair rides there as a real value), on the env path the
// declared ExtraArgs. The child inherits full env plus the wiring rows;
// the child starts through the ladder's StartChild (the mechanics the
// interrupt escalation polices — ConfigureGroup before Start, the joint
// child+cleanup publication after success), so on any exit after Start
// nothing of the wiring survives on disk. h may be nil: a launch without
// supervision publishes nothing.
func Launch(s Spec, ep Endpoint, model string, extra []string, confirm func(string) bool, h *ladder.Handoff) error {
	if !wiringDeclared(s.Wiring) {
		return fmt.Errorf("%s supports neither injected env nor a sandbox cleanup contract", s.Name)
	}
	if err := validate(s); err != nil {
		return err
	}
	if err := s.CheckModelArg(model); err != nil {
		return err
	}
	if err := s.RequiresModelRefusal(model); err != nil {
		return err
	}
	path, err := resolveBin(s, confirm)
	if err != nil {
		return err
	}
	// The Model catalog decision: resolved ONCE, here in Launch's gate
	// order, and only for a declared Catalog carrier — a non-carrier's
	// launch performs no fetch at all. Both wiring arms consume the same
	// value; no other site may resolve.
	var cat Catalog
	if s.IsCarrier() {
		cat = Resolve(ep, model)
	}
	var args, envRows []string
	var cleanup func()
	if s.Wiring == WiringSandbox {
		var w Wire
		var err error
		if s.CarrierBuild != nil {
			w, err = s.CarrierBuild(ep, model, cat)
		} else {
			w, err = s.BuildEnv(ep, model)
		}
		if err != nil {
			return err
		}
		defer w.Cleanup()
		args, envRows, cleanup = w.Args, w.Env, w.Cleanup
	} else {
		if s.CarrierEnv != nil {
			envRows = s.CarrierEnv(ep, model, cat)
		} else {
			if s.ExtraArgs != nil {
				args = s.ExtraArgs(model)
			}
			envRows = s.Env(ep, model)
		}
	}
	cmd := exec.Command(path, append(args, extra...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), envRows...)
	return ladder.StartChild(cmd, cleanup, h)
}

// RequiresModelRefusal is the one home of the requires-model gate: the
// predicate and the operator-facing sentence, owned once and rendered at
// the two frozen call sites — the CLI pre-gate in cmd/kyacode/launch.go
// (before endpoint resolution, so the actionable refusal wins over an
// endpoint error) and Launch's canonical gate above (before binary
// discovery). Placement is frozen at those two sites; the sentence is
// owned in this one method — the same split CheckModelArg already models
// (a Spec-owned sentence called from both sides of the seam).
func (s Spec) RequiresModelRefusal(model string) error {
	if model != "" || !s.RequiresModel {
		return nil
	}
	return fmt.Errorf("%s requires a model: kyacode launch %s <model> [-- extra args...]", s.Name, s.Name)
}

// ensureInstalled runs the official installer when the binary hunt misses
// and returns the Find-verified path from its own final check, so the
// caller (resolveBin) does not re-Find. A nil confirm channel is a REFUSAL
// (fail-closed): the installer never runs without a confirm surface the
// operator can actually answer — the same off-ramp as a "no" answer. The
// only production caller (cmdLaunch) always passes a non-nil closure; nil
// confirm is the test/library surface, and refusing it is the honest
// default (the rootdir/ladder fail-closed precedent).
func ensureInstalled(s Spec, confirm func(string) bool) (string, error) {
	if s.Install == nil {
		return "", fmt.Errorf("%s is not installed; install it first (see `kyacode status`)", s.Name)
	}
	bin, args := s.Install()
	if confirm == nil || !confirm(fmt.Sprintf("%s is not installed. Run official installer `%s %s` now?", s.Name, bin, strings.Join(args, " "))) {
		return "", fmt.Errorf("installation cancelled")
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to install %s: %w", s.Name, err)
	}
	path, ok := s.Find()
	if !ok {
		return "", fmt.Errorf("%s installed but binary not found; restart your shell", s.Name)
	}
	return path, nil
}

// runtimeGOOS lets runner files branch on the host OS (installers, home
// mechanisms, PATH shaping differ per platform) without each file
// importing runtime for a single call.
func runtimeGOOS() string { return runtime.GOOS }
