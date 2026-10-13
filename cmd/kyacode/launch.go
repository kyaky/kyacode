package main

// The launch boundary (one of the package-main files from ticket 06):
// argument policy for `launch`, the endpoint resolution, and the child
// handoff into the ladder's supervision. Everything about the interrupt
// itself lives in internal/ladder; this file only feeds it.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"kyacode/internal/config"
	"kyacode/internal/harness"
	"kyacode/internal/ladder"
)

// cmdLaunch parses the CLI model boundary for `launch` and hands the
// declared Spec, the resolved endpoint, and the passthrough to
// harness.Launch under the ladder's Handoff. The requires-model gate runs
// here on the DECLARED field before the endpoint is touched (restoring
// the fail-closed-before-endpoint invariant: a model-less + key-less
// launch shows the actionable refusal, not the endpoint error); the
// canonical gate stays in harness.Launch for every launch path.
func cmdLaunch(args []string, h *ladder.Handoff, ch channels) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: kyacode launch <harness> [model] [-- extra args...]")
	}
	spec, err := harness.Lookup(args[0])
	if err != nil {
		return err
	}
	rest := args[1:]

	var passthrough []string
	for i, a := range rest {
		if a == "--" {
			passthrough = rest[i+1:]
			rest = rest[:i]
			break
		}
	}
	model := ""
	switch {
	case len(rest) == 0:
	case strings.HasPrefix(rest[0], "-"):
		// A leading flag: the user asked to pass harness flags directly.
		// Keep them for the passthrough (append below) and do not guess a
		// model from them.
		passthrough = append(rest, passthrough...)
	default:
		// An empty or whitespace-only model here is almost always a
		// quoting bug in a wrapping script (`launch claude "$MODEL" --`
		// with MODEL="" or MODEL="  "); running the bare harness silently
		// would hide it, and a whitespace-only id would ride verbatim
		// into harness pins (tickets 20). Outer whitespace around a real
		// id is command-line noise — trim once, preserve the interior.
		model = strings.TrimSpace(rest[0])
		if model == "" {
			return fmt.Errorf("empty <model> argument")
		}
		// The provider-scope rule is the harness's, not this boundary's:
		// the Spec owns recognition and the sentence (CheckModelArg), so
		// the compose shape cannot be re-derived — or drifted — on this
		// side of the seam (ticket 21, deepened by the 2026-09-16 review).
		// The canonical gate lives in harness.Launch's gate order for
		// every launch path; this pre-gate keeps the refusal AHEAD of the
		// endpoint resolution (same ordering rationale as the
		// requires-model pre-gate below).
		if err := spec.CheckModelArg(model); err != nil {
			return err
		}
		// A double quote in the model name is refused. The mechanism the
		// old rationale credited (a bare `"` injecting a second key into
		// the codex sandbox config.toml) is in fact prevented by the
		// carrier: it %q-quotes the model into the file and runs it
		// through its TOML string validator beforehand (and the opencode
		// channel is JSON, which escapes any quote on its own). The
		// guard's live purpose is uniform fail-fast of the quoting-bug
		// family — the same class the empty-model refusal above serves —
		// before the binary hunt, the official-installer prompt, and any
		// sandbox build.
		if strings.Contains(model, "\"") {
			return fmt.Errorf("model name must not contain double quotes")
		}
		// Anything else before `--` is not parseable; failing closed beats
		// silently dropping arguments the user typed.
		if flags := rest[1:]; len(flags) > 0 {
			return fmt.Errorf("unexpected arguments before -- (%s); put harness flags after `--`: kyacode launch %s [model] -- <args...>",
				strings.Join(flags, " "), spec.Name)
		}
	}
	// Fail closed when `--` was typed but nothing follows it.
	if len(args) > 1 && args[len(args)-1] == "--" {
		return fmt.Errorf("`--` must be followed by at least one harness argument")
	}

	// Fail closed BEFORE touching the endpoint (single-model harnesses are
	// meaningless without a model, and a silent untitled session is worse
	// than a refusal — catalog harnesses with an in-session picker keep
	// launching). The canonical gates live in harness.Launch's gate order
	// (ticket 04; the provider-scope gate joined with the declared carrier
	// channels), which read the same declared facts during every launch
	// path; these pre-gates read the fields themselves so the actionable
	// refusals win over an endpoint error when both would fire (the
	// pre-auth state).
	if err := spec.RequiresModelRefusal(model); err != nil {
		return err
	}

	ep, err := resolveEndpoint()
	if err != nil {
		return err
	}
	return harness.Launch(spec, ep, model, passthrough, func(msg string) bool {
		fmt.Fprintf(ch.stdout, "%s [y/N] ", msg)
		return confirmYes(os.Stdin)
	}, h)
}

func resolveEndpoint() (harness.Endpoint, error) {
	tok, err := config.Token()
	if err != nil {
		return harness.Endpoint{}, err
	}
	s, err := config.Load()
	if err != nil {
		return harness.Endpoint{}, err
	}
	return harness.Endpoint{BaseURL: s.Gateway, Token: tok}, nil
}

// confirmYes reads ONE answer line from r and reports the affirmative:
// case- and whitespace-tolerant (y / Y / yes / padded input), and every
// other answer — including an empty read at EOF, the non-interactive
// default, and outright garbage — is the fail-closed NO. Its own
// io.Reader seam is what lets the ACCEPT side table-pin directly
// (launch_answer_test.go): before it, dropping "yes" or the ToLower red
// no test — the spawn pins only fed "y". Reading strictly one line is
// the ordering contract the main_test fleet names: the answer is
// consumed BEFORE the installer exec and BEFORE the harness child, so
// the answer and the child's stdin never contend for the same byte.
func confirmYes(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
