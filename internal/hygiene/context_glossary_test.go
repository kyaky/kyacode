package hygiene

import (
	"slices"
	"strings"
	"testing"

	"kyacode/internal/harness"
)

// The glossary's carrier-law sentences, pinned against the registry the
// way readme_wiring_test pins README prose. CONTEXT.md is the first text
// a new carrier author reads (it is the project's vocabulary of
// record, and production comments carry its terms), and it rotted within
// hours of the two 2026-10-12 carrier amendments - the fourth encoding of
// carrier law was the only one no test read. Anchors follow the house
// lessons: fact-pins over sentence-pins, verified-unique effect phrases.

// glossaryLine returns the single line of CONTEXT.md containing the
// needle, failing when the anchor is not unique on one line.
func glossaryLine(t *testing.T, text, needle string) string {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("CONTEXT.md anchor %q matched %d lines (want exactly 1) - keep the anchor unique on one line", needle, len(hits))
	}
	return hits[0]
}

// glossaryParagraph returns the whole entry paragraph that starts at the
// bold marker (through the next blank line), so multi-line entries can be
// asserted as one text.
func glossaryParagraph(t *testing.T, text, marker string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("CONTEXT.md lost the entry marker %q", marker)
	}
	var out []string
	for i := start; i < len(lines); i++ {
		if i > start && strings.TrimSpace(lines[i]) == "" {
			break
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

func TestContextGlossaryNamesEveryRegisteredHarness(t *testing.T) {
	text := string(repoFile(t, "CONTEXT.md"))
	line := glossaryLine(t, text, "An external coding-agent CLI that kyacode launches")
	open := strings.Index(line, "(")
	close := strings.LastIndex(line, ")")
	if open < 0 || close < open {
		t.Fatalf("the Harness glossary entry lost its name parenthesis: %q", line)
	}
	got := parseNameList(t, line[open+1:close])
	want := registeredNames(func(harness.Spec) bool { return true })
	if !slices.Equal(got, want) {
		t.Errorf("CONTEXT.md harness list = %v, want the registered set %v", got, want)
	}
}

func TestContextGlossaryCarrierEntryMatchesTheCarrierContract(t *testing.T) {
	text := string(repoFile(t, "CONTEXT.md"))
	entry := glossaryParagraph(t, text, "**Catalog carrier**")
	// The removed registration ban must stay gone: its tombstone lives at
	// registry.go ("recorded here so it is not re-added reflexively") and
	// the glossary re-teaching it re-arms the reflex.
	for _, stale := range []string{"is a registration error", "single-model-on-the-wire"} {
		if strings.Contains(entry, stale) {
			t.Errorf("the Catalog carrier glossary entry re-teaches the removed ban (%q) - registry.go records the removal deliberately", stale)
		}
	}
	// The carrier-body contract must be stated.
	for _, want := range []string{"must serve every catalog row", "carrier-body property"} {
		if !strings.Contains(entry, want) {
			t.Errorf("the Catalog carrier glossary entry lost the contract phrase %q", want)
		}
	}
	// Membership is not placement: the carriers that render their entry
	// model through their own spawn-time wiring are named as such (derived
	// from the registry - both facts the glossary asserts are Spec data).
	for _, name := range registeredNames(func(s harness.Spec) bool { return s.IsCarrier() && s.RequiresModel }) {
		if !strings.Contains(entry, name) {
			t.Errorf("the Catalog carrier glossary entry omits the entry-model carrier %q", name)
		}
	}
}

func TestContextGlossaryPlacementListMatchesTheHeldSplit(t *testing.T) {
	text := string(repoFile(t, "CONTEXT.md"))
	entry := glossaryParagraph(t, text, "**Catalog decision**")
	// The pin-FORM enumeration, pinned deliberately: the placement decision
	// is not derivable from Spec data alone (Runners does not expose pin
	// consumption), so the groups ride a labelled expectation table - the
	// readme_wiring_test idiom for held decisions. The CLAUSES are pinned
	// as substrings, not bare names: presence-only pins survived a swap of
	// pi and openclaw between the bare and scoped groups when mutation-
	// tested (2026-10-13 review). claude and codex are placement facts:
	// they consume no pin, their entry model rides their own spawn-time
	// wiring.
	for _, clause := range []string{
		"pi, dsh and droid pin bare",
		"omp and openclaw pin scoped",
		"claude and codex read no pin — their entry model rides their own spawn-time wiring",
		"opencode reads no pin — held",
	} {
		if !strings.Contains(entry, clause) {
			t.Errorf("the Catalog decision placement enumeration lost the clause %q", clause)
		}
	}
}
