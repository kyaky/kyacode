package hygiene

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"kyacode/internal/config"
	"kyacode/internal/gateway"
	"kyacode/internal/harness"
)

// The README's wiring prose rides the registry, not a hand-maintained
// copy: these pins read the README's claims back against the declared
// Spec facts (the readme_doctor_test.go house style — prose asserted
// against the module's own semantics). A Spec change that outdates a
// sentence goes red at go test instead of in a user's confusion. The
// anchors are exact substrings: a moved anchor fails here first, and the
// reviewer updates the pin deliberately together with the sentence.

// readmeText reads the README with CRLF checkout noise normalized: the
// Windows runner's git ships core.autocrlf=true, so the stored LF bytes
// land in the worktree as CRLF and the line-slicing pins below reason in
// LF. Anchors that live mid-line are unaffected, but line-END assertions
// (a HasSuffix on the last word of a wrapped line) are not.
func readmeText(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(string(repoFile(t, "README.md")), "\r\n", "\n")
}

// readmeLine returns the one README line containing marker (whole-line
// anchors keep the slices line-stable).
func readmeLine(t *testing.T, marker string) string {
	t.Helper()
	text := readmeText(t)
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("README lost the anchor %q — update this pin deliberately together with the sentence", marker)
	return ""
}

// parseNameList splits a README name enumeration ("a, b and c" or
// `a`, `b` and `c`) into sorted names.
func parseNameList(t *testing.T, s string) []string {
	t.Helper()
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, " and ", ", ")
	var out []string
	for _, part := range strings.Split(s, ", ") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	slices.Sort(out)
	return out
}

func registeredNames(pred func(harness.Spec) bool) []string {
	var out []string
	for _, s := range harness.Runners() {
		if pred(s) {
			out = append(out, s.Name)
		}
	}
	slices.Sort(out)
	return out
}

// TestReadmeAutoInstallExceptionsAreTheNoInstallerRunners pins the
// auto-install sentence's exception list against the registry: the
// harnesses you must install yourself are exactly the Specs whose
// Install is nil (availability IS the declaration). The clause spans a
// wrapped line, so the anchors slice the whole text, not one line.
func TestReadmeAutoInstallExceptionsAreTheNoInstallerRunners(t *testing.T) {
	text := readmeText(t)
	i := strings.Index(text, "except `")
	if i < 0 {
		t.Fatalf("README lost the auto-install `except` clause — update this pin deliberately together with the sentence")
	}
	j := strings.Index(text[i:], ", which you must")
	if j < 0 {
		t.Fatalf("README lost the auto-install sentence's tail — update this pin deliberately together with the sentence")
	}
	got := parseNameList(t, text[i+len("except "):i+j])
	want := registeredNames(func(s harness.Spec) bool { return s.Install == nil })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("README auto-install exceptions = %v, want the no-Install set %v", got, want)
	}
}

// TestReadmeCatalogCarriersAreTheDeclaredCarriers pins the catalog
// paragraph's fetcher list against the registry: the runners named as
// fetching the gateway's full model catalog are exactly the Specs with a
// declared carrier channel. The list closes the line ABOVE the catalog
// phrase (the sentence wraps), so the anchor is the phrase and the list
// is read off the preceding line.
func TestReadmeCatalogCarriersAreTheDeclaredCarriers(t *testing.T) {
	lines := strings.Split(readmeText(t), "\n")
	idx := slices.IndexFunc(lines, func(l string) bool {
		return strings.Contains(l, "the gateway's full model catalog at launch")
	})
	if idx < 1 {
		t.Fatalf("README lost the catalog sentence — update this pin deliberately together with the sentence")
	}
	list := lines[idx-1]
	if !strings.HasSuffix(list, " fetch") {
		t.Fatalf("the carrier list line changed shape: %q", list)
	}
	got := parseNameList(t, strings.TrimSuffix(list, " fetch"))
	want := registeredNames(func(s harness.Spec) bool { return s.IsCarrier() })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("README catalog carriers = %v, want the declared carrier set %v", got, want)
	}
}

// TestReadmePinSplitMatchesTheHeldDecision pins the catalog paragraph's
// pin/no-pin split: the names claiming to pin the first real id plus the
// name(s) claiming never to write a default must together be exactly the
// declared carrier set, disjoint. The no-pin half is a DELIBERATE
// expectation table, not a derivation: opencode's no-pin is the held
// decision (the routing is not behavior-neutral — opencode boots on the
// SORT-winner), invisible to Spec data by design.
func TestReadmePinSplitMatchesTheHeldDecision(t *testing.T) {
	pinLine := readmeLine(t, "pin the first real")
	i := strings.Index(pinLine, "with no model given, ")
	if i < 0 {
		t.Fatalf("the pinning sentence lost its clause: %q", pinLine)
	}
	j := strings.Index(pinLine, " pin the first real")
	pinners := parseNameList(t, pinLine[i+len("with no model given, "):j])

	noPinLine := readmeLine(t, "never write a default")
	k := strings.LastIndex(noPinLine, "(")
	if k < 0 {
		t.Fatalf("the no-pin sentence lost its parenthesis: %q", noPinLine)
	}
	m := strings.Index(noPinLine, " never write a default")
	noPin := parseNameList(t, noPinLine[k+1:m])

	// The held decision, pinned deliberately (research §7): opencode
	// never writes a default. codex and claude join the no-pin half by
	// their own shape — a model-less launch is refused outright
	// (RequiresModel), so there is never a fetched-catalog default to
	// write; the typed model rides the spawn-time wiring (codex's -c
	// pin, claude's ANTHROPIC_MODEL) and the catalog's first row is only
	// the picker's own default.
	wantNoPin := []string{"claude", "codex", "opencode"}
	if !reflect.DeepEqual(noPin, wantNoPin) {
		t.Errorf("README no-default exception = %v, want the held decision %v (research §7)", noPin, wantNoPin)
	}

	var got []string
	got = append(got, pinners...)
	got = append(got, noPin...)
	slices.Sort(got)
	want := registeredNames(func(s harness.Spec) bool { return s.IsCarrier() })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("README pin split (%v + %v) = %v, want exactly the declared carrier set %v", pinners, noPin, got, want)
	}
}

// TestReadmeScopedSetIsTheDeclaredComposers pins the bare-model-id
// sentence against the registry: the runners named as taking a BARE
// model id are exactly the declared ScopedComposer facts.
func TestReadmeScopedSetIsTheDeclaredComposers(t *testing.T) {
	line := readmeLine(t, "take a BARE model id")
	i := strings.Index(line, " take a BARE model id")
	// The named set opens the sentence; walk back to the line's start.
	got := parseNameList(t, line[:i])
	want := registeredNames(func(s harness.Spec) bool { return s.ScopedComposer })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("README bare-model-id set = %v, want the declared ScopedComposer set %v", got, want)
	}
}

// TestReadmeWiringTableMatchesTheDeclaredClasses pins the wiring table
// against the registry: every row names a registered runner whose
// declared wiring class matches the row's env/sandbox and persisted
// columns, and no registered runner lacks a row. The per-row VAR names
// (ANTHROPIC_BASE_URL, CODEX_HOME, ...) stay the wire tests' truth —
// this pin is the class map only.
func TestReadmeWiringTableMatchesTheDeclaredClasses(t *testing.T) {
	text := readmeText(t)
	var rows []struct {
		name      string
		wiring    string
		persisted string
	}
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "| harness"):
			inTable = true
		case inTable && strings.HasPrefix(line, "|---"):
			// separator row
		case inTable && strings.HasPrefix(line, "|"):
			cols := strings.Split(strings.Trim(line, "|"), "|")
			if len(cols) != 3 {
				t.Fatalf("wiring table row %q does not have three columns", line)
			}
			rows = append(rows, struct {
				name      string
				wiring    string
				persisted string
			}{strings.TrimSpace(cols[0]), strings.TrimSpace(cols[1]), strings.TrimSpace(cols[2])})
		case inTable && !strings.HasPrefix(line, "|"):
			inTable = false
		}
	}
	byName := map[string]harness.Spec{}
	for _, s := range harness.Runners() {
		byName[s.Name] = s
	}
	seen := map[string]bool{}
	for _, r := range rows {
		s, ok := byName[r.name]
		if !ok {
			t.Errorf("wiring table row %q names no registered runner", r.name)
			continue
		}
		seen[r.name] = true
		wantEnv := strings.HasPrefix(r.wiring, "env:")
		if wantEnv != (s.Wiring == harness.WiringEnv) {
			t.Errorf("%s: README wiring %q vs declared class %v", r.name, r.wiring, s.Wiring)
		}
		wantPersisted := "yes"
		if wantEnv {
			wantPersisted = "no"
		}
		if r.persisted != wantPersisted {
			t.Errorf("%s: README persisted = %q, want %q for the declared class", r.name, r.persisted, wantPersisted)
		}
	}
	for name := range byName {
		if !seen[name] {
			t.Errorf("registered runner %q has no wiring-table row; the table and the registry must agree", name)
		}
	}
}

// TestReadmeModelsRowRendersTheOwnedTimeout pins the commands table's
// models row against the gateway module's owned constant: the README
// NAMES the budget in prose, so it renders the same value the usage text
// renders (gateway.RequestTimeout) — a constant change goes red here
// instead of leaving a stale "10s" behind.
func TestReadmeModelsRowRendersTheOwnedTimeout(t *testing.T) {
	line := readmeLine(t, "list gateway models")
	want := "list gateway models (" + gateway.RequestTimeout.String() + " timeout)"
	if !strings.Contains(line, want) {
		t.Errorf("README models row = %q, want it to render the owned timeout (%q)", line, want)
	}
}

// TestReadmeKeyChannelAndHomeRowsMatchTheDeclaredSets pins the two
// remaining wiring sentences against held-decision expectation tables:
// (i) the credential-env-export set — the runners whose declared channel
// emits the KYA_API_KEY row (EnvRef/Dual) — and (ii) the HOME-replacer
// set. Both are DELIBERATE expectation tables, not derivations: the
// channel is declared at the buildSandbox call sites, not on the Spec
// (the upgrade trigger is a declared field, which upgrades table ->
// derivation in the same commit). Corroboration: wire_test.go's
// wantRedirect exact-row map and the keyInEnv column; hermes/omp's
// zero-row pins; dsh's frozen env. The anchors are verified unique in
// README.md ("additionally export", "Rows that replace"), so the
// README:138 anchor trap does not apply here.
func TestReadmeKeyChannelAndHomeRowsMatchTheDeclaredSets(t *testing.T) {
	// (i) the env-export five: codex, droid, dsh, openclaw, pi.
	exportLine := readmeLine(t, "additionally export")
	i := strings.Index(exportLine, "Five sandbox rows — ")
	if i < 0 {
		t.Fatalf("the env-export sentence lost its named set: %q", exportLine)
	}
	j := strings.Index(exportLine[i:], " — additionally export")
	got := parseNameList(t, exportLine[i+len("Five sandbox rows — "):i+j])
	wantExport := []string{"codex", "droid", "dsh", "openclaw", "pi"}
	if !reflect.DeepEqual(got, wantExport) {
		t.Errorf("README env-export set = %v, want %v (the EnvRef/Dual runners' declared channels)", got, wantExport)
	}

	// (ii) the HOME-replacer three: droid, pi, openclaw.
	homeLine := readmeLine(t, "Rows that replace")
	k := strings.Index(homeLine, "Rows that replace `HOME` (")
	if k < 0 {
		t.Fatalf("the HOME sentence lost its named set: %q", homeLine)
	}
	m := strings.Index(homeLine, ") redirect")
	gotHome := parseNameList(t, homeLine[k+len("Rows that replace `HOME` ("):m])
	wantHome := []string{"droid", "openclaw", "pi"} // parseNameList sorts; keep the expectation in the same order
	if !reflect.DeepEqual(gotHome, wantHome) {
		t.Errorf("README HOME-replacer set = %v, want %v (corroborated by wire_test's wantRedirect exact-row map)", gotHome, wantHome)
	}
}

// TestReadmeEnvTableRendersTheOwnedNames pins the Environment-variables
// table against config's owned constants. The anchors are the rows'
// unique EFFECT tails, deliberately NOT the variable names: readmeLine's
// first-contains semantics would bind a name marker to README's command
// table (the gateway row mentions KYA_GATEWAY_INSECURE, which contains
// KYA_GATEWAY as a substring) — an anchor trap that would false-pass.
func TestReadmeEnvTableRendersTheOwnedNames(t *testing.T) {
	line := readmeLine(t, "wins over config.json")
	if !strings.Contains(line, "`"+config.EnvGateway+"`") {
		t.Errorf("README env row %q must name the owned constant config.EnvGateway (%q)", line, config.EnvGateway)
	}
	line = readmeLine(t, "loopback hosts only")
	if !strings.Contains(line, "`"+config.EnvGatewayInsecure+"`") {
		t.Errorf("README env row %q must name the owned constant config.EnvGatewayInsecure (%q)", line, config.EnvGatewayInsecure)
	}
}

// TestReadmeExitCodeTablePinsTheThreeTiers pins the exit-code table's
// shape against the documented contract: exactly three tiers, 0/1/2 in
// order, with the tier-2 row naming the usage-shaped misuse (the
// behavior itself is pinned spawn-side in main_test; this pin keeps the
// prose contract from drifting away from what the tiers mean).
func TestReadmeExitCodeTablePinsTheThreeTiers(t *testing.T) {
	text := readmeText(t)
	var tiers []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cols := strings.Split(strings.Trim(line, "| "), "`")
		if len(cols) >= 2 && len(cols[1]) == 1 && cols[1] >= "0" && cols[1] <= "9" {
			tiers = append(tiers, cols[1])
		}
	}
	if len(tiers) != 3 || tiers[0] != "0" || tiers[1] != "1" || tiers[2] != "2" {
		t.Fatalf("README exit-code table tiers = %v, want exactly [0 1 2] in order", tiers)
	}
	tier2 := readmeLine(t, "unknown subcommand or no subcommand at all")
	if !strings.Contains(tier2, "`2`") {
		t.Errorf("the usage-misuse row %q must be tier 2", tier2)
	}
}
