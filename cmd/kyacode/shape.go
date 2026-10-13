package main

import (
	"fmt"
	"strings"
)

// The subcommand argv contract as declared data (round-2 ticket 03):
// one row per subcommand — how many arguments it takes, which of them
// precede help, whether a shape refusal may echo the offending argv
// back, and the exit tier its refusals map to. The scattered
// hand-rolled checks compose this table; adding a subcommand means
// declaring its shape here, and the gate refuses nothing silently: an
// incoherent or clipped declaration is a registration error at init,
// never a check lost somewhere below the dispatch.
//
// requires-model lives at its two FROZEN sites, not in this table: the
// gate PLACEMENT is frozen (the cmd pre-gate in launch.go and the
// canonical gate in harness.Launch) while the SENTENCE is owned once —
// Spec.RequiresModelRefusal in internal/harness, rendered byte-identical
// at both sites. The launch row below carries the anchor note, never a
// copy of the sentence.

// Exit tiers, declared alongside the table rows and kept exactly as the
// README's integrator table documents:
//
//	tierSuccess      — the subcommand did its job (the only tier a body may return at)
//	tierFailedCommand— any failed subcommand: shape refusals live here
//	tierUsageMisuse  — unknown subcommand or no subcommand at all. ONLY
//	                   the main-level dispatch exits at this tier, and
//	                   it has exactly two sites: the bare-invocation
//	                   argc gate and the dispatch's unknown-command arm.
//	                   A command body never returns (or reaches) it —
//	                   runShape may only decline (tier 1) or help.
const (
	tierSuccess       = 0
	tierFailedCommand = 1
	tierUsageMisuse   = 2
)

// leakClass declares what a declined argv may be printed as.
//
// The echo-policy asymmetry has one reason, stated here and nowhere
// else: auth moves a credential, so its argv may BE the inline key — the
// counting refusal exists because credentials in argv leak into shell
// history, and the error text would be a second leak channel (CI logs,
// pagers). Every other subcommand's declined arguments are printable
// (a url, a stray flag) and are echoed back for the operator's
// diagnosis.
type leakClass int

const (
	echoFree   leakClass = iota // refusals may echo the offending args
	countNever                  // refusals count them, never echo
)

type refusalStyle int

const (
	noArgs        refusalStyle = iota // argumentless: "takes no arguments (got …)"
	authDataLeaks                     // counted, never echoed; names the sole legal arg
	exactlyOne                        // bounded data mover: "takes exactly one url (got N: …)"
	bodyParsed                        // launch: mid-parse guards in the body own the shape
)

// shapeSentinelBodyParsed marks a bodyParsed row: the gate passes the
// argv straight to the body (the parse guards there ARE the shape).
const shapeSentinelBodyParsed = -1

type argShape struct {
	name string
	// maxArgs is refused above this count; shapeSentinelBodyParsed hands
	// the whole rest to the body's own parse.
	maxArgs int
	// onlyArg, when non-empty, is the sole spelling of a single
	// argument (auth's --stdin) — anything else is data-shaped misuse.
	onlyArg string
	// noun names what a exactlyOne row takes one of (gateway: "url").
	// The refusal renders FROM the row — identity literals in the
	// sentence builder would print one subcommand's refusal for
	// whatever future row is first declared with the same style.
	noun string
	// helpFirst: a sole -h/--help prints usage, never the action.
	// Declared only by the data movers: an argumentless row keeps
	// --help as the loud extra-argument refusal — the data-loss
	// discipline (unauth --help must not delete a key silently).
	helpFirst bool
	leak      leakClass
	style     refusalStyle
	tier      int
}

// shapeTable is the registry: one row per dispatched subcommand, in
// declaration order (the review round gives per-row verdicts against
// this list).
var shapeTable = []argShape{
	{
		name: "auth", maxArgs: 1, onlyArg: "--stdin", helpFirst: true,
		leak: countNever, style: authDataLeaks, tier: tierFailedCommand,
	},
	{
		name: "unauth", maxArgs: 0,
		leak: echoFree, style: noArgs, tier: tierFailedCommand,
	},
	{
		name: "gateway", maxArgs: 1, helpFirst: true, noun: "url",
		leak: echoFree, style: exactlyOne, tier: tierFailedCommand,
	},
	{
		name: "doctor", maxArgs: 0,
		leak: echoFree, style: noArgs, tier: tierFailedCommand,
	},
	{
		name: "status", maxArgs: 0,
		leak: echoFree, style: noArgs, tier: tierFailedCommand,
	},
	{
		name: "models", maxArgs: 0,
		leak: echoFree, style: noArgs, tier: tierFailedCommand,
	},
	{
		// launch's argv shape is enforced by the body's mid-parse guards
		// (model boundary, -- passthrough, flag position). Its
		// requires-model sentence stays at BOTH frozen sites — the cmd
		// pre-gate in launch.go and the canonical gate in harness.Launch —
		// rendered from the single owner, Spec.RequiresModelRefusal. This
		// row is the anchor for that pair, not a home for it.
		name: "launch", maxArgs: shapeSentinelBodyParsed,
		leak: echoFree, style: bodyParsed, tier: tierFailedCommand,
	},
	{
		name: "version", maxArgs: 0,
		leak: echoFree, style: noArgs, tier: tierFailedCommand,
	},
}

func init() {
	if err := validateShapeTable(shapeTable); err != nil {
		panic("cmd arg-shape table: " + err.Error()) // registration error: the process must not start half-declared
	}
}

// validateShapeTable is the registration contract: every row is
// individually coherent (style⇔leak pairing, declared bounds, tiers
// within the documented set) and the set as a whole is free of clipped
// or duplicated declarations. BodyParsed stays unique: a second
// body-parsed row would dissolve the table back into scattered checks,
// so the validator keeps that exception declared exactly once.
func validateShapeTable(rows []argShape) error {
	seen := map[string]bool{}
	bodyParsedRows := 0
	for _, sh := range rows {
		if sh.name == "" {
			return fmt.Errorf("a row has no name")
		}
		if seen[sh.name] {
			return fmt.Errorf("%q declared twice", sh.name)
		}
		seen[sh.name] = true

		if sh.tier != tierFailedCommand {
			return fmt.Errorf("%q: tier %d rejected — shape refusals are tier %d (a body cannot manufacture the usage tier)", sh.name, sh.tier, tierFailedCommand)
		}

		if sh.maxArgs < shapeSentinelBodyParsed {
			return fmt.Errorf("%q: maxArgs %d is outside the declared range", sh.name, sh.maxArgs)
		}
		if sh.onlyArg != "" && sh.style != authDataLeaks {
			return fmt.Errorf("%q: a sole legal argument (onlyArg) may only be declared by the credential-bearing data mover", sh.name)
		}
		if sh.helpFirst && sh.maxArgs <= 0 {
			return fmt.Errorf("%q: helpFirst is a data-mover property — an argumentless row keeps --help as the loud extra-argument refusal", sh.name)
		}

		switch sh.style {
		case noArgs:
			if sh.maxArgs != 0 {
				return fmt.Errorf("%q: noArgs row takes zero arguments (declared %d)", sh.name, sh.maxArgs)
			}
			if sh.leak != echoFree {
				return fmt.Errorf("%q: noArgs refusals echo a non-credential argv; leak class must be echoFree", sh.name)
			}
		case authDataLeaks:
			if sh.maxArgs != 1 || sh.onlyArg == "" {
				return fmt.Errorf("%q: the credential-bearing data mover takes exactly one argument of the declared onlyArg spelling", sh.name)
			}
			if sh.leak != countNever {
				return fmt.Errorf("%q: an inline credential in argv makes echoes a second leak channel — leak class must be countNever", sh.name)
			}
		case exactlyOne:
			if sh.maxArgs != 1 {
				return fmt.Errorf("%q: exactlyOne row is bounded at one argument (declared %d)", sh.name, sh.maxArgs)
			}
			if sh.noun == "" {
				return fmt.Errorf("%q: exactlyOne refusals render the row's identity — declare what the single argument is (noun)", sh.name)
			}
			if sh.leak != echoFree {
				return fmt.Errorf("%q: a non-credential argument is printable — leak class must be echoFree", sh.name)
			}
		case bodyParsed:
			if sh.maxArgs != shapeSentinelBodyParsed {
				return fmt.Errorf("%q: bodyParsed rows declare the %d sentinel, not a count", sh.name, shapeSentinelBodyParsed)
			}
			if sh.leak != echoFree {
				return fmt.Errorf("%q: bodyParsed runs no gate echo at all", sh.name)
			}
			bodyParsedRows++
		default:
			return fmt.Errorf("%q: unknown refusal style %d", sh.name, sh.style)
		}
	}
	if bodyParsedRows != 1 || !seen["launch"] {
		return fmt.Errorf("launch must be the single bodyParsed row (found %d)", bodyParsedRows)
	}
	return nil
}

// declaredShape is the lookup the dispatch runs through. A dispatched
// name missing from the table is the clipped-declaration case the
// registration contract refuses to lose silently — a programming error,
// never a degraded run.
func declaredShape(name string) argShape {
	for _, sh := range shapeTable {
		if sh.name == name {
			return sh
		}
	}
	panic("subcommand " + name + " dispatched with no declared arg shape — add its row to shapeTable")
}

// shapeRefusal is the byte-identical sentence per style, rendered FROM
// the row's declaration (name, onlyArg, noun) so a future row declared
// with the same style cannot inherit another subcommand's sentences.
// These are the same sentences the scattered hand-rolled checks printed
// before this table existed — pinned unchanged by the e2e fleet.
func shapeRefusal(sh argShape, rest []string) error {
	switch sh.style {
	case noArgs:
		return fmt.Errorf("takes no arguments (got %s) — see `kyacode help`", strings.Join(rest, " "))
	case authDataLeaks:
		// Count only — never echo the attempted arguments: the refusal
		// exists because inline credentials leak through argv, and the
		// error text would be a second leak channel (CI logs, pagers).
		return fmt.Errorf("%s takes no arguments beyond `%s` (got %d) — an inline key is refused by design: credentials in argv leak into shell history; see `kyacode help`", sh.name, sh.onlyArg, len(rest))
	case exactlyOne:
		return fmt.Errorf("%s takes exactly one %s (got %d: %s) — see `kyacode help`", sh.name, sh.noun, len(rest), strings.Join(rest, " "))
	default:
		return fmt.Errorf("internal: subcommand %q has no shape refusal for style %d", sh.name, sh.style)
	}
}

// shapeOverflow reports whether the rest violates the row's declared
// count/spelling bounds. The onlyArg row refuses a single
// non-matching argument too: inline credentials are refused by design,
// not merely counted badly.
func shapeOverflow(sh argShape, rest []string) bool {
	if sh.style == bodyParsed {
		return false // the body's parse guards own the shape
	}
	if sh.onlyArg != "" {
		return len(rest) > 1 || (len(rest) == 1 && rest[0] != sh.onlyArg)
	}
	return len(rest) > sh.maxArgs
}

// runShape applies a dispatch arm through its declared row: help-first
// where the row says so (usage through the caller's report channels),
// the refusal where the argv overflows, the body only for a clean
// shape.
func runShape(name string, rest []string, ch channels, doer func([]string, channels) error) error {
	sh := declaredShape(name)
	if sh.helpFirst && isSoleHelpArg(rest) {
		usage(ch.stdout)
		return nil
	}
	if shapeOverflow(sh, rest) {
		return shapeRefusal(sh, rest)
	}
	return doer(rest, ch)
}

// isSoleHelpArg reports whether the args are exactly one help flag —
// `-h`/`--help` on a data-moving subcommand (auth, gateway) means
// usage, never a key read or a write.
func isSoleHelpArg(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help")
}
