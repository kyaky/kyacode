package main

// The subcommand bodies (one of the package-main files from ticket 06):
// auth/unauth/gateway/doctor/status/models and their small formatting
// helpers. main.go keeps dispatch and usage; launch.go keeps the launch
// boundary.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"kyacode/internal/config"
	"kyacode/internal/doctor"
	"kyacode/internal/harness"
)

// channels is the writer-only seam the command bodies report through
// (round-2 ticket 04, the pattern the interrupt ladder already uses and
// CONTEXT.md credits): reports and warnings travel through parameters
// instead of the global streams, so the CLI battery can run in-process.
// Writer defaults wire to the current global streams (defaultChannels,
// called exactly once in main), so every command's output stays
// byte-identical — the ports moved, the topology did not: warning-class
// lines keep their stderr channel, report lines keep stdout. Nothing
// here touches the real tty/stdin branches (auth's prompt reads the
// actual file descriptors) and there is no root-dir seam.
type channels struct {
	stdout io.Writer
	stderr io.Writer
}

func defaultChannels() channels { return channels{stdout: os.Stdout, stderr: os.Stderr} }

func cmdUnauth(ch channels) error {
	if err := config.DeleteToken(); err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "key removed")
	return nil
}

// Input routing: stdin redirected (pipe/file) or --stdin reads the raw
// stream; a real terminal gets the hidden-password read (x/term, echo
// off) with the prompt written only when stdout is also a terminal — a
// piped run's stdout carries nothing but the saved-path line.
//
// The argv shape is declared on the auth row of the shape table (the
// credential-bearing data mover): a sole `--stdin` is the only legal
// argument, `auth --help` is help-first (usage, no key read), and
// anything else is the counting refusal — the gate in shape.go runs it
// before this body ever reads a credential.
func cmdAuth(args []string, ch channels) error {
	key, err := readKey(args, ch)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if err := config.SaveToken(key); err != nil {
		return err
	}
	p, _ := config.TokenPath()
	if back, err := config.Token(); err != nil || back != key {
		return fmt.Errorf("token write verification failed")
	}
	fmt.Fprintln(ch.stdout, "key saved to", p)
	return nil
}

// readAllKey is the shared stdin read of the --stdin and non-tty
// branches: read whole, trim surrounding whitespace. The BRANCHES stay
// (their gating reads the REAL file descriptors and is pinned
// spawn-tested forever); only the identical read body is shared.
func readAllKey() (string, error) {
	b, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readKey routes its reports (the hidden-read prompt) through the
// caller's channels, but the tty/stdin branches themselves are pinned
// spawn-tested forever: their gating reads the REAL file descriptors.
func readKey(args []string, ch channels) (string, error) {
	if len(args) > 0 && args[0] == "--stdin" {
		return readAllKey()
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return readAllKey()
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprint(ch.stdout, "paste your gateway key: ")
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	// The newline under the hidden read belongs to the terminal, not to
	// stdout's data stream: a piped stdout carries only the saved-path line.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(ch.stdout)
	}
	if err != nil && len(b) == 0 {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// cmdGateway shows (no args) or replaces (one arg) the persisted
// gateway. Its argv shape is the gateway row of the shape table (the
// exactly-one data mover: help-first on a sole -h/--help, the echoing
// one-URL refusal above it) — the gate in shape.go declines the misuse
// that used to persist args[0] and silently drop the rest. Whitespace
// around the URL is trimmed before validation, the same trim the
// KYA_GATEWAY env override applies — a copied-URL-with-trailing-space
// must behave identically from both sources. The set path never reads
// the old value: it normalizes locally, validates with the same rules
// Load enforces, and only then writes — so a rejected URL persists
// nothing, and a poisoned or unreadable config.json is repaired by the
// very next successful `gateway <url>` instead of blocking it behind a
// hard Load error.
func cmdGateway(args []string, ch channels) error {
	if len(args) > 0 {
		raw := args[0]
		url := config.NormalizeGateway(strings.TrimSpace(raw))
		if url == "" {
			return fmt.Errorf("gateway must start with http:// or https://")
		}
		if err := config.ValidateGateway(url); err != nil {
			return err
		}
		s := config.Setting{Gateway: url}
		if err := config.Save(s); err != nil {
			return err
		}
		// The note explains a transformation that happened: compare the
		// TRIMMED input against the normalized URL, so a whitespace-only
		// difference (no /v1, no slash touched) does not print a
		// factually wrong note.
		if strings.TrimSpace(raw) != url {
			fmt.Fprintln(ch.stdout, "note: normalized to the canonical no-/v1 form (claude and openai consumers append the suffix themselves); stored:", url)
		}
		fmt.Fprintln(ch.stdout, "gateway:", url)
		return nil
	}
	s, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "gateway:", s.Gateway)
	return nil
}

func cmdDoctor(ch channels) error {
	key, err := config.Token()
	if err != nil {
		return fmt.Errorf("doctor needs a stored key: %w", err)
	}
	s, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "gateway:", s.Gateway)
	res := doctor.Run(s.Gateway, key)
	ok := doctor.Render(ch.stdout, res)
	// The capability honesty note rides EVERY pre-flight, green or red:
	// what the probes prove does not change with the verdict.
	fmt.Fprintln(ch.stdout, "note: pre-flight proves wire reachability and the model list — not real-model inference, streaming, or tool calls")
	if !ok {
		return fmt.Errorf("gateway pre-flight FAILED — direct connect cannot proceed")
	}
	if res.Warned {
		fmt.Fprintln(ch.stdout, "all gateway checks passed (warnings above do not block direct connect)")
	} else {
		fmt.Fprintln(ch.stdout, "all gateway checks passed")
	}
	return nil
}

func cmdStatus(ch channels) error {
	s, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Fprintln(ch.stdout, "gateway:", s.Gateway)
	if tok, err := config.Token(); err == nil {
		// Avoid a slice panic on pathologically short stored tokens.
		if preview := shortTokenPreview(tok); preview != "" {
			fmt.Fprintf(ch.stdout, "key:     stored (%s, %d chars) at %s\n", preview, len(tok), orPlaceholder(config.TokenPath()))
		} else {
			fmt.Fprintf(ch.stdout, "key:     stored (%d chars) at %s\n", len(tok), orPlaceholder(config.TokenPath()))
		}
	} else if errors.Is(err, config.ErrNoToken) {
		fmt.Fprintln(ch.stdout, "key:     not stored (run `kyacode auth`)")
	} else {
		// Any other read failure must NOT collapse into "not stored":
		// the fix (`kyacode auth`) cannot even read the file to overwrite
		// it. The diagnostic command prints the cause (config.Token's
		// taxonomy carries the operator hint).
		fmt.Fprintf(ch.stdout, "key:     %v\n", err)
	}
	removed, sweepErr := harness.SweepOrphans()
	if sweepErr != nil {
		// The sweep is hygiene, not launch-critical: report loudly, keep
		// status useful (the harness error already names every stuck dir).
		fmt.Fprintf(ch.stderr, "WARNING: orphan sweep incomplete: %v\n", sweepErr)
	}
	if len(removed) > 0 {
		fmt.Fprintf(ch.stdout, "swept %d orphaned sandbox(es)\n", len(removed))
		for _, dir := range removed {
			fmt.Fprintf(ch.stdout, "  removed %s\n", dir)
		}
	}
	fmt.Fprintln(ch.stdout)
	w := tabWriter(ch)
	fmt.Fprintln(w, "HARNESS\tBINARY\tMODE")
	for _, s := range harness.Runners() {
		bin := "missing"
		if p, ok := s.Find(); ok {
			bin = p
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, bin, s.Description)
	}
	return w.Flush()
}

func cmdModels(ch channels) error {
	models, err := fetchModels()
	if err != nil {
		return err
	}
	for _, m := range models {
		fmt.Fprintln(ch.stdout, m)
	}
	return nil
}

// fetchModels delegates to the gateway module (same fetch, filter and
// sort as every sandbox config build), so `models` and the launch path
// cannot drift: one keyed client, one catalog shape.
func fetchModels() ([]string, error) {
	ep, err := resolveEndpoint()
	if err != nil {
		return nil, err
	}
	return ep.Base().Models()
}

// shortTokenPreview renders the stored key's preview for `status`: the
// first 3 and last 4 characters — but only for keys long enough that the
// preview stays a fraction of the value (config accepts 8-char keys, and
// 3+4 would reveal all but one character of those). A shorter stored key
// shows no preview at all.
func shortTokenPreview(tok string) string {
	if len(tok) >= 16 {
		return tok[:3] + "…" + tok[len(tok)-4:]
	}
	return ""
}

func tabWriter(ch channels) *tabwriter.Writer {
	return tabwriter.NewWriter(ch.stdout, 0, 4, 2, ' ', 0)
}

// orPlaceholder returns the string, or a "?" placeholder when resolution
// fails. Status formatting is best-effort: the key row is already on
// screen when this runs, and a missing/failed token-path lookup must not
// fail the whole `status` command over a display detail — but it also
// must not be silent, hence the visible placeholder.
func orPlaceholder(s string, err error) string {
	if err != nil {
		return "?"
	}
	return s
}
