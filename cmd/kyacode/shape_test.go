package main

// The registration contract for the arg-shape table: every dispatched
// name resolves to a row, every row's refusal is the byte-identical
// sentence the spawn-level fleet pins, and a broken declaration is a
// loud registration error — never a check lost below dispatch.

import (
	"io"
	"strings"
	"testing"
)

// withHermeticHome isolates the in-process run() drives in a temp HOME:
// no real ~/.kyacode is ever touched and no env-override gateway leaks
// in (the refusal arms below never reach the bodies that would fetch).
func withHermeticHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KYA_GATEWAY", "")
	t.Setenv("KYA_GATEWAY_INSECURE", "")
}

// silentChannels keeps the in-process drives' reports off the test log:
// the usage blocks and banner rows they would print are asserted by the
// spawn-level pins (and, for ticket 04's battery, captured buffers).
var silentChannels = channels{stdout: io.Discard, stderr: io.Discard}

// TestShapeRowsRefuseThroughRun drives every table row's refusal arm
// through run() itself — one row at a time, each individually
// checkable. The refused argv never reaches the command body: the
// noArgs rows stay argumentless down to (and including) `unauth --help`,
// and the launch row passes its shape through to the body's parse.
func TestShapeRowsRefuseThroughRun(t *testing.T) {
	withHermeticHome(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"unauth", "extra"}, "takes no arguments (got extra) — see `kyacode help`"},
		{[]string{"doctor", "extra"}, "takes no arguments (got extra) — see `kyacode help`"},
		{[]string{"status", "-x"}, "takes no arguments (got -x) — see `kyacode help`"},
		{[]string{"models", "extra"}, "takes no arguments (got extra) — see `kyacode help`"},
		{[]string{"version", "extra"}, "takes no arguments (got extra) — see `kyacode help`"},
		{[]string{"auth", "--stdin", "extra"}, "auth takes no arguments beyond `--stdin` (got 2) — an inline key is refused by design: credentials in argv leak into shell history; see `kyacode help`"},
		{[]string{"gateway", "https://a.example", "https://b.example"}, "gateway takes exactly one url (got 2: https://a.example https://b.example) — see `kyacode help`"},
		{[]string{"launch"}, "usage: kyacode launch <harness> [model] [-- extra args...]"},
	} {
		err := run(tc.args, nil, silentChannels)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: refusal = %v, want the row's exact sentence %q", tc.args, err, tc.want)
		}
	}
}

// The requires-model pre-gate's ORDER is load-bearing and otherwise
// unpinned: the canonical gate in harness.Launch returns the
// byte-identical sentence, so a silently dropped pre-gate would change
// nothing a spawn pin could see — except which refusal wins when the
// store has no token. A hermetic no-token home makes the endpoint error
// "no token stored"; the requires-model refusal must beat it to the
// user. The parse refusals return before resolveEndpoint is touched, so
// run() drives the ordering directly, in-process.
func TestLaunchRequiresModelRefusalBeatsTheEndpoint(t *testing.T) {
	withHermeticHome(t)
	err := run([]string{"launch", "claude"}, nil, silentChannels)
	if err == nil || !strings.Contains(err.Error(), "requires a model") {
		t.Fatalf("launch claude (no model, no token) = %v, want the requires-model refusal", err)
	}
	if strings.Contains(err.Error(), "no token stored") {
		t.Errorf("the endpoint error reached the user before the model refusal — the pre-gate ordering broke: %v", err)
	}
}

// The two parse-side launch refusals beyond the ordering pins: their
// sentences are otherwise unpinned, and they sit exactly in the span a
// future parse extraction would touch — pinning them here decouples
// that risk from the extraction's fate. Both return before
// resolveEndpoint, so no token, stub binary, or gateway double is needed.
func TestLaunchParseRefusalSentencesArePinned(t *testing.T) {
	withHermeticHome(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"launch", "claude", "m1", "extra", "--", "x"}, "unexpected arguments before -- (extra); put harness flags after `--`: kyacode launch claude [model] -- <args...>"},
		{[]string{"launch", "claude", "m1", "--"}, "`--` must be followed by at least one harness argument"},
	}
	for _, tc := range cases {
		err := run(tc.args, nil, silentChannels)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: refusal = %v, want the exact parse sentence %q", tc.args, err, tc.want)
		}
	}
}

// Help-first is a row property the gate honors before anything else:
// the two data movers print usage and run nothing (their action-level
// behavior is pinned at spawn level in main_test.go; here the row is
// what decides).
func TestHelpFirstRowsAreTheDeclaredDataMovers(t *testing.T) {
	withHermeticHome(t)
	for _, args := range [][]string{
		{"auth", "--help"},
		{"auth", "-h"},
		{"gateway", "--help"},
		{"gateway", "-h"},
	} {
		if err := run(args, nil, silentChannels); err != nil {
			t.Errorf("%v: a help-first row must run to nil, got: %v", args, err)
		}
	}
	// The argumentless rows keep --help as the loud refusal — data-loss
	// discipline, never insight.
	for _, args := range [][]string{{"unauth", "--help"}, {"status", "--help"}, {"doctor", "--help"}, {"models", "--help"}, {"version", "--help"}} {
		err := run(args, nil, silentChannels)
		if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("%v: an argumentless row must refuse --help, got: %v", args, err)
		}
	}
}

// The validator rejects every shape of broken declaration this test can
// invent — each row-level rule pin leads to its own rejection sentence.
func TestValidateShapeTableRejectsBrokenDeclarations(t *testing.T) {
	base := append([]argShape(nil), shapeTable...)
	if err := validateShapeTable(base); err != nil {
		t.Fatalf("the shipped table must pass its own validation: %v", err)
	}

	breakRow := func(name string, f func(*argShape)) []argShape {
		rows := append([]argShape(nil), base...)
		for i := range rows {
			if rows[i].name == name {
				f(&rows[i])
				return rows
			}
		}
		t.Fatalf("no row named %q", name)
		return nil
	}

	for _, tc := range []struct {
		label string
		rows  []argShape
		want  string
	}{
		{"empty name", append([]argShape{{maxArgs: 0, leak: echoFree, style: noArgs, tier: tierFailedCommand}}, base...), "no name"},
		{
			"duplicate row", func() []argShape { return append([]argShape{base[0]}, base...) }(),
			"declared twice",
		},
		{"bad tier", breakRow("status", func(r *argShape) { r.tier = tierUsageMisuse }), "tier"},
		{"maxArgs under the sentinel", breakRow("status", func(r *argShape) { r.maxArgs = -2 }), "outside the declared range"},
		{"noArgs over counts", breakRow("status", func(r *argShape) { r.maxArgs = 1 }), "takes zero arguments"},
		{"noArgs credential class", breakRow("status", func(r *argShape) { r.leak = countNever }), "must be echoFree"},
		{"credential mover echoes", breakRow("auth", func(r *argShape) { r.leak = echoFree }), "countNever"},
		{"credential mover unbounded", breakRow("auth", func(r *argShape) { r.maxArgs = 3 }), "declared onlyArg spelling"},
		{"onlyArg without the credential class", breakRow("unauth", func(r *argShape) { r.onlyArg = "--force" }), "credential-bearing data mover"},
		{"helpFirst on an argumentless row", breakRow("unauth", func(r *argShape) { r.helpFirst = true }), "data-mover property"},
		{"exactlyOne over counts", breakRow("gateway", func(r *argShape) { r.maxArgs = 2 }), "bounded at one argument"},
		{"exactlyOne without a noun", breakRow("gateway", func(r *argShape) { r.noun = "" }), "declare what the single argument is"},
		{"unknown style", breakRow("version", func(r *argShape) { r.style = refusalStyle(99) }), "unknown refusal style"},
		{"bodyParsed with a count", breakRow("launch", func(r *argShape) { r.maxArgs = 2 }), "sentinel"},
		{"launch row gone — no bodyParsed left", append([]argShape(nil), base[:6]...), "single bodyParsed row"},
		{
			"one bodyParsed too many", append(append([]argShape(nil), base...),
				argShape{name: "other", maxArgs: shapeSentinelBodyParsed, leak: echoFree, style: bodyParsed, tier: tierFailedCommand}),
			"single bodyParsed row",
		},
	} {
		if err := validateShapeTable(tc.rows); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: validateShapeTable = %v, want a rejection naming %q", tc.label, err, tc.want)
		}
	}
}

// A dispatched name with no declared row is the clipped-declaration
// case: the registration contract refuses to lose it silently.
func TestDeclaredShapePanicsOnUndeclaredDispatch(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("an undeclared dispatch must panic as a registration error, not silently default a shape")
		}
	}()
	declaredShape("nosuchsubcommand")
}

// The dispatch ↔ table correspondence, both directions:
//
//   - every DECLARED row is driven through run(): a switch arm deleted
//     without its row falls through to run()'s unknown-command arm,
//     which exits the whole process at the usage tier — so a missing
//     arm kills this test binary instead of passing quietly;
//   - every DISPATCHED arm (the shipped subcommand set, copied verbatim
//     from run()'s switch — the usage aliases intentionally carry no
//     row) resolves declaredShape: a row deleted without its arm reds
//     here as the registration error it is.
func TestDispatchAndTableAgree(t *testing.T) {
	withHermeticHome(t)

	for _, sh := range shapeTable {
		err := run([]string{sh.name, "extra-arg-for-a-refusal"}, nil, silentChannels)
		_ = err // the arms' individual behavior is pinned per-sentence elsewhere; the claim is only that the arm exists
	}

	assertResolved := func(name string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("dispatched subcommand %q has no declared row: %v — add the row to shapeTable (the registration contract)", name, r)
			}
		}()
		declaredShape(name)
	}
	for _, name := range []string{"auth", "unauth", "gateway", "doctor", "status", "models", "launch", "version"} {
		assertResolved(name)
	}
}
