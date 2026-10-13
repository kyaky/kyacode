package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kyacode/internal/harness/sandbox"
)

// eqEnv compares env-shaped expectations positionally: for the child env
// the ORDER carries the contract (the credential row is frozen first),
// including "nothing follows" — a pseudo-var smuggled after the rows the
// runner legitimately emits must fail this pin. Siblings with join-and-
// contains expectations keep their shape; argv and env use positional
// equality. Built alongside Wire (ticket 03) — slices like dsh's env are
// fully determined by the interface, so the scan-and-extract style gives
// way to exact wants.
func eqEnv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env = %v, want %v (differ at %d)", got, want, i)
		}
	}
}

// TestDSHBuildEnvDeliversPatchRedirectAsArgs pins the stale-transport bug
// class death at the new interface: the `web --patch` flag pair dsh needs
// is a RETURN VALUE on the Wire (a fact of the launch's own sandbox),
// never an env-var smuggle and never package state (the stale-path leak
// dsh.go documents). The env is the credential row alone: no pseudo-var
// may ride next to it — asserting the env's FULL shape is what catches a
// smuggled row.
func TestDSHBuildEnvDeliversPatchRedirectAsArgs(t *testing.T) {
	home := withHome(t)
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}

	w, err := dshSpec.CarrierBuild(ep, "", degradedCatalog())
	if err != nil {
		t.Fatalf("CarrierBuild: %v", err)
	}
	defer w.Cleanup()

	// The patch pair names a file INSIDE this launch's own sandbox — the
	// path the wrapper wrote, returned, not re-translated by anyone.
	want := []string{"web", "--patch", filepath.Join(w.Root, "kya.cordis.yml")}
	eqArgs(t, w.Args, want)
	if fi, err := os.Stat(w.Args[2]); err != nil || fi.IsDir() {
		t.Errorf("patch arg %q does not name an existing file: %v", w.Args[2], err)
	}

	// The env is the credential row alone — asserting the full ordered
	// shape fails on any smuggled pseudo-var row (the env-slice carrier
	// the older revisions used) — the child env's shape is a frozen
	// contract, changed ONLY by that transport row's deletion.
	eqEnv(t, w.Env, []string{sandbox.KeyEnvName + "=" + ep.Token})

	// The recorded root is real state: inside the isolated home and
	// holding the files this launch just wrote.
	if w.Root == "" || !testDirWithin(home, w.Root) {
		t.Fatalf("wire root %q is not a dir inside the isolated home %s", w.Root, home)
	}
	if fi, err := os.Stat(filepath.Join(w.Root, "settings.yaml")); err != nil || fi.IsDir() {
		t.Errorf("sandbox root lacks the settings file the wiring wrote: %v", err)
	}
}

// TestSequentialLaunchesNeverTradePatchPaths pins the historical
// stale-path bug class (a package-global once carried a previous launch's
// sandbox path into a later launch) at the new interface: two live
// launches must each reference ONLY their own sandbox — in env and in
// args — and the two sandboxes must both exist at once.
func TestSequentialLaunchesNeverTradePatchPaths(t *testing.T) {
	withHome(t)
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}

	w1, err := dshSpec.CarrierBuild(ep, "", degradedCatalog())
	if err != nil {
		t.Fatalf("launch 1 CarrierBuild: %v", err)
	}
	defer w1.Cleanup()
	w2, err := dshSpec.CarrierBuild(ep, "", degradedCatalog())
	if err != nil {
		t.Fatalf("launch 2 CarrierBuild: %v", err)
	}
	defer w2.Cleanup()

	// Distinct live roots: MkdirTemp refuses to clobber, so one shared
	// root would mean the lifecycle reclaimed the other launch's wiring
	// underneath it.
	if w1.Root == w2.Root {
		t.Fatalf("two live launches share one sandbox root %q — per-launch state is not per-launch", w1.Root)
	}

	eqArgs(t, w1.Args, []string{"web", "--patch", filepath.Join(w1.Root, "kya.cordis.yml")})
	eqArgs(t, w2.Args, []string{"web", "--patch", filepath.Join(w2.Root, "kya.cordis.yml")})

	// Neither wire may mention the other launch anywhere: the other side's
	// root in env rows or args is exactly the stale-word leak.
	for i, w := range []Wire{w1, w2} {
		other := w2.Root
		if i == 1 {
			other = w1.Root
		}
		joined := strings.Join(w.Env, "\n") + "\n" + strings.Join(w.Args, "\n")
		if strings.Contains(joined, other) {
			t.Errorf("launch %d carries a foreign root (%s) in env/args: %v // %v", i+1, other, w.Env, w.Args)
		}
	}

	// Both sandboxes exist while both launches are live.
	for _, w := range []Wire{w1, w2} {
		if fi, err := os.Stat(w.Args[2]); err != nil || fi.IsDir() {
			t.Errorf("live launch's patch file %q missing: %v", w.Args[2], err)
		}
	}
}

// TestLaunchersWiresCarryTheLaunchFacts pins the widened handover for
// EVERY registered sandbox-wired spec: a non-empty root inside the
// isolated home, a non-nil idempotent cleanup that reclaims the dir, and
// the root naming the declared sandbox prefix.
func TestLaunchersWiresCarryTheLaunchFacts(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	for _, s := range sandboxRunners() {
		t.Run(s.Name, func(t *testing.T) {
			home := withHome(t)
			w := buildWire(t, s, ep, "")
			if w.Root == "" || !testDirWithin(home, w.Root) {
				t.Fatalf("wire root %q is not a dir inside the isolated home %s", w.Root, home)
			}
			if !strings.HasPrefix(filepath.Base(w.Root), s.SandboxPrefix+"-") {
				t.Errorf("root %q does not carry the declared prefix %q", w.Root, s.SandboxPrefix)
			}
			if w.Cleanup == nil {
				t.Fatal("wire carries no cleanup — the launch boundary has nothing to reclaim")
			}
			// The redirect row, registry-wide exact-row rule: the env var
			// each runner points the harness at must carry exactly this
			// launch's own wire root — a dropped or wrong-valued row
			// redirects the child at the real user config. The table is
			// test truth (extend it when adding a runner); derivation was
			// what the ticket killed. dsh is the deliberate exception: its
			// env carries the credential row alone (frozen full shape,
			// TestDSHBuildEnvDeliversPatchRedirectAsArgs) and the root
			// rides the Wire.
			if s.Name != "dsh" {
				wantRedirect := map[string]string{
					"codex":    "CODEX_HOME=" + w.Root,
					"cline":    "CLINE_DATA_DIR=" + w.Root,
					"droid":    "HOME=" + w.Root,
					"hermes":   "HERMES_HOME=" + w.Root,
					"omp":      "PI_CODING_AGENT_DIR=" + filepath.Join(w.Root, "agent"),
					"pi":       "HOME=" + w.Root,
					"openclaw": "HOME=" + w.Root,
				}
				row, declared := wantRedirect[s.Name]
				if !declared {
					t.Fatalf("sandbox runner %s has no expected redirect row; extend the table when adding a runner", s.Name)
				}
				if !slices.Contains(w.Env, row) {
					t.Errorf("%s: env %v, want the exact redirect row %q", s.Name, w.Env, row)
				}
			}
			w.Cleanup()
			if _, err := os.Stat(w.Root); !os.IsNotExist(err) {
				t.Errorf("cleanup must remove the sandbox dir; stat err = %v", err)
			}
		})
	}
}

// TestLaunchersWireArgsAreTheRunnerOwnArgv pins, per real sandbox runner,
// that the wire's args ARE the runner's own argv: before the widened
// interface the launch boundary derived them from ExtraArgs at a single
// site no runner could forget; since the Wire each BuildEnv body must
// re-carry them. The literal table is the independent pre-change
// baseline, so a wiring that drops its model flags (child boots
// unconfigured) or composes a second scope layer fails loudly.
func TestLaunchersWireArgsAreTheRunnerOwnArgv(t *testing.T) {
	ep := Endpoint{BaseURL: "https://127.0.0.1:1", Token: "sk-test-12345"}
	model := "m1"
	// One stub for the whole table: the codex row's carrier build harvests
	// the instruction template through the seam, and the real harvest
	// spawns the installed codex — hermetic tests must not.
	stubCodexHarvest(t)
	for _, s := range sandboxRunners() {
		t.Run(s.Name, func(t *testing.T) {
			withHome(t)
			w := buildWire(t, s, ep, model)
			// dsh's argv is its patch pair, built from the wire's own root —
			// the one deliberate departure from the model-args rule, pinned
			// to that shape.
			if s.Name == "dsh" {
				eqArgs(t, w.Args, []string{"web", "--patch", filepath.Join(w.Root, "kya.cordis.yml")})
				return
			}
			want := map[string][]string{
				// codex's argv is the runtime-layer `-c` pin set: codex
				// 0.162's project config layers (trusted cwd/tree/repo
				// .codex/config.toml) sit ABOVE the sandbox's user layer
				// and replaced the wired model in the field, so every
				// wiring fact also rides the override layer that outranks
				// them (codexWiringPairs) — plus the model_catalog_json pin
				// serving the /model picker the gateway's own rows. The
				// hand-spelled baseline lives in wantCodexWiring.
				"codex":    codexWantArgs(model, filepath.Join(w.Root, "models.json")),
				"droid":    {"-m", "m1"},
				"cline":    {"-m", "m1"},
				"hermes":   {"--model", "m1"},
				"omp":      {"--model", "kya/m1"},
				"openclaw": {"--model", "kya/m1"},
				"pi":       {"--model", "kya/m1"},
			}
			rows, declared := want[s.Name]
			if !declared {
				t.Fatalf("sandbox runner %s has no expected wire-args row; extend the table when adding a runner", s.Name)
			}
			eqArgs(t, w.Args, rows)
		})
	}
}

// TestLaunchComposesWireArgsThenExtra pins the ONE merge site: the child
// argv is the wire's args followed by the CLI's extra args — nothing
// else, nothing reordered, and ExtraArgs is not consulted on the launcher
// path (the wire is the launcher's single argv channel).
func TestLaunchComposesWireArgsThenExtra(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "argv.txt")
	fake := composeSpec("KYA_ARGV_DUMP", dump)
	// Positional extras: the child is this test's own binary, whose flag
	// parser exits 2 on unknown --flags — passthrough shape, not content,
	// is what the pin carries.
	extra := []string{"E1", "E2"}
	if err := Launch(fake, Endpoint{}, "", extra, nil, nil); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("child argv sentinel never dumped: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"-test.run", "^TestArgvChildEntry$", "E1", "E2"}
	eqArgs(t, got, want)
}

// TestEnvRowsReachTheChildFromTheWire rides the same sentinel child one
// step further: the wire's env rows must be exactly the injected rows on
// top of the ambient process env — the launch boundary injects, children
// inherit.
func TestEnvRowsReachTheChildFromTheWire(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "env.txt")
	fake := composeSpec("KYA_ENV_DUMP", dump)
	if err := Launch(fake, Endpoint{}, "", nil, nil, nil); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("child env sentinel never dumped: %v", err)
	}
	env := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !slices.Contains(env, "KYA_ENV_DUMP="+dump) {
		t.Errorf("child env missing the wire's injected row %q", "KYA_ENV_DUMP="+dump)
	}
}

// composeSpec is the fake sandbox spec for the composition pins: a zero
// sandbox (no-op cleanup), a wire carrying the sentinel's flags as args
// and the sentinel's dump path as an env row. A declared Spec value —
// Launch consumes spec data, so the double needs no registry presence.
func composeSpec(dumpEnvRow, dumpPath string) Spec {
	return Spec{
		Name:          "compose-fake",
		Wiring:        WiringSandbox,
		SandboxPrefix: "compose-fake",
		Find: func() (string, bool) {
			bin, err := os.Executable()
			return bin, err == nil
		},
		BuildEnv: func(Endpoint, string) (Wire, error) {
			env := []string{}
			if dumpPath != "" {
				env = append(env, dumpEnvRow+"="+dumpPath)
			}
			return Wire{
				Env:     env,
				Args:    []string{"-test.run", "^TestArgvChildEntry$"},
				Cleanup: func() {},
			}, nil
		},
	}
}

// TestArgvChildEntry is the -test.run target used by the composition pins:
// as a launch child it dumps this process's argv (everything after the
// binary) and env to the files the fake launcher wired. Run standalone it
// does nothing.
func TestArgvChildEntry(t *testing.T) {
	if dump := os.Getenv("KYA_ARGV_DUMP"); dump != "" {
		if err := os.WriteFile(dump, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			t.Fatalf("child argv dump: %v", err)
		}
	}
	if dump := os.Getenv("KYA_ENV_DUMP"); dump != "" {
		if err := os.WriteFile(dump, []byte(strings.Join(os.Environ(), "\n")), 0o600); err != nil {
			t.Fatalf("child env dump: %v", err)
		}
	}
}
