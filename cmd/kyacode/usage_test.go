package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"kyacode/internal/config"
	"kyacode/internal/harness"
)

// TestUsageRendersTheDeclaredScopedSet pins the usage DERIVATION, not
// the bytes: the BARE-model-id sentence renders from the registry's
// declared ScopedComposer facts, so a Spec change flows into the usage
// text instead of drifting from a hand-maintained copy. The rendered
// names are parsed back and compared to the registry — the same
// prose-against-facts pattern the hygiene README pins use, one seam
// earlier. (A full byte-pin would re-introduce the hand copy this
// rendering exists to delete.)
func TestUsageRendersTheDeclaredScopedSet(t *testing.T) {
	var out strings.Builder
	usage(&out)
	text := out.String()
	i := strings.Index(text, "Harnesses: ")
	if i < 0 {
		t.Fatal("usage lost the harness list")
	}
	j := strings.Index(text[i:], " take a BARE model id")
	if j < 0 {
		t.Fatal("usage lost the BARE-model-id sentence")
	}
	between := text[i+len("Harnesses: ") : i+j]
	// The names ride the sentence's own line — the tail of `between`
	// after its last newline, trimmed of the indent.
	k := strings.LastIndex(between, "\n")
	if k < 0 {
		t.Fatalf("usage BARE sentence lost its own line: %q", between)
	}
	sentence := strings.TrimSpace(between[k+1:])
	got := strings.Split(strings.ReplaceAll(sentence, " and ", ", "), ", ")
	var want []string
	for _, s := range harness.Runners() {
		if s.ScopedComposer {
			want = append(want, s.Name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("usage BARE sentence names %v, want the declared ScopedComposer set %v", got, want)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Errorf("usage BARE sentence names %v, want %v (registry order)", got, want)
			break
		}
	}
}

// TestUsageRendersTheOwnedEnvNames extends the derivation doctrine to the
// Environment block: the two variable names render from config's owned
// constants, so a rename flows into the usage text instead of stranding
// it (the same decision that made the timeout and the scoped set render).
func TestUsageRendersTheOwnedEnvNames(t *testing.T) {
	var out strings.Builder
	usage(&out)
	text := out.String()
	i := strings.Index(text, "Environment:")
	if i < 0 {
		t.Fatal("usage lost the Environment block")
	}
	env := text[i:]
	for _, name := range []string{config.EnvGateway, config.EnvGatewayInsecure} {
		if !strings.Contains(env, name) {
			t.Errorf("usage Environment block missing the owned env name %q", name)
		}
	}
}

// TestUsageCommandsMatchTheShapeTable pins the command set's third
// surface: every shapeTable row is named in usage(), and usage's single
// extra name is `help` — the declared usage affordance that carries no
// row by design (the dispatch comment in main.go). The README commands
// table is pinned to the same set by the sibling test below.
func TestUsageCommandsMatchTheShapeTable(t *testing.T) {
	var out strings.Builder
	usage(&out)
	text := out.String()
	i := strings.Index(text, "Usage:")
	j := strings.Index(text, "Harnesses:")
	if i < 0 || j < 0 || j < i {
		t.Fatal("usage lost its Usage block")
	}
	var got []string
	for _, m := range regexp.MustCompile(`kyacode ([a-z]+)`).FindAllStringSubmatch(text[i:j], -1) {
		got = append(got, m[1])
	}
	slices.Sort(got)
	want := shapeTableRowNames()
	want = append(want, "help") // the declared usage affordance: carries no row by design
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("usage commands = %v, want the shapeTable set plus the help affordance %v", got, want)
	}
}

// TestReadmeCommandsMatchTheShapeTable closes the reverse drift the
// dispatch/table agreement cannot see: the README commands table must
// name exactly the shapeTable set plus the help affordance. shapeTable
// is unexported, so this pin lives in package main with a module-root
// walk to reach the README.
func TestReadmeCommandsMatchTheShapeTable(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "| `kyacode ") {
			continue
		}
		m := regexp.MustCompile("`kyacode ([a-z]+)").FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("README commands row %q has no kyacode <name> token", line)
		}
		got = append(got, m[1])
	}
	slices.Sort(got)
	want := shapeTableRowNames()
	want = append(want, "help") // README:144 promises the help affordance
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("README commands table = %v, want the shapeTable set plus help %v", got, want)
	}
}

// shapeTableRowNames collects the dispatched subcommand names from the
// declared table (the same nilness-data read the wiring matrix makes).
func shapeTableRowNames() []string {
	var out []string
	for _, r := range shapeTable {
		out = append(out, r.name)
	}
	return out
}
