// kyacode: launch coding harnesses against the KYA inference gateway with
// the user's own key. Nothing is written to the user's real harness config:
// env runners inject ephemeral process env, sandbox runners materialise a
// throwaway dir reclaimed when the launch returns (the harness package doc
// carries the full contract).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"kyacode/internal/config"
	"kyacode/internal/gateway"
	"kyacode/internal/harness"
	"kyacode/internal/ladder"
)

// Version is stamped into release builds via -ldflags "-X main.Version=…"
// (see .github/workflows/release.yml). A source build without the stamp
// reports the unnamed dev form and still exits 0 — never an error.
var Version = "v0.0.0-dev"

func main() {
	// The channel seam is wired HERE, once — before anything may report:
	// report and warning writers default to the process's real streams,
	// so spawn-level behavior is byte-identical while in-process tests
	// hand buffers (round-2 ticket 04 — the pattern the interrupt ladder
	// already uses). main's own error line below reads the process
	// stream directly: it runs after run() has returned, outside every
	// command body.
	ch := defaultChannels()
	if len(os.Args) < 2 {
		usage(ch.stdout)
		os.Exit(tierUsageMisuse)
	}
	// The signal set is the operator-facing disposition (README-armed
	// Ctrl-C semantics), so arming stays here; the interrupt escalation
	// itself is the ladder module's (ticket 06). The bare-invocation argc
	// gate below exits 2 with no handler installed; any dispatched
	// subcommand — including an unknown one — runs under the armed
	// handler, which is fine: the armed window is either fleeting
	// (dispatch/usage) or the launch the ladder supervises.
	//
	// Reclaim the codex sandbox (holding the plaintext key) even when we
	// are interrupted: the ladder's forced-exit path reclaims what the
	// deferred cleanup would have missed. Resetting the disposition on the
	// FIRST interrupt (the ladder's disarm-before-rungs ordering) means an
	// impatient second Ctrl-C is default-fatal to kyacode — the documented
	// limitation for SIGKILL remains.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	h := ladder.NewHandoff()
	exit := ladder.Run(ctx, stop, func() error { return run(os.Args[1:], h, ch) }, h, nil)
	if exit.Fail != nil {
		fmt.Fprintln(os.Stderr, "error:", exit.Fail)
	}
	os.Exit(exit.Code)
}

func run(args []string, h *ladder.Handoff, ch channels) error {
	// Every dispatched subcommand runs through its declared arg-shape
	// row (round-2 ticket 03): help-first where the row says so, the
	// byte-identical refusal where the argv overflows, the body only for
	// a clean shape. The usage aliases (help/-h/--help) deliberately
	// carry no row: they are the usage affordance, not a data mover
	// (extra args are ignored, exactly as before).
	var err error
	switch args[0] {
	case "auth":
		err = runShape("auth", args[1:], ch, cmdAuth)
	case "unauth":
		err = runShape("unauth", args[1:], ch, func(_ []string, ch channels) error { return cmdUnauth(ch) })
	case "gateway":
		err = runShape("gateway", args[1:], ch, cmdGateway)
	case "doctor":
		err = runShape("doctor", args[1:], ch, func(_ []string, ch channels) error { return cmdDoctor(ch) })
	case "status":
		err = runShape("status", args[1:], ch, func(_ []string, ch channels) error { return cmdStatus(ch) })
	case "models":
		err = runShape("models", args[1:], ch, func(_ []string, ch channels) error { return cmdModels(ch) })
	case "launch":
		err = runShape("launch", args[1:], ch, func(rest []string, ch channels) error { return cmdLaunch(rest, h, ch) })
	case "help", "-h", "--help":
		usage(ch.stdout)
	case "version":
		err = runShape("version", args[1:], ch, func(_ []string, ch channels) error { return cmdVersion(ch) })
	default:
		fmt.Fprintf(ch.stderr, "unknown command %q\n\n", args[0])
		usage(ch.stdout)
		os.Exit(tierUsageMisuse)
	}
	return err
}

func usage(w io.Writer) {
	fmt.Fprint(w, `kyacode — point coding harnesses at the KYA gateway with your own key,
wiring is injected at spawn time — never written to your real harness config

Usage:
  kyacode auth              Store YOUR gateway key (read from stdin, 0600 file)
  kyacode unauth            Remove the stored key
  kyacode gateway [url]     Show or set gateway base URL, no /v1 suffix needed (default `+config.DefaultGateway+`)
  kyacode doctor            Pre-flight: verify gateway speaks all three wire protocols
  kyacode status            Key / gateway / harness overview
  kyacode models            List gateway models (direct, `+gateway.RequestTimeout.String()+` timeout)
  kyacode launch <harness> [model] [-- extra args...]
  kyacode version           Print the build's version
  kyacode help              Show this usage (-h / --help)

Harnesses: `+strings.Join(harness.Names(), ", ")+`
  `+humanList(scopedComposers())+` take a BARE model id — they scope it
  themselves (kya/<id>); a provider-scoped argument is refused.

Environment:
  `+config.EnvGateway+`            Override gateway base URL
  `+config.EnvGatewayInsecure+`   Non-empty value allows plaintext http:// (loopback hosts only)
`)
}

// scopedComposers derives the bare-model-id set from the registry's
// declared ScopedComposer facts — the usage sentence renders from
// declared data instead of a hand-maintained copy that drifts the moment
// a Spec field changes (the runner list itself already renders via
// Names(); Runners() returns the same name-sorted order).
func scopedComposers() []string {
	var out []string
	for _, s := range harness.Runners() {
		if s.ScopedComposer {
			out = append(out, s.Name)
		}
	}
	return out
}

// humanList renders a name list the way the usage sentence reads:
// "a, b and c" (a one-name set prints bare).
func humanList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// cmdVersion reports the build's stamped version. The dev form names
// itself as unstamped so a "v0.0.0-dev" in a bug report is immediately
// recognizable as a source build, not a broken release.
func cmdVersion(ch channels) error {
	fmt.Fprintf(ch.stdout, "kyacode %s\n", Version)
	if Version == "v0.0.0-dev" {
		fmt.Fprintln(ch.stdout, "(built from source without the release stamp; tagged releases print their semver)")
	}
	return nil
}
