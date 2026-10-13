package hygiene

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The installer scripts are executable contracts, same class as the
// README fences: a pasting user's shell either installs a verified
// binary or leaves the working one alone. These tests execute the
// checked-in scripts the way that shell would — bash (dash-clean),
// a PATH-stubbed uname, a sandboxed HOME holding a sentinel binary,
// and a scratch HTTP server standing in for the dist directory — so a
// script that overwrites a good binary after a failed download, or
// sprays requests on a refused platform, fails `go test` instead of
// failing on someone's machine. Substituting the Asset-base literal is
// the only edit; the script body runs verbatim.
//
// The uniform final command name is part of the contract: every
// platform installs as plain `kyacode` (`kyacode.exe` on Windows) no
// matter what the download asset was named — the psInstalledBinary and
// installedBinary assertions below pin that name at every happy path.

const (
	installerAssetBaseLiteral = "https://kyaky.ai/kyacode"
	// The cross-platform guidance must be DERIVED from the asset base (one
	// place to flip): the printed URL can never drift from the download
	// origin. These pin the derivation shapes, byte-exact — the runtime
	// refusal tests derive the expected one-liner from the substituted
	// base URL.
	installerWindowsGuidanceDerivation = "irm $ASSET_BASE/install.ps1 | iex"
	installerPosixGuidanceDerivation   = "curl -fsSL $AssetBase/install.sh | sh"
)

// sidecarFor renders the .sha256 sidecar body for a binary blob the way
// the release workflow's `shasum -a 256` does ("<hex>  <name>\n").
func sidecarFor(bin []byte, name string) []byte {
	return []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(bin), name))
}

// runInstallerScript executes one POSIX installer script body under the
// sandbox: bash, PATH-stubbed uname driven by fakeOS/fakeArch, a
// sandboxed HOME pre-loaded with the sentinel binary at
// ~/.local/bin/kyacode, and the Asset-base literal substituted with
// baseURL. extraEnv rides the child's environment; args land as the
// script's positional arguments (the `sh -s vX.Y.Z` pin).
func runInstallerScript(t *testing.T, body, fakeOS, fakeArch, baseURL string, extraEnv []string, args ...string) (snippetResult, string) {
	t.Helper()
	bashExe, extraDirs := discoverBash(t)
	for _, tool := range []string{"curl", "mktemp", "install"} {
		if !toolAvailable(tool, extraDirs) {
			t.Skipf("%s not available; cannot execute the installer script", tool)
		}
	}
	if !toolAvailable("sha256sum", extraDirs) && !toolAvailable("shasum", extraDirs) {
		t.Skip("neither sha256sum nor shasum available; cannot execute the installer script")
	}

	home := sandboxedHome(t)

	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "uname"), []byte(unameStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if baseURL != "" {
		body = strings.ReplaceAll(body, installerAssetBaseLiteral, baseURL)
	}
	scriptPath := filepath.Join(stub, "kya-installer.sh")
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub dir must shadow the real uname; the wrapper atom carries the
	// msys cygpath preamble and execs the script with the host's /bin/sh
	// (dash on the ubuntu CI leg — the dash-clean claim is exercised, not
	// just asserted).
	wrapperPath := writePathWrapper(t, stub, "run-installer.sh", pathLineAppend, execTailInstallerScript)

	argv := append([]string{wrapperPath, stub, scriptPath}, args...)
	cmd := exec.Command(bashExe, argv...)
	cmd.Env = append(os.Environ(),
		"HOME="+posixify(home),
		"KYA_FAKE_OS="+fakeOS,
		"KYA_FAKE_ARCH="+fakeArch,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	rerr := cmd.Run()
	res := snippetResult{stdout: outBuf.String(), stderr: errBuf.String()}
	if ee, ok := rerr.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	} else if rerr != nil {
		t.Fatalf("running bash: %v", rerr)
	}
	return res, home
}

// runInstallerScriptPathOnly runs the installer script under a stub-ONLY
// PATH with no host tool-availability gate: the child PATH holds only the
// stubs by construction, and install.sh refuses on curl before mktemp,
// the fetches, sha256sum or install are ever reached, so host tool
// presence is irrelevant. HOME is a bare TempDir — no sentinel preload,
// as the inline re-roll had it.
func runInstallerScriptPathOnly(t *testing.T, body, fakeOS, fakeArch, baseURL string) snippetResult {
	t.Helper()
	bashExe, _ := discoverBash(t)
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "uname"), []byte(unameStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if baseURL != "" {
		body = strings.ReplaceAll(body, installerAssetBaseLiteral, baseURL)
	}
	scriptPath := filepath.Join(stub, "kya-installer.sh")
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapperPath := writePathWrapper(t, stub, "run-installer.sh", pathLineRestrict, execTailPathOnly)
	cmd := exec.Command(bashExe, wrapperPath, stub, scriptPath)
	cmd.Env = append(os.Environ(),
		"HOME="+posixify(t.TempDir()),
		"KYA_FAKE_OS="+fakeOS,
		"KYA_FAKE_ARCH="+fakeArch,
	)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	rerr := cmd.Run()
	res := snippetResult{stdout: outBuf.String(), stderr: errBuf.String()}
	if ee, ok := rerr.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	} else if rerr != nil {
		t.Fatalf("running bash: %v", rerr)
	}
	return res
}

// TestInstallerScriptGatesInstallOnFailure mirrors the README-fence gate:
// a verified download installs; any failure (404, truncation, checksum
// mismatch) exits non-zero and leaves the pre-existing binary untouched.
func TestInstallerScriptGatesInstallOnFailure(t *testing.T) {
	body := string(repoFile(t, "scripts/install.sh"))
	binName, sidecarName := fenceFixtureAsset(t)
	fakeBin := bytes.Repeat([]byte("FAKEGO-BINARY\x00\n"), 64)
	correctSidecar := sidecarFor(fakeBin, binName)
	garbageBin := bytes.Repeat([]byte("GARBAGE-not-a-binary\n"), 80)
	wrongSidecar := sidecarFor([]byte("different\n"), binName)

	t.Run("a complete verified download installs the binary", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{
			binName:     {body: fakeBin},
			sidecarName: {body: correctSidecar},
		})
		res, home := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, nil)
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		if got := installedBinary(t, home); !bytes.Equal(got, fakeBin) {
			t.Errorf("installed binary is %d bytes, want the served %d-byte binary", len(got), len(fakeBin))
		}
		if !containsAll(*seen, []string{"/" + binName, "/" + sidecarName}) {
			t.Errorf("server saw %v, want both binary and checksum requests", *seen)
		}
	})

	t.Run("a 404 leaves the installed binary alone and exits non-zero", func(t *testing.T) {
		srv, _ := releaseServer(t, map[string]assetSpec{})
		res, home := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, nil)
		if res.exit == 0 {
			t.Errorf("exit = 0 on a 404; the script must fail loud")
		}
		if res.stderr == "" {
			t.Errorf("no diagnostic on stderr for the 404")
		}
		installedUntouched(t, home)
	})

	t.Run("a truncated download leaves the installed binary alone and exits non-zero", func(t *testing.T) {
		srv, _ := releaseServer(t, map[string]assetSpec{
			binName: {body: fakeBin[:512], truncate: true},
		})
		res, home := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, nil)
		if res.exit == 0 {
			t.Errorf("exit = 0 on a truncated download (curl transfer error)")
		}
		if res.stderr == "" {
			t.Errorf("no diagnostic on stderr for the truncated download")
		}
		installedUntouched(t, home)
	})

	t.Run("a checksum mismatch leaves the installed binary alone and exits non-zero", func(t *testing.T) {
		srv, _ := releaseServer(t, map[string]assetSpec{
			binName:     {body: garbageBin},
			sidecarName: {body: wrongSidecar},
		})
		res, home := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, nil)
		if res.exit == 0 {
			t.Errorf("exit = 0 on a checksum mismatch; the script must refuse to install")
		}
		if res.stderr == "" {
			t.Errorf("no diagnostic on stderr for the checksum mismatch")
		}
		installedUntouched(t, home)
	})
}

// TestInstallerScriptRefusesBeforeAnyRequest: every refusal path fires
// before the first byte hits the network — the server must observe zero
// requests.
func TestInstallerScriptRefusesBeforeAnyRequest(t *testing.T) {
	body := string(repoFile(t, "scripts/install.sh"))
	t.Run("an unmapped architecture is refused up front", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{})
		res, _ := runInstallerScript(t, body, "Darwin", "sparc", srv.URL, nil)
		if res.exit == 0 {
			t.Errorf("exit = 0 on an unsupported architecture; the script must refuse up front")
		}
		if len(*seen) != 0 {
			t.Errorf("script fetched %v on an unsupported architecture; refuse before any request", *seen)
		}
	})
	t.Run("an unsupported OS is refused up front", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{})
		res, _ := runInstallerScript(t, body, "Plan9", "amd64", srv.URL, nil)
		if res.exit == 0 {
			t.Errorf("exit = 0 on an unsupported OS; the script must refuse up front")
		}
		if len(*seen) != 0 {
			t.Errorf("script fetched %v on an unsupported OS; refuse before any request", *seen)
		}
	})
	t.Run("Git Bash on Windows gets the guided Windows refusal", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{})
		res, _ := runInstallerScript(t, body, "MINGW64_NT-10.0-26100", "x86_64", srv.URL, nil)
		if res.exit == 0 {
			t.Errorf("exit = 0 under MINGW; the script must refuse and guide")
		}
		if !strings.Contains(res.stderr+res.stdout, "irm "+srv.URL+"/install.ps1 | iex") {
			t.Errorf("the MINGW refusal must carry the Windows one-liner derived from the asset base %q; got %q", "irm "+srv.URL+"/install.ps1 | iex", res.stderr+res.stdout)
		}
		if len(*seen) != 0 {
			t.Errorf("script fetched %v under MINGW; refuse before any request", *seen)
		}
	})
	t.Run("a malformed version tag is refused without any request", func(t *testing.T) {
		for _, tag := range []string{"v1..2", "v4x", "4x", "v4.", "v4.x", "v4.5.0-rc1"} {
			t.Run(tag, func(t *testing.T) {
				srv, seen := releaseServer(t, map[string]assetSpec{})
				res, _ := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, []string{"KYA_VERSION=" + tag})
				if res.exit == 0 {
					t.Errorf("exit = 0 on a malformed tag; refuse without requesting")
				}
				if !strings.Contains(res.stderr, "bad version tag") {
					t.Errorf("the malformed-tag refusal must name the tag; got %q", res.stderr)
				}
				if len(*seen) != 0 {
					t.Errorf("script fetched %v on a malformed tag; refuse before any request", *seen)
				}
			})
		}
	})
	t.Run("a missing curl is refused by name without any request", func(t *testing.T) {
		// pathOnly: the PATH holds ONLY the stub dir, so curl cannot
		// resolve — the refusal must name the tool and never touch the net.
		srv, seen := releaseServer(t, map[string]assetSpec{})
		res := runInstallerScriptPathOnly(t, body, "Darwin", "arm64", srv.URL)
		if res.exit == 0 {
			t.Errorf("exit = 0 with curl absent; the script must refuse by name")
		}
		if !strings.Contains(res.stderr, "curl") {
			t.Errorf("the missing-curl refusal must name curl; got %q", res.stderr)
		}
		if len(*seen) != 0 {
			t.Errorf("script fetched %v with curl absent; refuse before any request", *seen)
		}
	})
}

// TestInstallerScriptResolvesThePlatformMap: every mapped (os, uname-m)
// pair requests exactly its declared asset — the map table and the
// release grammar must never drift apart.
func TestInstallerScriptResolvesThePlatformMap(t *testing.T) {
	body := string(repoFile(t, "scripts/install.sh"))
	for _, tc := range []struct{ fakeOS, fakeArch, wantAsset string }{
		{"Darwin", "arm64", "kyacode-darwin-arm64"},
		{"Darwin", "x86_64", "kyacode-darwin-amd64"},
		{"Linux", "x86_64", "kyacode-linux-amd64"},
		{"Linux", "aarch64", "kyacode-linux-arm64"},
	} {
		assetIsDeclared(t, tc.wantAsset)
		t.Run(tc.fakeOS+"/"+tc.fakeArch, func(t *testing.T) {
			fakeBin := []byte("bin\n")
			srv, seen := releaseServer(t, map[string]assetSpec{
				tc.wantAsset:             {body: fakeBin},
				tc.wantAsset + ".sha256": {body: sidecarFor(fakeBin, tc.wantAsset)},
			})
			res, _ := runInstallerScript(t, body, tc.fakeOS, tc.fakeArch, srv.URL, nil)
			if res.exit != 0 {
				t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
			}
			if !containsAll(*seen, []string{"/" + tc.wantAsset, "/" + tc.wantAsset + ".sha256"}) {
				t.Errorf("server saw %v, want requests for %s(.sha256)", *seen, tc.wantAsset)
			}
		})
	}
}

// TestInstallerScriptVersionPinsHitVersionedPaths: a pin (env or
// positional) requests v<tag>/<asset>; the positional pin wins over the
// env pin (explicit beats ambient).
func TestInstallerScriptVersionPinsHitVersionedPaths(t *testing.T) {
	body := string(repoFile(t, "scripts/install.sh"))
	binName, sidecarName := fenceFixtureAsset(t)
	fakeBin := []byte("bin\n")
	correctSidecar := sidecarFor(fakeBin, binName)

	assertVersioned := func(t *testing.T, res snippetResult, seen *[]string, tag string) {
		t.Helper()
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		if !containsAll(*seen, []string{"/" + tag + "/" + binName, "/" + tag + "/" + sidecarName}) {
			t.Errorf("server saw %v, want the versioned requests %s/%s(.sha256)", *seen, tag, binName)
		}
	}

	t.Run("KYA_VERSION pins the request path", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{
			"v1.2.3/" + binName:     {body: fakeBin},
			"v1.2.3/" + sidecarName: {body: correctSidecar},
		})
		res, _ := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, []string{"KYA_VERSION=v1.2.3"})
		assertVersioned(t, res, seen, "v1.2.3")
	})
	t.Run("the positional pin hits the same path", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{
			"v1.2.3/" + binName:     {body: fakeBin},
			"v1.2.3/" + sidecarName: {body: correctSidecar},
		})
		res, _ := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, nil, "v1.2.3")
		assertVersioned(t, res, seen, "v1.2.3")
	})
	t.Run("the positional pin wins over the env pin", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{
			"v1.2.3/" + binName:     {body: fakeBin},
			"v1.2.3/" + sidecarName: {body: correctSidecar},
		})
		res, _ := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, []string{"KYA_VERSION=v9.9.9"}, "v1.2.3")
		assertVersioned(t, res, seen, "v1.2.3")
		if containsAll(*seen, []string{"/v9.9.9/" + binName}) {
			t.Errorf("the env pin leaked into the request path despite the positional pin: %v", *seen)
		}
	})
	t.Run("a pinned tag missing from the dist directory fails loud", func(t *testing.T) {
		srv, _ := releaseServer(t, map[string]assetSpec{})
		res, home := runInstallerScript(t, body, "Darwin", "arm64", srv.URL, []string{"KYA_VERSION=v0.0.404"})
		if res.exit == 0 {
			t.Errorf("exit = 0 on a pinned tag that does not exist; must fail loud")
		}
		installedUntouched(t, home)
	})
}

// TestInstallerScriptHonorsTheInstallDirOverride: KYACODE_INSTALL_DIR
// relocates the placement (package-manager/scripted environments).
func TestInstallerScriptHonorsTheInstallDirOverride(t *testing.T) {
	body := string(repoFile(t, "scripts/install.sh"))
	binName, sidecarName := fenceFixtureAsset(t)
	fakeBin := bytes.Repeat([]byte("FAKEGO-BINARY\x00\n"), 64)
	srv, _ := releaseServer(t, map[string]assetSpec{
		binName:     {body: fakeBin},
		sidecarName: {body: sidecarFor(fakeBin, binName)},
	})
	home := t.TempDir()
	custom := filepath.Join(home, "custom-bin")
	res, _ := runInstallerScript(t, body, "Darwin", "arm64", srv.URL,
		[]string{"KYACODE_INSTALL_DIR=" + posixify(custom)})
	if res.exit != 0 {
		t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
	}
	got, err := os.ReadFile(filepath.Join(custom, "kyacode"))
	if err != nil {
		t.Fatalf("the override dir was not used: %v", err)
	}
	if !bytes.Equal(got, fakeBin) {
		t.Errorf("installed binary is %d bytes, want the served %d-byte binary", len(got), len(fakeBin))
	}
}

// TestInstallerScriptIsDashClean: the script must parse under plain dash
// where one exists (the ubuntu CI leg's /bin/sh already IS dash, so the
// executions above double as dash runs there; this pin adds the explicit
// parse check for hosts that carry dash).
func TestInstallerScriptIsDashClean(t *testing.T) {
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Skip("no dash on PATH; cannot parse-check with dash")
	}
	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(scriptPath, []byte(repoFile(t, "scripts/install.sh")), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(dash, "-n", scriptPath)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Errorf("dash -n rejected the installer: %v; stderr: %s", err, errBuf.String())
	}
}

// TestInstallerScriptCarriesTheContractPins: the never-elevate and
// guided-refusal clauses are textual pins — a script that grows a sudo
// path or loses the Windows guidance reds here.
func TestInstallerScriptCarriesTheContractPins(t *testing.T) {
	body := string(repoFile(t, "scripts/install.sh"))
	if strings.Contains(body, "sudo") {
		t.Errorf("the installer mentions sudo — the contract is: never elevate")
	}
	if !strings.Contains(body, installerWindowsGuidanceDerivation) {
		t.Errorf("the installer lost the derived Windows guidance %q", installerWindowsGuidanceDerivation)
	}
	if n := strings.Count(body, `ASSET_BASE="`+installerAssetBaseLiteral+`"`); n != 1 {
		t.Errorf("the Asset base assignment appears %d times, want exactly once (one place to flip)", n)
	}
}

// --- the Windows installer ---

// psInstalledBinary reads the uniform final name on Windows: kyacode.exe
// (invoked as plain `kyacode` from a PATH that carries its directory).
func psInstalledBinary(t *testing.T, installDir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(installDir, "kyacode.exe"))
	if err != nil {
		t.Fatalf("installed binary: %v", err)
	}
	return b
}

func psInstalledUntouched(t *testing.T, installDir string) {
	t.Helper()
	if got := psInstalledBinary(t, installDir); !bytes.Equal(got, sentinelBinary) {
		t.Errorf("the pre-existing binary was destroyed (now %d bytes: %.32q)", len(got), got)
	}
}

// runPowerShellInstaller executes scripts/install.ps1 under Windows
// PowerShell (the shipped production dialect). targetArg may be "" (the
// irm|iex shape: no parameters). env rides the child's environment; when
// it carries no KYACODE_INSTALL_DIR the runner supplies an isolated one
// and pre-loads the sentinel binary at the final name, so every
// failure-state run can assert the working install survived.
func runPowerShellInstaller(t *testing.T, targetArg string, assets map[string]assetSpec, env []string) (snippetResult, string, *[]string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 executes only on Windows (the production dialect is Windows PowerShell 5.1)")
	}
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no powershell.exe on PATH; cannot execute the Windows installer")
	}
	installDir := t.TempDir()
	hasDir := false
	for _, e := range env {
		if strings.HasPrefix(e, "KYACODE_INSTALL_DIR=") {
			installDir = strings.TrimPrefix(e, "KYACODE_INSTALL_DIR=")
			hasDir = true
		}
	}
	if !hasDir {
		env = append(env, "KYACODE_INSTALL_DIR="+installDir)
	}
	// Strip an inherited PSModulePath: on CI the job shell is PowerShell 7,
	// whose module paths poison a child Windows PowerShell 5.1 (its own
	// built-ins stop autoloading). The script pins its PS5 module path
	// itself; the runner strips the inheritance too.
	clean := os.Environ()[:0]
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "PSModulePath=") {
			continue
		}
		clean = append(clean, e)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "kyacode.exe"), sentinelBinary, 0o755); err != nil {
		t.Fatal(err)
	}

	srv, seen := releaseServer(t, assets)
	body := strings.ReplaceAll(string(repoFile(t, "scripts/install.ps1")), installerAssetBaseLiteral, srv.URL)
	scriptPath := filepath.Join(t.TempDir(), "install.ps1")
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	if targetArg != "" {
		argv = append(argv, "-Target", targetArg)
	}
	cmd := exec.Command(ps, argv...)
	cmd.Env = append(clean, env...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	rerr := cmd.Run()
	res := snippetResult{stdout: outBuf.String(), stderr: errBuf.String()}
	if ee, ok := rerr.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	} else if rerr != nil {
		t.Fatalf("running powershell: %v", rerr)
	}
	return res, installDir, seen
}

// psMatrixEnv is the matrix rows' env: an isolated install dir comes from
// the runner; the PATH persistence stays OFF so no test ever writes the
// real user PATH (the registry behavior has its own type-preserving
// save/restore test on the windows leg).
func psMatrixEnv() []string {
	return []string{"KYACODE_SKIP_PATH_PERSIST=1"}
}

// TestPowerShellInstallerGatesInstallOnFailure mirrors the POSIX gate on
// the Windows dialect.
func TestPowerShellInstallerGatesInstallOnFailure(t *testing.T) {
	asset := "kyacode-windows-amd64.exe"
	fakeBin := bytes.Repeat([]byte("FAKEGO-BINARY\x00\n"), 64)
	garbageBin := bytes.Repeat([]byte("GARBAGE-not-a-binary\n"), 80)

	t.Run("a complete verified download installs the binary", func(t *testing.T) {
		res, installDir, seen := runPowerShellInstaller(t, "", map[string]assetSpec{
			asset:             {body: fakeBin},
			asset + ".sha256": {body: sidecarFor(fakeBin, asset)},
		}, psMatrixEnv())
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		if got := psInstalledBinary(t, installDir); !bytes.Equal(got, fakeBin) {
			t.Errorf("installed binary is %d bytes, want the served %d-byte binary", len(got), len(fakeBin))
		}
		if !containsAll(*seen, []string{"/" + asset, "/" + asset + ".sha256"}) {
			t.Errorf("server saw %v, want both binary and checksum requests", *seen)
		}
	})

	t.Run("a 404 leaves the installed binary alone and exits non-zero", func(t *testing.T) {
		res, installDir, _ := runPowerShellInstaller(t, "", map[string]assetSpec{}, psMatrixEnv())
		if res.exit == 0 {
			t.Errorf("exit = 0 on a 404; the script must fail loud")
		}
		if res.stderr == "" && res.stdout == "" {
			t.Errorf("no diagnostic for the 404")
		}
		psInstalledUntouched(t, installDir)
	})

	t.Run("a truncated download leaves the installed binary alone and exits non-zero", func(t *testing.T) {
		res, installDir, _ := runPowerShellInstaller(t, "", map[string]assetSpec{
			asset: {body: fakeBin[:512], truncate: true},
		}, psMatrixEnv())
		if res.exit == 0 {
			t.Errorf("exit = 0 on a truncated download (transfer error)")
		}
		psInstalledUntouched(t, installDir)
	})

	t.Run("a checksum mismatch leaves the installed binary alone and exits non-zero", func(t *testing.T) {
		res, installDir, _ := runPowerShellInstaller(t, "", map[string]assetSpec{
			asset:             {body: garbageBin},
			asset + ".sha256": {body: sidecarFor([]byte("different\n"), asset)},
		}, psMatrixEnv())
		if res.exit == 0 {
			t.Errorf("exit = 0 on a checksum mismatch; the script must refuse to install")
		}
		psInstalledUntouched(t, installDir)
	})
}

// TestPowerShellInstallerRefusesBeforeAnyRequest: refusals fire before
// the first byte hits the network.
func TestPowerShellInstallerRefusesBeforeAnyRequest(t *testing.T) {
	for _, arch := range []string{"ARM64", "X86"} {
		t.Run("unsupported architecture "+arch, func(t *testing.T) {
			res, _, seen := runPowerShellInstaller(t, "", map[string]assetSpec{},
				[]string{"KYACODE_SKIP_PATH_PERSIST=1", "PROCESSOR_ARCHITECTURE=" + arch})
			if res.exit == 0 {
				t.Errorf("exit = 0 on an unsupported architecture; refuse up front")
			}
			if len(*seen) != 0 {
				t.Errorf("script fetched %v on an unsupported architecture; refuse before any request", *seen)
			}
		})
	}
	t.Run("a malformed version tag is refused without any request", func(t *testing.T) {
		for _, tag := range []string{"v1..2", "v4x", "4x", "v4.", "v4.x", "v4.5.0-rc1"} {
			t.Run(tag, func(t *testing.T) {
				res, _, seen := runPowerShellInstaller(t, tag, map[string]assetSpec{}, psMatrixEnv())
				if res.exit == 0 {
					t.Errorf("exit = 0 on a malformed tag; refuse without requesting")
				}
				if !strings.Contains(res.stderr, "bad version tag") {
					t.Errorf("the malformed-tag refusal must name the tag; got %q", res.stderr)
				}
				if len(*seen) != 0 {
					t.Errorf("script fetched %v on a malformed tag; refuse before any request", *seen)
				}
			})
		}
	})
	t.Run("a malformed env pin is refused without any request", func(t *testing.T) {
		res, _, seen := runPowerShellInstaller(t, "", map[string]assetSpec{},
			[]string{"KYACODE_SKIP_PATH_PERSIST=1", "KYA_VERSION=v1..2"})
		if res.exit == 0 {
			t.Errorf("exit = 0 on a malformed env pin; refuse without requesting")
		}
		if len(*seen) != 0 {
			t.Errorf("script fetched %v on a malformed env pin; refuse before any request", *seen)
		}
	})
}

// TestPowerShellInstallerVersionPinsHitVersionedPaths: -Target and the
// env pin both request v<tag>/<asset>; the explicit -Target wins over
// the env (explicit beats ambient).
func TestPowerShellInstallerVersionPinsHitVersionedPaths(t *testing.T) {
	asset := "kyacode-windows-amd64.exe"
	fakeBin := []byte("bin\n")

	t.Run("-Target pins the request path", func(t *testing.T) {
		res, _, seen := runPowerShellInstaller(t, "v1.2.3", map[string]assetSpec{
			"v1.2.3/" + asset:             {body: fakeBin},
			"v1.2.3/" + asset + ".sha256": {body: sidecarFor(fakeBin, asset)},
		}, psMatrixEnv())
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		if !containsAll(*seen, []string{"/v1.2.3/" + asset, "/v1.2.3/" + asset + ".sha256"}) {
			t.Errorf("server saw %v, want the versioned requests", *seen)
		}
	})
	t.Run("the env pin hits the same path", func(t *testing.T) {
		res, _, seen := runPowerShellInstaller(t, "", map[string]assetSpec{
			"v1.2.3/" + asset:             {body: fakeBin},
			"v1.2.3/" + asset + ".sha256": {body: sidecarFor(fakeBin, asset)},
		}, []string{"KYACODE_SKIP_PATH_PERSIST=1", "KYA_VERSION=v1.2.3"})
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		if !containsAll(*seen, []string{"/v1.2.3/" + asset}) {
			t.Errorf("server saw %v, want the versioned requests", *seen)
		}
	})
	t.Run("-Target wins over the env pin", func(t *testing.T) {
		res, _, seen := runPowerShellInstaller(t, "v1.2.3", map[string]assetSpec{
			"v1.2.3/" + asset:             {body: fakeBin},
			"v1.2.3/" + asset + ".sha256": {body: sidecarFor(fakeBin, asset)},
		}, []string{"KYACODE_SKIP_PATH_PERSIST=1", "KYA_VERSION=v9.9.9"})
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		if !containsAll(*seen, []string{"/v1.2.3/" + asset}) {
			t.Errorf("server saw %v, want the versioned requests", *seen)
		}
		if containsAll(*seen, []string{"/v9.9.9/" + asset}) {
			t.Errorf("the env pin leaked into the request path despite -Target: %v", *seen)
		}
	})
	t.Run("a pinned tag missing from the dist directory fails loud", func(t *testing.T) {
		res, installDir, _ := runPowerShellInstaller(t, "v0.0.404", map[string]assetSpec{}, psMatrixEnv())
		if res.exit == 0 {
			t.Errorf("exit = 0 on a pinned tag that does not exist; must fail loud")
		}
		psInstalledUntouched(t, installDir)
	})
}

// TestPowerShellInstallerCarriesTheContractPins: the textual contract —
// every Invoke-WebRequest carries -UseBasicParsing (the KB5074596 belt),
// no bare `exit` (an exit inside piped iex kills the user's shell), the
// strict-mode line, the throw-based refusal mechanism, and the guided
// POSIX one-liner for non-Windows hosts.
func TestPowerShellInstallerCarriesTheContractPins(t *testing.T) {
	body := string(repoFile(t, "scripts/install.ps1"))
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Invoke-WebRequest") && !strings.Contains(trimmed, "-UseBasicParsing") {
			t.Errorf("line %d calls Invoke-WebRequest without -UseBasicParsing: %q", i+1, trimmed)
		}
		if strings.HasPrefix(trimmed, "exit") {
			t.Errorf("line %d starts with exit — refusals must throw, never exit: %q", i+1, trimmed)
		}
	}
	if !strings.Contains(body, "Set-StrictMode") {
		t.Errorf("the installer lost Set-StrictMode")
	}
	if !strings.Contains(body, "throw") {
		t.Errorf("the installer lost its throw-based refusal mechanism")
	}
	if !strings.Contains(body, installerPosixGuidanceDerivation) {
		t.Errorf("the installer lost the derived POSIX refusal one-liner")
	}
	if n := strings.Count(body, `$AssetBase = '`+installerAssetBaseLiteral+`'`); n != 1 {
		t.Errorf("the Asset base assignment appears %d times, want exactly once (one place to flip)", n)
	}
}
