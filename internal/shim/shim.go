// Package shim is the engine side of the codex GUI wiring: the SAME
// kyacode binary installed under a second name, answering the Codex IDE
// extension's engine spawn instead of the CLI dispatch.
//
// Why a shim exists at all: the official extension bundles its own codex
// engine and offers exactly one override — the chatgpt.cliExecutable
// setting, application-scoped and documented development-only — and no
// CODEX_HOME or environment setting at all. But the extension spreads
// process.env into the engine it spawns, so a binary that injects the
// wiring ITSELF needs no editor-side env cooperation: the user points
// chatgpt.cliExecutable at our installed copy once, and every spawn the
// extension makes lands here with the passthrough argv of a real codex.
// The shim then supplies what kyacode's launcher would have: the
// persistent GUI home as CODEX_HOME and the gateway key as KYA_API_KEY,
// both resolved at spawn time (the token's single source of truth stays
// ~/.kyacode/token, so `kyacode unauth` and a key rotation take effect on
// the next spawn without touching the editor).
//
// Everything the shim spawns is wired, never written: no -c pins are
// synthesized here because the GUI home's config.toml already carries
// every wiring fact, and upstream denies project layers the ability to
// override model_provider/model_providers (verified in the codex-rs
// config loader) — the one file layer is authoritative. The home holds
// NO static key (no auth.json; verified empirically that the app-server
// path needs none with an env_key provider), so a persistent home is a
// state dir, not a credential store, and the "throwaway homes only"
// doctrine extends to it without a key-at-rest exception.
//
// Two ownership mechanisms make the resident engine safe:
//
//   - The app-server lease (sandbox.AcquireLease) is held for the
//     child's whole lifetime: a second editor window spawning a second
//     engine against the same home is refused loudly (upstream #41085
//     contention class) instead of corrupting it.
//   - On Windows the child is assigned to a job object with
//     kill-on-job-close, so a shim killed out from under it (extension
//     reload, editor exit) cannot orphan the app-server.
//
// A dormant shim is a pass-through: with the enable flag absent the shim
// spawns the recorded (or PATH) codex with UNCHANGED environment — the
// application-scoped setting stays installed without imposing kyacode's
// wiring on unrelated extension usage. Name alone never triggers the
// dispatch (InvokedAsEngine also checks the install dir), so an
// operator's renamed kyacode stays kyacode.
package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"kyacode/internal/config"
	"kyacode/internal/harness/sandbox"
	"kyacode/internal/procctl"
	"kyacode/internal/rootdir"
)

// HomeName is the persistent GUI home's fixed name under ~/.kyacode.
// The sandbox module's orphan sweep can never match it (no pid-random
// tail), which is what makes the home's persistence structural.
const HomeName = "codex-gui"

// runtimeRecordName is the file inside the GUI home that records the
// engine binary the shim drives: written by every `kyacode launch
// codex-gui`, read by every shim spawn. The record is why the shim never
// floats on PATH (an amendment from the design review): the engine is
// the exact binary the launch harvested the model catalog with.
const runtimeRecordName = "runtime.json"

// shimDirName is the install dir for the second-name copy of kyacode.
const shimDirName = "shim"

// runtimeRecord is the on-disk shape of the runtime record.
type runtimeRecord struct {
	CodexExe     string `json:"codex_exe"`
	CodexVersion string `json:"codex_version"`
}

// Dir resolves the shim install dir under ~/.kyacode.
func Dir() (string, error) {
	d, err := rootdir.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, shimDirName), nil
}

// ExePath is the shim binary's install path (the second name the editor
// setting points at): <shimdir>/codex[.exe].
func ExePath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(d, name), nil
}

// EnableFlagPath is the dormant/pass-through switch: its existence turns
// the shim's engine personality on. `kyacode launch codex-gui` sets it;
// removing it returns every extension spawn to vanilla codex behavior
// without touching the editor setting.
func EnableFlagPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "enabled"), nil
}

// HomeDir resolves the persistent GUI home's path (no creation — the
// launch owns materialising it).
func HomeDir() (string, error) {
	d, err := rootdir.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, HomeName), nil
}

// RuntimeRecordPath is where the engine record lives inside the home.
func RuntimeRecordPath() (string, error) {
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, runtimeRecordName), nil
}

// InvokedAsEngine reports whether THIS running binary answers the shim
// dispatch: the file name is the engine's AND the binary lives in the
// shim install dir. Name alone is never sufficient — dispatch by name
// is routing, never identity (a kyacode the operator renamed must stay
// kyacode), and the install dir is what the editor setting actually
// points at.
func InvokedAsEngine() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	base := strings.ToLower(filepath.Base(exe))
	if base != "codex.exe" && base != "codex" {
		return false
	}
	dir, err := Dir()
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Dir(exe), dir)
}

// ElectronProfileDirValue is the dedicated electron user-data directory
// for kyacode-launched desktop instances: the sibling of the codex GUI
// home under ~/.kyacode. The launcher passes it as --user-data-dir so
// the gateway instance's electron state (and its single-instance lock)
// stays fully separate from the user's everyday signed-in app, and the
// marker doubles as the spawn-time discriminator (the shim walks its
// ancestor chain looking for it).
func ElectronProfileDirValue() string {
	d, err := rootdir.Path()
	if err != nil {
		return ""
	}
	return d + string(os.PathSeparator) + "codex-gui-electron"
}

// hasExactArg reports whether argv carries the bare token (the
// exec-server detection needs exact matching: a config value or path
// that merely contains the word must not trip it).
func hasExactArg(argv []string, token string) bool {
	for _, a := range argv {
		if a == token {
			return true
		}
	}
	return false
}

// DebugFlagPath is the spawn-diagnostics switch: while the file exists,
// every shim spawn writes its ancestry observations to
// last-spawn-debug.txt beside it (the env channel cannot carry a debug
// toggle - MSIX activation sanitizes it - so the switch is a file).
func DebugFlagPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "debug"), nil
}

// maybeSpawnDebug records the ancestry observations and the resolved
// mode when the debug switch is on. A failure anywhere degrades to no
// diagnostics: the debug surface must never influence the spawn.
func maybeSpawnDebug(mode int, lines []string) {
	p, err := DebugFlagPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(p); err != nil {
		return
	}
	out, err2 := Dir()
	if err2 != nil {
		return
	}
	names := map[int]string{desktopModeNone: "none (extension path)", desktopModeVanilla: "vanilla passthrough", desktopModeGateway: "gateway"}
	body := "mode: " + names[mode] + "\n"
	for _, l := range lines {
		body += l + "\n"
	}
	_ = os.WriteFile(filepath.Join(out, "last-spawn-debug.txt"), []byte(body), 0o600)
}

// Run IS the engine personality: resolve the mode (desktop gateway,
// desktop vanilla, or the legacy extension path), wire the child, and
// return its exit code. It never returns to the CLI dispatch - main()
// exits with the returned code.
func Run() int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, "kyacode shim: "+format+"\n", args...)
		return 1
	}
	// The desktop personalities come first: CODEX_CLI_PATH reaches both
	// the user's everyday desktop instance and any kyacode-launched one
	// (registry-delivered env), so spawn-time ancestry - not the enable
	// flag - decides gateway versus vanilla.
	mode, chainLines := resolveDesktopModeDiag()
	maybeSpawnDebug(mode, chainLines)
	switch mode {
	case desktopModeGateway:
		if hasExactArg(os.Args[1:], "exec-server") {
			return fail("cloud environments (exec-server) are unavailable in gateway mode - this desktop session runs on the KYA gateway home")
		}
		return runGatewayEngine(fail)
	case desktopModeVanilla:
		exe, _, err := desktopVanillaRuntime()
		if err != nil {
			return fail("%v", err)
		}
		// Vanilla passthrough: the environment stays untouched, the argv
		// rides through, and the binary is the desktop's own managed
		// runtime - byte-for-byte the spawn the host would have made.
		return runChild(exe, os.Args[1:], nil)
	}
	return runLegacy(fail, readRuntimeRecord())
}

// runLegacy is the pre-desktop behavior, kept verbatim: the enable flag
// governs, pass-through resolves the recorded (or PATH) codex, and the
// enabled branch is the gateway engine flow.
func runLegacy(fail func(string, ...any) int, rec *runtimeRecord) int {
	enabled := false
	if p, err := EnableFlagPath(); err == nil {
		if _, err := os.Stat(p); err == nil {
			enabled = true
		}
	}
	if !enabled {
		// Pass-through: the setting stays installed, kyacode's wiring
		// does not apply. The engine is the recorded runtime when known
		// (the same binary a GUI launch would drive), else the PATH
		// codex — this binary itself is never the engine.
		exe := ""
		if rec != nil && fileExists(rec.CodexExe) {
			exe = rec.CodexExe
		}
		if exe == "" {
			p, ok := lookPathEngine()
			if !ok {
				return fail("pass-through needs a recorded runtime or a codex on PATH — run `kyacode launch codex-gui <model>` once")
			}
			exe = p
		}
		return runChild(exe, os.Args[1:], nil)
	}
	return runGatewayEngine(fail)
}

// runGatewayEngine is the gateway wiring both gateway surfaces share
// (the kyacode-launched desktop instance and the enabled extension):
// the persistent home must exist, the token is read fresh at spawn, the
// lease enforces one live engine per home, and the recorded runtime is
// the exact binary the launch harvested the catalog with.
func runGatewayEngine(fail func(string, ...any) int) int {
	home, err := HomeDir()
	if err != nil {
		return fail("%v", err)
	}
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return fail("no GUI home yet — run `kyacode launch codex-gui <model>` first")
	}
	tok, err := config.Token()
	if err != nil {
		return fail("%v", err)
	}
	lease, err := sandbox.AcquireLease(home)
	if err != nil {
		return fail("%v — close the Codex panel or run 'Developer: Reload Window' in VS Code, then reopen it", err)
	}
	defer lease.Release()
	rec := readRuntimeRecord()
	if rec == nil || rec.CodexExe == "" || !fileExists(rec.CodexExe) {
		return fail("no engine recorded in %s — run `kyacode launch codex-gui <model>` once", HomeName)
	}
	// The wiring rows ride LAST so they outrank any inherited CODEX_HOME
	// or stale KYA_API_KEY from the editor's environment — the same
	// last-wins mechanism the launcher's env injection relies on.
	rows := []string{"CODEX_HOME=" + home, "KYA_API_KEY=" + tok}
	return runChild(rec.CodexExe, os.Args[1:], rows)
}

// runChild spawns exe with argv, wires stdio straight through, owns the
// child in a platform job object where one exists, forwards the process
// group's soft interrupt rung to it, and propagates its exit code.
func runChild(exe string, argv []string, rows []string) int {
	cmd := exec.Command(exe, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if rows != nil {
		cmd.Env = append(os.Environ(), rows...)
	}
	procctl.ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "kyacode shim: spawn %s: %v\n", exe, err)
		return 1
	}
	// The job assignment rides AFTER Start (a suspended-start assignment
	// would need the main-thread handle, which the stdlib does not
	// surface); the window before it is sub-millisecond against an
	// app-server that needs seconds to spawn its own children, and the
	// signal path's ForceTree covers anything that slips it.
	release := assignJob(cmd.Process)
	defer release()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			return exitCode(err)
		case <-stop:
			// One soft rung to the child's group, one grace window,
			// then the force rung — the ladder's escalation shape in
			// miniature; the shim has no cleanup of its own to run.
			_ = procctl.GracefulInterrupt(cmd.Process)
			select {
			case err := <-done:
				return exitCode(err)
			case <-time.After(2 * time.Second):
				_ = procctl.ForceTree(cmd.Process.Pid)
				<-done
				return 1
			}
		}
	}
}

// exitCode folds a cmd.Wait result into a process exit code.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// SaveRuntimeRecord records the engine binary and its version inside the
// GUI home: the exact executable the shim drives and the catalog was
// harvested with. Every launch rewrites it, so the record always
// describes the runtime the LAST launch vetted.
func SaveRuntimeRecord(exe, version string) error {
	p, err := RuntimeRecordPath()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(runtimeRecord{CodexExe: exe, CodexVersion: version}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(body, '\n'), 0o600)
}

// readRuntimeRecord loads the engine record from the GUI home; a missing
// record is a nil,nil (the caller decides what "no record" means per
// mode) and a corrupt one degrades to missing with a stderr note — the
// record is derived state, always rewritten by the next launch.
func readRuntimeRecord() *runtimeRecord {
	p, err := RuntimeRecordPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var rec runtimeRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		fmt.Fprintf(os.Stderr, "kyacode shim: corrupt %s ignored (rewritten by the next launch)\n", runtimeRecordName)
		return nil
	}
	return &rec
}

// lookPathEngine resolves the PATH codex for pass-through, refusing the
// shim's own file (a pass-through that re-enters itself would loop).
func lookPathEngine() (string, bool) {
	for _, name := range []string{"codex.exe", "codex"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if self, err := ExePath(); err == nil && strings.EqualFold(p, self) {
			continue
		}
		return p, true
	}
	return "", false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Install makes the second-name copy of the CURRENT binary: the running
// executable is copied to ExePath, replacing any earlier copy — on
// Windows a copy cannot overwrite an executable in use, so a live shim
// is first renamed aside (a rename of a running binary IS allowed) and
// the newest copy takes the name; the renamed-aside copy is removed on
// the next successful install, keeping at most one generation of
// rollback on disk.
func Install() error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	dst, err := ExePath()
	if err != nil {
		return err
	}
	src, err := os.Executable()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmp, 0o755); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		// The in-use rename dance: the live copy steps aside, the new
		// one takes the name, the old one is removed here and now — a
		// failure at THIS remove is logged and dropped, never fatal
		// (the next install reclaims it).
		aside := dst + ".old"
		if rerr := os.Rename(dst, aside); rerr != nil {
			os.Remove(tmp)
			return fmt.Errorf("shim %s is live and cannot be replaced: %w", dst, rerr)
		}
		if rerr := os.Rename(tmp, dst); rerr != nil {
			os.Rename(aside, dst) // best-effort restore of the live copy
			os.Remove(tmp)
			return rerr
		}
		os.Remove(aside)
	}
	return nil
}

// SetEnabled turns the engine personality on or off by creating or
// removing the enable flag. Installing the shim does NOT enable it: the
// flag is the GUI switch the launch sets, so a manually pointed-at shim
// stays pass-through until a kyacode launch turns it on.
func SetEnabled(on bool) error {
	p, err := EnableFlagPath()
	if err != nil {
		return err
	}
	if !on {
		err := os.Remove(p)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	d := filepath.Dir(p)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o600)
}
