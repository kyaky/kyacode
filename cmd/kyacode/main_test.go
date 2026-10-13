package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kyacode/internal/config"
	"kyacode/internal/gatewaytest"
)

// The CLI-level seam: subcommand semantics. Tests here build the real
// binary once and run it in an isolated HOME so nothing touches the
// developer's ~/.kyacode on any platform (Windows reads USERPROFILE, so
// both must be set on the child process).

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kyacode-bin-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)
	bin = filepath.Join(dir, "kyacode")
	if os.PathSeparator == '\\' {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build kyacode: %v\n%s", err, out)
		os.Exit(2)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// cliEnv is the hermetic environment one subcommand invocation runs in.
type cliEnv struct {
	home  string
	env   []string
	token string
}

// newBareCLIEnv is the no-.kyacode variant of the hermetic env — and the
// single owner of that env assembly (the four-row hermetic contract is
// written once, here). The root is deliberately NOT pre-created, so
// tests can observe whether a command materialises it as a side effect
// (the default fixture's pre-created dir would mask exactly that).
func newBareCLIEnv(t *testing.T, base string) cliEnv {
	t.Helper()
	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"KYA_GATEWAY=" + base,
		"KYA_GATEWAY_INSECURE=1",
	}
	return cliEnv{
		home:  home,
		env:   append(os.Environ(), env...),
		token: filepath.Join(home, ".kyacode", "token"),
	}
}

// newCLIEnv is the bare env plus the one mkdir line it exists for: the
// pre-created ~/.kyacode the pre-existing pins assume (they never
// observe the side-effect class).
func newCLIEnv(t *testing.T, base string) cliEnv {
	t.Helper()
	e := newBareCLIEnv(t, base)
	if err := os.MkdirAll(filepath.Dir(e.token), 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

// withPathPrepended puts dir in front of PATH on the child process so a
// stub harness shim resolves before any real installation.
func (e cliEnv) withPathPrepended(dir string) cliEnv {
	out := make([]string, 0, len(e.env)+1)
	for _, kv := range e.env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			continue // drop inherited PATH; replaced below
		}
		out = append(out, kv)
	}
	sep := ";"
	if runtime.GOOS != "windows" {
		sep = ":"
	}
	return cliEnv{
		home:  e.home,
		env:   append(out, "PATH="+dir+sep+getenvFallback("PATH")),
		token: e.token,
	}
}

func getenvFallback(name string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return ""
}

// pathOnly is the install-on-miss hermeticity fixture: PATH is EXACTLY dir
// with no system fallback (withPathPrepended keeps the inherited PATH at
// the tail — a cline or codex present on the developer's machine would make
// the first Find hit, the confirm prompt never appear, and the cancellation
// pin go red). Safe: the kyacode child is launched by ABSOLUTE path (run),
// the POSIX shims use absolute /bin/sh, and the harness child resolves
// through the stub in dir (LookPath, PATH is dir).
func (e cliEnv) pathOnly(dir string) cliEnv {
	out := make([]string, 0, len(e.env)+1)
	for _, kv := range e.env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return cliEnv{
		home:  e.home,
		env:   append(out, "PATH="+dir),
		token: e.token,
	}
}

// stubHarness writes a shim named name into dir that echoes its own
// arguments, so an end-to-end launch shows exactly what reached the child.
func stubHarness(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		shim := filepath.Join(dir, name+".cmd")
		// %* expands to every argument, so stdout names the full child argv.
		if err := os.WriteFile(shim, []byte("@echo off\r\necho %*\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return shim
	}
	shim := filepath.Join(dir, name)
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return shim
}

// stubNpm writes a stub npm into dir that, when invoked as
// "npm install -g <pkg>", writes an echo harness shim for the package (the
// last argument; @scope/pkg → pkg) BESIDE the stub. The post-install Find
// then resolves the shim through LookPath (PATH is dir in the pathOnly
// fixture) and the child runs: Windows emits cline.cmd (CRLF — a
// batch-content file without a runnable suffix cannot be exec'd), POSIX a
// #!/bin/sh script. The stub derives its own dir (%~dp0 on Windows,
// ${0%/*} on POSIX) so the same fixture works whether the harness is npm or
// a scoped package; the POSIX stub restores PATH for its own cat/chmod only
// (the fixture strips it, and a child's PATH never propagates back to
// kyacode's post-install Find).
func stubNpm(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		content := strings.Join([]string{
			"@echo off",
			"setlocal",
			`set "pkg="`,
			`for %%a in (%*) do set "pkg=%%a"`,
			`set "target=%pkg%"`,
			// Last package segment: an npm scoped package @scope/pkg names its
			// binary after the single /; a bare package (cline) has no second
			// token, so the for body is a no-op and target is unchanged.
			`for /f "tokens=2 delims=/" %%b in ("%target%") do set "target=%%b"`,
			"(",
			"echo @echo off",
			"echo echo %%*",
			`) > "%~dp0%target%.cmd"`,
		}, "\r\n")
		if err := os.WriteFile(filepath.Join(dir, "npm.cmd"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	content := `#!/bin/sh
pkg=""
for a in "$@"; do pkg="$a"; done
target="${pkg##*/}"
dir=$(cd "${0%/*}" && pwd)
# The fixture strips PATH to shimDir only, so this stub's own external tools
# (cat, chmod) would be unresolvable — restore a standard PATH for JUST the
# stub. A child's PATH export never propagates to the parent, so kyacode's
# post-install Find still runs under the fixture's stripped PATH.
PATH="/usr/local/bin:/usr/bin:/bin${PATH:+:$PATH}"
cat > "$dir/$target" <<'SHIM'
#!/bin/sh
printf '%s\n' "$*"
SHIM
chmod 700 "$dir/$target"
`
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

// cfgPath is the config.json inside the isolated HOME.
func (e cliEnv) cfgPath() string {
	return filepath.Join(e.home, ".kyacode", "config.json")
}

// readCfg parses the persisted config.json (very sparingly: only the
// gateway field) so tests can compare what the CLI printed with what is
// actually on disk.
func (e cliEnv) readCfg(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.cfgPath())
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var s struct {
		Gateway string `json:"gateway"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse config.json %q: %v", b, err)
	}
	return s.Gateway
}

func (e cliEnv) withToken(tok string) cliEnv {
	if err := os.WriteFile(e.token, []byte(tok+"\n"), 0o600); err != nil {
		panic(err)
	}
	return e
}

// runCore is the ONE spawn fixture: run the built binary in e.env, capture
// stdout+stderr, and return (trimmed combined output, exit code). stdin is
// the child's stdin — nil leaves it to exec's default, a *strings.Reader
// pipes a scripted answer. run and runWithStdin are thin wrappers so the
// spawn mechanics live in exactly one place.
func (e cliEnv) runCore(t *testing.T, stdin io.Reader, args ...string) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Env = e.env
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return strings.TrimSpace(stdout.String() + "\n" + stderr.String()), exit
}

func (e cliEnv) run(t *testing.T, args ...string) (string, int) {
	return e.runCore(t, nil, args...)
}

// runWithStdin is run with a controlled stdin — the install-on-miss e2e
// pipes the "[y/N]" answer (or EOF) exactly the way a scripted operator
// would. confirmYes (launch.go) consumes the line strictly BEFORE the
// installer exec and BEFORE the harness child, so the answer and the
// child's stdin never contend for the same byte.
func (e cliEnv) runWithStdin(t *testing.T, stdin string, args ...string) (string, int) {
	return e.runCore(t, strings.NewReader(stdin), args...)
}

// `kyacode models` must refuse to follow a redirecting gateway: the hop
// target records zero requests, the command fails, and the refusal names
// the redirect.
func TestModelsCommandRefusesARedirectingGateway(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gw-model"}]}`))
	}))
	t.Cleanup(target.Close)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(gateway.Close)

	out, exit := newCLIEnv(t, gateway.URL).withToken("sk-test-123456").run(t, "models")
	if exit == 0 {
		t.Errorf("redirecting gateway must make `models` fail; output: %s", out)
	}
	if !strings.Contains(out, "redirect") {
		t.Errorf("refusal should name the redirect, got: %s", out)
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target received %d request(s) with the token attached; must record zero", n)
	}
}

// Setting the gateway to a URL config.Load would reject (plaintext http to
// a non-loopback host) must fail WITHOUT persisting: a rejected URL on
// disk bricks every later command behind Load's hard error.
func TestGatewayCommandNeverPersistsRejectedURLs(t *testing.T) {
	e := newCLIEnv(t, "")
	out, exit := e.run(t, "gateway", "http://gw.example.com")
	if exit == 0 {
		t.Errorf("non-loopback http:// must be rejected; output: %s", out)
	}
	// Nothing may persist: either no config.json exists yet, or it still
	// holds the default — never the rejected value.
	if b, err := os.ReadFile(e.cfgPath()); err == nil {
		var s struct {
			Gateway string `json:"gateway"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("persisted config unparsable after rejection: %q (%v)", b, err)
		}
		if s.Gateway != config.DefaultGateway {
			t.Errorf("rejected URL persisted; config.json now has gateway %q", s.Gateway)
		}
	}
	// And the CLI still boots: the show path must not be bricked.
	out, exit = e.run(t, "gateway")
	if exit != 0 {
		t.Errorf("gateway show bricked by the rejected save: %s", out)
	}
}

// A poisoned config.json (hand-edited garbage) must be repairable with
// `kyacode gateway <known-good>`; refusing to repair means disk surgery
// by hand as the only route back.
func TestGatewaySetRepairsAPoisonedConfigFile(t *testing.T) {
	e := newCLIEnv(t, "")
	if err := os.MkdirAll(filepath.Dir(e.cfgPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.cfgPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, exit := e.run(t, "gateway", "https://gw.example.com")
	if exit != 0 {
		t.Fatalf("gateway set must be able to repair a poisoned config, failed: %s", out)
	}
	if got := e.readCfg(t); got != "https://gw.example.com" {
		t.Errorf("repaired config.json has gateway %q", got)
	}
}

// What `kyacode gateway` (show) prints must be exactly what is persisted,
// in canonical /v1-stripped form, environment included.
func TestGatewayReportMatchesWhatsOnDisk(t *testing.T) {
	e := newCLIEnv(t, "")
	out, exit := e.run(t, "gateway", "https://gw.example.com/v1/")
	if exit != 0 {
		t.Fatalf("gateway set failed: %s", out)
	}
	if got := e.readCfg(t); got != "https://gw.example.com" {
		t.Errorf("config.json has gateway %q, want the canonical stripped form", got)
	}
	out, exit = e.run(t, "gateway")
	if exit != 0 {
		t.Fatalf("gateway show failed: %s", out)
	}
	if !strings.Contains(out, "https://gw.example.com") {
		t.Errorf("showed %q; want the stored gateway", out)
	}
}

// Argumentless subcommands take no arguments: `unauth --help` must be a
// loud error, not a silent no-op that just deletes the key.
func TestArgumentlessSubcommandsRejectExtraArguments(t *testing.T) {
	e := newCLIEnv(t, "").withToken("sk-test-123456")
	for _, args := range [][]string{
		{"unauth", "--help"},
		{"unauth", "extra"},
		{"doctor", "extra"},
		{"status", "-x"},
		{"models", "extra"},
		{"version", "extra"},
	} {
		_, exit := e.run(t, args...)
		if exit == 0 {
			t.Errorf("%v: silent success; expected rejection", args)
		}
	}
}

// `models` lists the gateway catalog filtered and sorted: the blank
// placeholder rows drop out and output is deterministic. The fake is the
// shared gatewaytest.Models helper (gateway, harness, doctor ride the
// same one) — the CLI env's token matches its asserted Bearer.
func TestModelsCommandListsSortedGatewayIDs(t *testing.T) {
	gateway := gatewaytest.Models(t, "sk-test-123456", []string{"beta", "", "alpha"})

	out, exit := newCLIEnv(t, gateway.URL).withToken("sk-test-123456").run(t, "models")
	if exit != 0 {
		t.Fatalf("models failed: %s", out)
	}
	if out != "alpha\nbeta" {
		t.Errorf("models printed %q, want sorted non-empty ids only", out)
	}
}

// Green pin: `auth --stdin` persists and round-trips. On Windows chmod
// weakens to a read-only bit, so the 0600 assertion is POSIX-only.
func TestAuthStdinRoundTripStoresAndVerifies0600(t *testing.T) {
	e := newCLIEnv(t, "")
	cmd := exec.Command(bin, "auth", "--stdin")
	cmd.Env = e.env
	cmd.Stdin = strings.NewReader("sk-test-123456\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("auth --stdin: %v (out %s)", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "key saved") {
		t.Errorf("auth should confirm the saved path, got: %s", stdout.String())
	}
	b, err := os.ReadFile(e.token)
	if err != nil || strings.TrimSpace(string(b)) != "sk-test-123456" {
		t.Errorf("token round-trip failed: %q (%v)", b, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(e.token)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("token mode = %o, want 600", got)
		}
	}
}

// Piping a key into `auth` (no --stdin) is a scripted run: the prompt goes
// to a tty only, stdout must carry just the saved-path line — other
// tooling greps the output for where the key landed.
func TestAuthPipedRunWritesOnlyTheSavedPathLineToStdout(t *testing.T) {
	e := newCLIEnv(t, "")
	cmd := exec.Command(bin, "auth")
	cmd.Env = e.env
	cmd.Stdin = strings.NewReader("sk-test-123456\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("auth piped: %v (out %q)", err, stdout.String())
	}
	out := stdout.String()
	if strings.Contains(out, "paste") {
		t.Errorf("piped stdout must not carry the tty prompt; got %q", out)
	}
	if !strings.Contains(out, "key saved") {
		t.Errorf("piped stdout should confirm where the key was saved; got %q", out)
	}
}

// Green pin: an unknown command is usage + exit 2, like a flag parse error.
func TestUnknownCommandExitsWith2(t *testing.T) {
	e := newCLIEnv(t, "")
	out, exit := e.run(t, "nonsense")
	if exit != 2 {
		t.Errorf("unknown command exit = %d, want 2 (output %q)", exit, out)
	}
}

// `kyacode version` is the user's only way to tell what they run: the
// plain test build carries no stamp, so it must print the clearly-unnamed
// dev form (plus the source-build hint) and STILL exit 0 — never an error.
func TestVersionCommandPrintsDevFormAndExits0(t *testing.T) {
	e := newCLIEnv(t, "")
	out, exit := e.run(t, "version")
	if exit != 0 {
		t.Errorf("version exit = %d, want 0 (output %q)", exit, out)
	}
	if !strings.Contains(out, "kyacode v0.0.0-dev") {
		t.Errorf("version output %q misses the dev form", out)
	}
	if !strings.Contains(out, "built from source") {
		t.Errorf("the dev form must name itself as unstamped, got: %q", out)
	}
}

// The release stamp is the whole point of the subcommand: a build with the
// -ldflags -X injection must print the injected semver verbatim (no dev
// hint), proving the release workflow's stamp reaches users.
func TestVersionCommandPrintsTheStampedVersion(t *testing.T) {
	stamped := filepath.Join(t.TempDir(), "kyacode-stamped")
	if os.PathSeparator == '\\' {
		stamped += ".exe"
	}
	out, err := exec.Command("go", "build", "-ldflags", "-X main.Version=v9.9.9-test", "-o", stamped, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("stamped build: %v\n%s", err, out)
	}
	cmd := exec.Command(stamped, "version")
	cmd.Env = newCLIEnv(t, "").env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("stamped version: %v (out %q)", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "kyacode v9.9.9-test") {
		t.Errorf("stamped output %q misses the injected version", stdout.String())
	}
	if strings.Contains(stdout.String(), "built from source") {
		t.Errorf("a stamped build must not print the source-build hint: %q", stdout.String())
	}
}

// `status` must not print the stored token: the preview is a bounded
// prefix+suffix, and nothing longer survives into stdout.
func TestStatusTokenPreviewRespectsTheLengthGate(t *testing.T) {
	e := newCLIEnv(t, "").withToken("sk-abcdef1234567890abcdef")
	out, exit := e.run(t, "status")
	if exit != 0 {
		t.Fatalf("status failed: %s", out)
	}
	if strings.Contains(out, "abcdef1234567890") || strings.Contains(out, strings.Repeat("a", 5)) {
		t.Errorf("status printed token material: %s", out)
	}
}

// The status overview must honor Token()'s read taxonomy at the display
// site too: a missing file says the friendly not-stored line, while an
// unreadable file names the cause — status is the diagnostic command,
// and "not stored — run auth" would send the operator into the re-auth
// that fails identically on an unreadable file (POSIX-DAC fixture).
func TestStatusSurfacesUnreadableTokenInsteadOfNotStored(t *testing.T) {
	e := newCLIEnv(t, "")
	out, exit := e.run(t, "status")
	if exit != 0 || !strings.Contains(out, "not stored (run `kyacode auth`)") {
		t.Fatalf("missing-token status must keep the friendly line (exit %d, out %q)", exit, out)
	}
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("unreadable-file fixture needs POSIX DAC as non-root")
	}
	if err := os.WriteFile(e.token, []byte("sk-test-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.token, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(e.token, 0o600)
	out, exit = e.run(t, "status")
	if exit != 0 {
		t.Fatalf("status must not fail on an unreadable token; exit %d (out %q)", exit, out)
	}
	if !strings.Contains(out, "reading token:") {
		t.Errorf("status must surface the unreadable-token cause, got: %q", out)
	}
	if strings.Contains(out, "not stored") {
		t.Errorf("an unreadable token must not collapse into not-stored at the display site: %q", out)
	}
}

// The whole child argv for `launch claude m1 -- --flag value` is exactly
// [--flag value]: the model rides the ANTHROPIC_MODEL env row since the
// carrier conversion (claude's picker aliases make --model redundant),
// everything after the first `--` rides as-is. A stub claude on PATH
// prints its own argv.
func TestLaunchDashDashPassthroughFollowsTheSplit(t *testing.T) {
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "claude")

	e := newCLIEnv(t, "").withToken("sk-test-123456").withPathPrepended(shimDir)
	out, exit := e.run(t, "launch", "claude", "m1", "--", "--flag", "value")
	if exit != 0 {
		t.Fatalf("launch failed: %s", out)
	}
	if strings.Contains(out, "--model m1") {
		t.Errorf("child argv carried a --model row: %q — the model moved to ANTHROPIC_MODEL", out)
	}
	if !strings.Contains(out, "--flag value") {
		t.Errorf("child argv was %q; want the passthrough --flag value", out)
	}
}

// Single-model harnesses refuse to run without an explicit model, before
// any endpoint resolution: a silent untitled session is worse than an
// error. Catalog harnesses (opencode — in-session picker) keep launching
// model-less.
func TestLaunchWithoutModelRefusesSingleModelHarnesses(t *testing.T) {
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "claude")
	e := newCLIEnv(t, "").withToken("sk-test-123456").withPathPrepended(shimDir)
	out, exit := e.run(t, "launch", "claude")
	if exit == 0 {
		t.Errorf("launch without a model on a single-model harness must fail; out %q", out)
	}
	if !strings.Contains(out, "requires a model") {
		t.Errorf("refusal should say the harness requires a model, got: %s", out)
	}
}

func TestLaunchWithoutModelStillAllowsCatalogHarnesses(t *testing.T) {
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "opencode")
	// Local catalog double: opencode's model-less inline config fetches the
	// catalog, and with KYA_GATEWAY unset that would resolve to the real
	// production gateway — a live outbound request from a test. Belt the
	// launch to a loopback server instead.
	gateway := gatewaytest.Models(t, "sk-test-123456", []string{"gw-model"})
	e := newCLIEnv(t, gateway.URL).withToken("sk-test-123456").withPathPrepended(shimDir)
	out, exit := e.run(t, "launch", "opencode")
	if strings.Contains(out, "requires a model") {
		t.Errorf("catalog harness must launch model-less, got: %s", out)
	}
	if exit != 0 {
		t.Logf("catalog model-less harness exit %d (stub limits itself to existence): %q", exit, out)
	}
}

// The launch parser defends the model boundary against quoting bugs:
// whitespace-only model ids are the SAME bug family as the empty string
// (a wrapping script passing "$MODEL" with an unquoted env) and must
// take the same loud empty-model refusal — never land verbatim inside
// harness pins as an id named after spaces.
func TestLaunchRejectsWhitespaceOnlyModelArgument(t *testing.T) {
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "omp")
	e := newCLIEnv(t, "").withToken("sk-test-123456").withPathPrepended(shimDir)
	out, exit := e.run(t, "launch", "omp", "   ")
	if exit != 1 {
		t.Errorf("whitespace-only model exit = %d, want 1 (out %q)", exit, out)
	}
	if !strings.Contains(out, "empty <model> argument") {
		t.Errorf("refusal must reuse the empty-model quoting-bug error, got: %q", out)
	}
	if strings.Contains(out, "requires a model") {
		t.Errorf("the whitespace refusal must hit the parse guard, not the model gate: %q", out)
	}

	// The same refusal through a RequiresModel runner: claude would reach
	// the model gate on an empty id, so the whitespace refusal landing
	// FIRST is the ordering claim the omp drive above cannot prove (omp
	// declares no RequiresModel, so its assertion can never bite).
	stubHarness(t, shimDir, "claude")
	out, exit = e.run(t, "launch", "claude", "   ")
	if exit != 1 || !strings.Contains(out, "empty <model> argument") || strings.Contains(out, "requires a model") {
		t.Errorf("claude whitespace drive = (%d, %q), want the parse guard to win before the model gate", exit, out)
	}
}

// Outer whitespace around a real id is trimmed once at the boundary
// (copied-into-the-command reality): the composing runners pin the
// TRIMMED id, never `kya/  m1  ` interior-space ids keep their interior.
func TestLaunchTrimsOuterWhitespaceOnModelIds(t *testing.T) {
	// Loopback catalog double — omp still fetches with a CLI model, and a
	// bare KYA_GATEWAY would resolve to the REAL production gateway.
	gateway := gatewaytest.Models(t, "sk-test-123456", []string{"m1"})
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "omp")
	e := newCLIEnv(t, gateway.URL).withToken("sk-test-123456").withPathPrepended(shimDir)
	out, exit := e.run(t, "launch", "omp", "  m1  ")
	if exit != 0 {
		t.Fatalf("trimmed launch failed: %s", out)
	}
	if !strings.Contains(out, "--model kya/m1 ") && !strings.Contains(strings.TrimSuffix(out, "\n"), "--model kya/m1") {
		t.Errorf("child argv %q must carry the trimmed scoped id kya/m1 (no outer spaces)", out)
	}
}

// Ticket 21's rule at the boundary: a provider-scoped CLI model
// argument composes a SECOND scope layer for the runners that hand the
// harness "kya/<id>" — an id no gateway serves — so the launch refuses
// it loudly instead of composing.
func TestLaunchRefusesProviderScopedCliModelForScopedComposers(t *testing.T) {
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "omp")
	stubHarness(t, shimDir, "openclaw")
	e := newCLIEnv(t, "").withToken("sk-test-123456").withPathPrepended(shimDir)
	for _, harness := range []string{"omp", "openclaw"} {
		out, exit := e.run(t, "launch", harness, "kya/alpha")
		if exit != 1 {
			t.Errorf("%s: scoped-form refusal exit = %d, want 1 (out %q)", harness, exit, out)
		}
		if !strings.Contains(out, "bare id") {
			t.Errorf("%s: refusal must name the bare-id rule, got: %q", harness, out)
		}
	}
}

// The bare-composing runners keep accepting the same string: a gateway
// may genuinely serve an id named like the provider scope, and their
// pins/args are bare, so no composition hazard exists for them.
func TestLaunchStillAcceptsScopedFormOnBareComposers(t *testing.T) {
	// Loopback catalog double (same belt as above — droid always fetches).
	gateway := gatewaytest.Models(t, "sk-test-123456", []string{"m1"})
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "droid")
	e := newCLIEnv(t, gateway.URL).withToken("sk-test-123456").withPathPrepended(shimDir)
	out, exit := e.run(t, "launch", "droid", "kya/alpha")
	if exit != 0 {
		t.Fatalf("droid launch with kya/alpha must not be refused: %s", out)
	}
	if !strings.Contains(out, "-m kya/alpha ") && !strings.Contains(strings.TrimSuffix(out, "\n"), "-m kya/alpha") {
		t.Errorf("child argv %q must carry the bare pin kya/alpha", out)
	}
}

// clinePostInstallPath is where the install-on-miss e2e's post-install Find
// resolves the freshly-installed cline: beside the stub npm (LookPath, PATH
// is shimDir in the pathOnly fixture).
func clinePostInstallPath(shimDir string) string {
	p := filepath.Join(shimDir, "cline")
	if runtime.GOOS == "windows" {
		p += ".cmd"
	}
	return p
}

// TestLaunchClineInstallsOnMissThenLaunches pins the one launch path whose
// behavior is neither declared nor pinned: the install-on-miss sub-flow
// (resolveBin → ensureInstalled → the [y/N] confirm → installer exec →
// post-install Find). cline is NOT pre-installed, so the first Find misses,
// the confirm prompt appears, the stub npm (PATH is shimDir) writes the
// cline shim, the post-install Find hits, and cline's child runs with its
// own model row plus the passthrough. Fully offline: cline's BuildEnv makes
// no catalog fetch, so resolveEndpoint serves the DefaultGateway with no
// config.json (no gatewaytest double needed).
func TestLaunchClineInstallsOnMissThenLaunches(t *testing.T) {
	shimDir := t.TempDir()
	stubNpm(t, shimDir)
	e := newCLIEnv(t, "").withToken("sk-test-123456").pathOnly(shimDir)
	out, exit := e.runWithStdin(t, "y\n", "launch", "cline", "m1", "--", "--flag", "value")
	if exit != 0 {
		t.Fatalf("launch cline (install-on-miss) failed: exit %d, out %q", exit, out)
	}
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("the install confirm prompt must appear on stdout, got: %s", out)
	}
	if !strings.Contains(out, "-m m1 --flag value") {
		t.Errorf("child argv %q, want cline's model row + the passthrough (-m m1 --flag value)", out)
	}
	// The stub cline exists at the Find path after the run.
	if _, err := os.Stat(clinePostInstallPath(shimDir)); err != nil {
		t.Errorf("post-install Find path lacks the stub cline: %v", err)
	}
}

// TestLaunchClineInstallationCancelled pins the fail-closed default the
// prompt path otherwise has ZERO coverage for: an empty/EOF stdin (no "y")
// refuses the install, exits 1, and NEVER runs the installer (the stub npm
// is never invoked, so no cline shim is created). confirmYes swallows the EOF
// and the non-interactive default already refuses — this freeze is what
// makes the nil-confirm inversion observable-safe.
func TestLaunchClineInstallationCancelled(t *testing.T) {
	shimDir := t.TempDir()
	stubNpm(t, shimDir)
	e := newCLIEnv(t, "").withToken("sk-test-123456").pathOnly(shimDir)
	out, exit := e.runWithStdin(t, "", "launch", "cline", "m1")
	if exit != 1 {
		t.Fatalf("cancelled install exit = %d, want 1 (out %q)", exit, out)
	}
	if !strings.Contains(out, "installation cancelled") {
		t.Errorf("refusal must be the fail-closed cancelled sentence, got: %s", out)
	}
	// The installer never ran: no cline shim was created beside the stub.
	if _, err := os.Stat(clinePostInstallPath(shimDir)); !os.IsNotExist(err) {
		t.Errorf("a cancelled install must not have run the installer (cline exists at %s, err %v)", clinePostInstallPath(shimDir), err)
	}
}

// Green pins for the launch parser: quoting bugs and injection shapes are
// rejected before any child starts.
func TestLaunchParserRejectsUnsafeModelArguments(t *testing.T) {
	shimDir := t.TempDir()
	stubHarness(t, shimDir, "claude")
	e := newCLIEnv(t, "").withToken("sk-test-123456").withPathPrepended(shimDir)

	for _, args := range [][]string{
		{"launch", "claude", ""},                  // $MODEL="" quoting bug
		{"launch", "claude", "m1", "extra", "--"}, // args before -- that are not the model
		{"launch", "claude", "m1", "--"},          // -- with nothing after it
		{"launch", "nosuchharness", "m1"},         // unknown harness
	} {
		out, exit := e.run(t, args...)
		if exit == 0 {
			t.Errorf("%v: silent success; expected rejection (out %q)", args, out)
		}
	}
}

// The double-quote refusal was the repo's only zero-pinned boundary
// guard — pin its exact sentence. No harness shim here on purpose: the
// guard must fire before the binary hunt (the fail-fast-before-install
// property its comment states), so the refusal reaches the exit without
// any claude binary ever being resolved.
func TestLaunchRefusesDoubleQuotedModelWithTheExactSentence(t *testing.T) {
	e := newCLIEnv(t, "").withToken("sk-test-123456")
	out, exit := e.run(t, "launch", "claude", `a"b`)
	if exit != 1 {
		t.Errorf("double-quote refusal exit = %d, want 1 (out %q)", exit, out)
	}
	if !strings.Contains(out, "model name must not contain double quotes") {
		t.Errorf("refusal must be the exact sentence, got: %q", out)
	}
}

// Bare `doctor` with no token: the complete operator sentence, pinned
// byte-for-byte through the CLI's error prefix — wrapper (cmdDoctor's
// "doctor needs a stored key"), core (ErrNoToken's wording), and remedy
// (run auth). POST-01 semantics: the missing token file is the friendly
// not-stored class, never a mkdir permission brick.
func TestDoctorWithoutTokenPrintsTheFullWrappedPreRefusal(t *testing.T) {
	e := newCLIEnv(t, "") // no token written
	out, exit := e.run(t, "doctor")
	if exit != 1 {
		t.Fatalf("doctor without a token exit = %d, want 1 (out %q)", exit, out)
	}
	if !strings.Contains(out, "error: doctor needs a stored key: no token stored (run `kyacode auth`)") {
		t.Errorf("missing-token refusal = %q, want the full wrapped form", out)
	}
}

// Bare `doctor` against a fully-serving gateway: exit 0, the banner row,
// the five named probe rows, and the pass line — the half the red-path
// pin (which rides KYA_GATEWAY alone today) never exercised because no
// models-only fake can green the wire probes by design.
func TestDoctorGreenPathOnARouteCapableGateway(t *testing.T) {
	// gatewaytest.DoctorServing: the models-only fake 404s the four wire
	// probes by design, so the route-capable shared fake serves the
	// probe set plus the models row under the same literal-Bearer
	// discipline.
	gateway := gatewaytest.DoctorServing(t, "sk-test-123456", []string{"gw-model"})
	e := newCLIEnv(t, gateway.URL).withToken("sk-test-123456")
	out, exit := e.run(t, "doctor")
	if exit != 0 {
		t.Fatalf("doctor must pass a fully-serving gateway (exit %d): %s", exit, out)
	}
	if !strings.Contains(out, "gateway: "+gateway.URL) {
		t.Errorf("missing the banner row: %s", out)
	}
	for _, row := range []string{"anthropic /v1/messages", "openai /v1/chat/completions", "responses /v1/responses", "beta header tolerance", "list /v1/models"} {
		if !strings.Contains(out, row) {
			t.Errorf("missing the %q probe row: %s", row, out)
		}
	}
	if !strings.Contains(out, "all gateway checks passed") {
		t.Errorf("missing the pass line: %s", out)
	}
}

// The warned arm of the doctor verdict is otherwise unreachable from
// the CLI: every green-path fixture answers the beta probe identically
// to the plain probe, so no test exercised the cmdDoctor branch that
// renders the warned sentence — the one live hole in doctor's three-way
// cannot-disagree contract. The fixture answers the beta probe with a
// body carrying no probe-miss evidence (the Warn branch), and the
// asserted parenthetical is the discriminating byte: the green-path
// pin's bare substring also matches the warned variant, so it cannot
// substitute for this assertion.
func TestDoctorWarnedVerdictThroughTheCLI(t *testing.T) {
	s := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case r.URL.Path == "/v1/messages":
			w.WriteHeader(http.StatusNotFound)
			if r.Header.Get("anthropic-beta") != "" {
				w.Write([]byte(`<html><body>502-ish gateway error page</body></html>`))
				return
			}
			w.Write([]byte(gatewaytest.AnthropicProbeMiss))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(gatewaytest.OpenAIProbeMiss))
		}
	})
	e := newCLIEnv(t, s.URL).withToken("sk-test-123456")
	out, exit := e.run(t, "doctor")
	if exit != 0 {
		t.Fatalf("doctor exit = %d, want 0 (a warn must not block): %q", exit, out)
	}
	if !strings.Contains(out, "all gateway checks passed (warnings above do not block direct connect)") {
		t.Errorf("the warned verdict sentence must name its non-blocking status: %q", out)
	}
	if !strings.Contains(out, "possible beta-header rejection") {
		t.Errorf("the beta row's warn info must reach the operator: %q", out)
	}
	if !strings.Contains(out, "note: pre-flight proves wire reachability") {
		t.Errorf("the capability-honesty note must ride the warned output too: %q", out)
	}
}

// Bare `doctor` with the token but a failing gateway: the failing rows
// and the honest verdict sentence at exit 1 — the red path pinned
// end-to-end through the CLI (the testdouble flat-rejects every route,
// so all five rows fail under the shared code predicate).
func TestDoctorOnAFailingGatewayPrintsTheVerdictAndExits1(t *testing.T) {
	gateway := gatewaytest.Raw(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	e := newCLIEnv(t, gateway.URL).withToken("sk-test-123456")
	out, exit := e.run(t, "doctor")
	if exit != 1 {
		t.Fatalf("doctor against a failing gateway exit = %d, want 1 (out %q)", exit, out)
	}
	if !strings.Contains(out, "gateway pre-flight FAILED — direct connect cannot proceed") {
		t.Errorf("missing the verdict sentence: %s", out)
	}
	// The rows carry the shared taxonomy, not a bare success lie.
	if !strings.Contains(out, "500") || !strings.Contains(out, "boom") {
		t.Errorf("failing rows must carry the status+body shapes: %s", out)
	}
}

// The reported operator gateway (2026-09-14, fixed 2026-09-15):
// per-protocol probe-miss bodies on a healthy gateway must drive the CLI
// to exit 0 — the exact pre-flight that used to FAIL on the anthropic +
// beta rows. The bodies are the shared byte-exact fixture in gatewaytest
// (the report carried no credentials to redact).
func TestDoctorGreenOnTheReportedAnthropicGatewayShape(t *testing.T) {
	gateway := gatewaytest.ReportedAnthropicGateway(t, "sk-test-123456")
	e := newCLIEnv(t, gateway.URL).withToken("sk-test-123456")
	out, exit := e.run(t, "doctor")
	if exit != 0 {
		t.Fatalf("the reported gateway must pre-flight green through the CLI (exit %d): %s", exit, out)
	}
	for _, row := range []string{"anthropic /v1/messages", "beta header tolerance", "all gateway checks passed"} {
		if !strings.Contains(out, row) {
			t.Errorf("missing %q: %s", row, out)
		}
	}
	// The capability honesty note rides every pre-flight: wire
	// reachability is not real-model inference, streaming, or tool calls.
	if !strings.Contains(out, "not real-model inference, streaming, or tool calls") {
		t.Errorf("missing the capability note: %s", out)
	}
}

// runStreams runs the CLI with stdout and stderr separated — needed where
// a seam splits the report channel from the warning channel.
func (e cliEnv) runStreams(t *testing.T, args ...string) (out, errs string, exit int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = e.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), exit
}

// The data-moving subcommands follow the same loud-misuse discipline as
// the argumentless ones: an inline `auth <token>` must error and leave
// the stored token untouched (the old parser dropped the inline token
// with no word), `auth --stdin extra` is refused, and `gateway <url>
// extra` refuses the whole set instead of persisting args[0] and
// silently discarding the rest.
func TestAuthAndGatewayRejectExtraArgumentsLoudly(t *testing.T) {
	e := newCLIEnv(t, "").withToken("sk-test-123456")
	for _, args := range [][]string{
		{"auth", "sk-inline-pasted-key"},
		{"auth", "--stdin", "extra"},
		{"gateway", "https://gw.example.com", "extra"},
	} {
		out, exit := e.run(t, args...)
		if exit != 1 {
			t.Errorf("%v: exit = %d, want 1 (the data-shaped misuse tier)", args, exit)
		}
		if !strings.Contains(out, "takes") {
			t.Errorf("%v: refusal must name the take-what rule, got: %q", args, out)
		}
	}
	// The failed inline `auth` call must not have touched the stored key.
	b, err := os.ReadFile(e.token)
	if err != nil || strings.TrimSpace(string(b)) != "sk-test-123456" {
		t.Errorf("a refused auth call must leave the stored token untouched: %q (%v)", b, err)
	}
	// And the gateway set with extra args must have persisted nothing new
	// (no file yet, or a file not carrying the refused URL).
	if b, err := os.ReadFile(e.cfgPath()); err == nil {
		if strings.Contains(string(b), "gw.example.com") {
			t.Errorf("a refused gateway set must not persist the URL; file has: %s", b)
		}
	}
}

// `--help` on a data-moving subcommand prints usage and exits 0 — and
// crucially reads no credentials: the old parser sent `auth --help` into
// the password prompt instead.
func TestAuthAndGatewayHelpFlagPrintsUsageWithoutReadingCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "--help"},
		{"auth", "-h"},
		{"gateway", "--help"},
		{"gateway", "-h"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := newCLIEnv(t, "")
			out, exit := e.run(t, args...)
			if exit != 0 {
				t.Errorf("help exit = %d, want 0 (output %q)", exit, out)
			}
			if !strings.Contains(out, "Usage:") {
				t.Errorf("help must print the usage block, got: %q", out)
			}
		})
	}
}

// Whitespace handling is unified across the two sources of the gateway
// URL: a copied-URL-with-spaces must behave identically from KYA_GATEWAY
// (load path, which already trims) and from argv (set path).
func TestGatewayWhitespaceTreatedIdenticallyFromEnvAndArgv(t *testing.T) {
	want := "https://gw.example.com"
	// Env path: the default show (Load applies the override).
	e := newCLIEnv(t, "  "+want+"  ")
	out, exit := e.run(t, "gateway")
	if exit != 0 {
		t.Fatalf("gateway env-show failed: %s", out)
	}
	if !strings.Contains(out, "gateway: "+want) {
		t.Errorf("env path printed %q, want the trimmed %s", out, want)
	}
	// Argv path: the set persists the trimmed form.
	e2 := newCLIEnv(t, "")
	out, exit = e2.run(t, "gateway", "  "+want+"  ")
	if exit != 0 {
		t.Fatalf("gateway argv-set failed: %s", out)
	}
	if got := e2.readCfg(t); got != want {
		t.Errorf("argv path persisted %q, want the trimmed %s", got, want)
	}
	// The note is the explanation of a transformation that happened:
	// whitespace-only input must NOT claim /v1-or-slash removal, while a
	// genuine /v1 suffix still earns the note (an untested branch rots).
	out, _ = e2.run(t, "gateway", "  "+want+"  ")
	if strings.Contains(out, "note:") {
		t.Errorf("whitespace-only input must not print the /v1 note: %q", out)
	}
	e3 := newCLIEnv(t, "")
	out, exit = e3.run(t, "gateway", want+"/v1/")
	if exit != 0 {
		t.Fatalf("gateway /v1 set failed: %s", out)
	}
	if !strings.Contains(out, "note:") {
		t.Errorf("a real /v1-suffix normalization must still print the note, got: %q", out)
	}
}

// The sweep lists what it reclaimed, one path per line — a bare count
// would hide WHICH sandbox (and which stale key) was reaped.
func TestStatusNamesEachSweptSandbox(t *testing.T) {
	e := newCLIEnv(t, "")
	dir := filepath.Join(e.home, ".kyacode", "codex-123-456")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(dir, stamp, stamp); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	out, errs, exit := e.runStreams(t, "status")
	if exit != 0 {
		t.Fatalf("status exit %d, want 0 (out %q err %q)", exit, out, errs)
	}
	if !strings.Contains(out, "swept 1 orphaned sandbox") {
		t.Errorf("stdout missing the swept-count line, got: %s", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("stdout must name the removed path %s, got: %s", dir, out)
	}
	if strings.Contains(errs, "WARNING") {
		t.Errorf("healthy sweep must not warn: %q", errs)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("swept dir still on disk: %v", statErr)
	}
}

// Scan failure is a WARNING on stderr with exit 0 — status is health
// reporting; the sweep must not take the command down (and must not
// swallow the failure silently either). POSIX-DAC fixture: 0300 keeps
// MkdirAll (dir exists) but defeats ReadDir; Windows chmod cannot express
// this, so the wiring is unix-observed — the harness-layer error contract
// is pinned cross-platform in internal/harness.
func TestStatusReportsSweepScanFailureWithoutFailingTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no-listable-dir fixture needs posix-DAC chmod")
	}
	e := newCLIEnv(t, "")
	kya := filepath.Join(e.home, ".kyacode")
	if err := os.Chmod(kya, 0o300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(kya, 0o700)

	out, errs, exit := e.runStreams(t, "status")
	if exit != 0 {
		t.Fatalf("scan failure must not fail status; exit %d (out %q errs %q)", exit, out, errs)
	}
	if !strings.Contains(strings.ToUpper(errs), "WARNING") {
		t.Errorf("stderr missing the sweep warning, got: %q", errs)
	}
	if !strings.Contains(out, "HARNESS") {
		t.Errorf("harness table must still be printed on stdout, got: %q", out)
	}
}

// On a writable bare HOME, every read-only command must run to completion
// WITHOUT materialising ~/.kyacode as a side effect (round-2 ticket 01:
// read paths are pure; the root materialises only when a write site
// runs). Outputs stay byte-identical to the root created — that the dir
// vanishing changes nothing visible is the deviation's positive side.
func TestStatusOnBareHomeServesWithoutCreatingTheRoot(t *testing.T) {
	e := newBareCLIEnv(t, "")
	out, exit := e.run(t, "status")
	if exit != 0 {
		t.Fatalf("status on a bare home failed: %s", out)
	}
	if !strings.Contains(out, "gateway: "+config.DefaultGateway) {
		t.Errorf("the gateway row must print the default: %s", out)
	}
	if !strings.Contains(out, "not stored (run `kyacode auth`)") {
		t.Errorf("the key row must be the friendly not-stored line: %s", out)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".kyacode")); !os.IsNotExist(err) {
		t.Errorf("status created ~/.kyacode as a side effect: %v", err)
	}

	// The brief's remaining read-path consumers keep serving the same
	// bare home without materialising anything: the shared resolver's
	// friendly no-token refusal (models, launch), doctor's pre-refusal,
	// unauth's clean not-found, and gateway show's default.
	for _, args := range [][]string{
		{"doctor"},
		{"models"},
		{"launch", "claude", "m1"},
		{"unauth"},
		{"gateway"},
	} {
		outs, exit := e.run(t, args...)
		if args[0] == "gateway" {
			if exit != 0 {
				t.Errorf("gateway show on a bare home must serve the default (exit %d): %s", exit, outs)
			}
		} else if exit == 0 {
			t.Errorf("%v: expected the friendly pre-refusal, got exit 0: %s", args, outs)
		}
		if args[0] == "unauth" {
			if !strings.Contains(outs, "not found") {
				t.Errorf("unauth on a bare home must answer not-found (no brick): %s", outs)
			}
		} else if args[0] != "gateway" && !strings.Contains(outs, "no token stored") {
			t.Errorf("%v must surface the friendly not-stored refusal: %s", args, outs)
		}
		if _, err := os.Stat(filepath.Join(e.home, ".kyacode")); !os.IsNotExist(err) {
			t.Errorf("%v created ~/.kyacode as a side effect: %v", args, err)
		}
	}

	// The write-site leg, end to end from the SAME bare home: the set
	// path creates the root exactly when it persists.
	outs, exit := e.run(t, "gateway", "https://gw.example.com")
	if exit != 0 {
		t.Fatalf("gateway set from a bare home must create the root and persist: %s", outs)
	}
	if got := e.readCfg(t); got != "https://gw.example.com" {
		t.Errorf("persisted %q", got)
	}
}
