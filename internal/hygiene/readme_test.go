package hygiene

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The README install/upgrade snippets are executable contracts: their
// whole point is that a pasting user's shell either installs a verified
// binary or leaves the working one alone. These tests execute the
// checked-in fences the way that shell would — bash, a PATH-stubbed
// uname/gh, a sandboxed HOME holding a sentinel binary, and a scratch
// HTTP server standing in for the release host — so a fence that
// overwrites a good binary after a failed download fails `go test`
// instead of failing on someone's machine. Substituting the release
// base-URL literal is the only edit; the fence body runs verbatim.

const (
	releaseBaseLiteral = "https://github.com/kyaky/kyacode/releases/latest/download"
	tokenFenceMarker   = releaseBaseLiteral // only the token-aware fence builds this URL
	ghFenceMarker      = "gh release download"
)

// sentinelBinary is the good binary a sandboxed HOME starts with: any
// subtest that ends with anything else at that path destroyed a working
// install — exactly the failure class the fences exist to prevent.
var sentinelBinary = []byte("OLD-BINARY-SENTINEL\x1a\x02\x03\x04\n")

func fenceContaining(t *testing.T, marker string) string {
	t.Helper()
	for _, f := range rawFences(t) {
		if strings.Contains(f, marker) {
			return f
		}
	}
	t.Fatalf("README has no fenced snippet containing %q", marker)
	return ""
}

// rawFences returns every ```-fenced code block body in README.md, in
// order.
func rawFences(t *testing.T) []string {
	t.Helper()
	s := string(repoFile(t, "README.md"))
	var fences []string
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			return fences
		}
		nl := strings.IndexByte(s[start:], '\n')
		if nl < 0 {
			return fences
		}
		bodyStart := start + nl + 1
		end := strings.Index(s[bodyStart:], "```")
		if end < 0 {
			return fences
		}
		fences = append(fences, s[bodyStart:bodyStart+end])
		s = s[bodyStart+end:]
	}
}

type assetSpec struct {
	body     []byte
	notFound bool
	truncate bool // advertise a large Content-Length, write part of body, abort
}

// releaseServer stands in for the GitHub release host. It records every
// requested path so tests can observe which assets a fetched snippet
// asked for.
func releaseServer(t *testing.T, assets map[string]assetSpec) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.URL.Path)
		mu.Unlock()
		a, ok := assets[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok || a.notFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if a.truncate {
			w.Header().Set("Content-Length", "65536")
			w.Write(a.body)
			panic(http.ErrAbortHandler) // hard connection abort mid-transfer
		}
		w.Write(a.body)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func containsAll(hay []string, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range hay {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type snippetResult struct {
	exit   int
	stdout string
	stderr string
	ghArgv string // newline-joined argv of every stubbed gh invocation
}

// runReadmeSnippet executes one README fence under bash with a sandboxed
// HOME (pre-loaded with the sentinel binary at ~/.local/bin/kyacode),
// PATH-stubbed uname/gh driven by fakeOS/fakeArch, and the release
// base-URL literal substituted with baseURL ("" keeps the fence
// unedited — the gh fence never touches it).
// discoverBash finds a bash executable plus the directories holding its
// bundled coreutils/curl: hosts whose PATH already carries bash (CI
// runners, a Git Bash session) return it unchanged; a Windows host that
// runs `go test` from a console without bash on PATH still gets the
// snippets executed via the standard Git-for-Windows install.
func discoverBash(t *testing.T) (bashExe string, extraDirs []string) {
	t.Helper()
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	for _, root := range []string{
		`C:\Program Files\Git`,
		`C:\Program Files (x86)\Git`,
	} {
		bin := filepath.Join(root, "bin", "bash.exe")
		usr := filepath.Join(root, "usr", "bin")
		if _, err := os.Stat(bin); err != nil {
			continue
		}
		if _, err := os.Stat(usr); err != nil {
			continue
		}
		return bin, []string{usr, filepath.Join(root, "mingw64", "bin")}
	}
	t.Skip("no bash on PATH and no Git-for-Windows install found; cannot execute README snippets")
	return "", nil
}

func toolAvailable(tool string, extraDirs []string) bool {
	if _, err := exec.LookPath(tool); err == nil {
		return true
	}
	for _, d := range extraDirs {
		if _, err := os.Stat(filepath.Join(d, tool+".exe")); err == nil {
			return true
		}
	}
	return false
}

func runReadmeSnippet(t *testing.T, fence, fakeOS, fakeArch, baseURL string) (snippetResult, string) {
	t.Helper()
	bashExe, extraDirs := discoverBash(t)
	for _, tool := range []string{"curl", "mktemp", "install"} {
		if !toolAvailable(tool, extraDirs) {
			t.Skipf("%s not available; cannot execute README snippets", tool)
		}
	}
	if !toolAvailable("sha256sum", extraDirs) && !toolAvailable("shasum", extraDirs) {
		t.Skip("neither sha256sum nor shasum available; cannot execute README snippets")
	}

	home := sandboxedHome(t)

	stub := t.TempDir()
	ghLog := filepath.Join(stub, "gh-argv.log")
	if err := os.WriteFile(filepath.Join(stub, "uname"), []byte(unameStub), 0o755); err != nil {
		t.Fatal(err)
	}
	// gh logs its argv and plays a token carry for `auth token`; the log
	// path is Windows-form under a go-test host, so cygpath it first.
	gh := "#!/bin/sh\n" +
		"log=\"$KYA_GH_LOG\"\n" +
		"command -v cygpath >/dev/null 2>&1 && log=\"$(cygpath -u -- \"$KYA_GH_LOG\")\"\n" +
		"printf '%s\\n' \"$*\" >> \"$log\"\n" +
		"case \"$1 $2\" in \"auth token\") echo kya-test-token;; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(stub, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub dir must shadow real uname/gh; the wrapper atom carries the
	// msys cygpath preamble.
	wrapperPath := writePathWrapper(t, stub, "run-snippet.sh", pathLineAppend, execTailReadmeFence)

	if baseURL != "" {
		fence = strings.ReplaceAll(fence, releaseBaseLiteral, baseURL)
	}

	cmd := exec.Command(bashExe, wrapperPath, stub, fence)
	cmd.Env = append(os.Environ(),
		"HOME="+posixify(home),
		"KYA_FAKE_OS="+fakeOS,
		"KYA_FAKE_ARCH="+fakeArch,
		"KYA_GH_LOG="+ghLog,
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
	if b, err := os.ReadFile(ghLog); err == nil {
		res.ghArgv = string(b)
	}
	return res, home
}

func installedBinary(t *testing.T, home string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".local", "bin", "kyacode"))
	if err != nil {
		t.Fatalf("installed binary: %v", err)
	}
	return b
}

func installedUntouched(t *testing.T, home string) {
	t.Helper()
	if got := installedBinary(t, home); !bytes.Equal(got, sentinelBinary) {
		t.Errorf("the pre-existing binary was destroyed (now %d bytes: %.32q)", len(got), got)
	}
}

// fenceFixtureAsset derives the darwin/arm64 asset names the fence
// fixtures exercise, from the release grammar's owner (releaseTargets —
// the release.yml matrix): a producer-side rename or a dropped matrix
// target re-points these fixtures instead of stranding them on a 404'd
// asset. The uname inputs to runReadmeSnippet ("Darwin", "arm64") stay
// hardcoded: they are the fence's runtime inputs, not asset names.
func fenceFixtureAsset(t *testing.T) (binName, sidecarName string) {
	for _, tgt := range releaseTargets(t) {
		if tgt.goos == "darwin" && tgt.goarch == "arm64" {
			n := assetName(t, tgt)
			return n, n + ".sha256"
		}
	}
	t.Fatal("the release matrix lost the darwin/arm64 row the fence fixtures exercise")
	return "", ""
}

// assetIsDeclared asserts a candidate asset name is derivable from the
// release grammar (the matrix + ext rule) — the derivation guard for
// tables whose rows are uname-mapped fence behavior.
func assetIsDeclared(t *testing.T, name string) {
	t.Helper()
	for _, tgt := range releaseTargets(t) {
		if assetName(t, tgt) == name {
			return
		}
	}
	t.Errorf("asset %q is not derivable from the release matrix — the fence table drifted from the producer", name)
}

func TestReadmeTokenFetchSnippetGatesInstallOnFailure(t *testing.T) {
	fence := fenceContaining(t, tokenFenceMarker)
	binName, sidecarName := fenceFixtureAsset(t)
	fakeBin := bytes.Repeat([]byte("FAKEGO-BINARY\x00\n"), 64)
	correctSidecar := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(fakeBin), binName))
	garbageBin := bytes.Repeat([]byte("GARBAGE-not-a-binary\n"), 80)
	wrongSidecar := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("different\n")), binName))

	t.Run("a complete verified download installs the binary", func(t *testing.T) {
		srv, seen := releaseServer(t, map[string]assetSpec{
			binName:     {body: fakeBin},
			sidecarName: {body: correctSidecar},
		})
		res, home := runReadmeSnippet(t, fence, "Darwin", "arm64", srv.URL)
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
		res, home := runReadmeSnippet(t, fence, "Darwin", "arm64", srv.URL)
		if res.exit == 0 {
			t.Errorf("exit = 0 on a 404; the snippet must fail loud")
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
		res, home := runReadmeSnippet(t, fence, "Darwin", "arm64", srv.URL)
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
		res, home := runReadmeSnippet(t, fence, "Darwin", "arm64", srv.URL)
		if res.exit == 0 {
			t.Errorf("exit = 0 on a checksum mismatch; the snippet must refuse to install")
		}
		if res.stderr == "" {
			t.Errorf("no diagnostic on stderr for the checksum mismatch")
		}
		installedUntouched(t, home)
	})
}

func TestReadmeDownloadSnippetsResolveTheRightAssetPattern(t *testing.T) {
	t.Run("token fence requests the uname-derived asset", func(t *testing.T) {
		for _, tc := range []struct{ fakeOS, fakeArch, wantAsset string }{
			{"Darwin", "arm64", "kyacode-darwin-arm64"},
			{"Linux", "x86_64", "kyacode-linux-amd64"},
		} {
			assetIsDeclared(t, tc.wantAsset)
			t.Run(tc.fakeOS+"/"+tc.fakeArch, func(t *testing.T) {
				fence := fenceContaining(t, tokenFenceMarker)
				srv, seen := releaseServer(t, map[string]assetSpec{
					tc.wantAsset:             {body: []byte("bin\n")},
					tc.wantAsset + ".sha256": {body: []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("bin\n")), tc.wantAsset))},
				})
				res, _ := runReadmeSnippet(t, fence, tc.fakeOS, tc.fakeArch, srv.URL)
				if res.exit != 0 {
					t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
				}
				if !containsAll(*seen, []string{"/" + tc.wantAsset, "/" + tc.wantAsset + ".sha256"}) {
					t.Errorf("server saw %v, want requests for %s(.sha256)", *seen, tc.wantAsset)
				}
			})
		}
	})

	t.Run("token fence refuses an unmapped architecture before fetching", func(t *testing.T) {
		fence := fenceContaining(t, tokenFenceMarker)
		srv, seen := releaseServer(t, map[string]assetSpec{})
		res, _ := runReadmeSnippet(t, fence, "Darwin", "sparc", srv.URL)
		if res.exit == 0 {
			t.Errorf("exit = 0 on an unsupported architecture; the fence must refuse up front")
		}
		if len(*seen) != 0 {
			t.Errorf("fence fetched %v on an unsupported architecture; refuse before any request", *seen)
		}
	})

	t.Run("gh fence requests an arch-qualified exact pattern", func(t *testing.T) {
		fence := fenceContaining(t, ghFenceMarker)
		res, _ := runReadmeSnippet(t, fence, "Darwin", "arm64", "")
		if res.exit != 0 {
			t.Fatalf("exit = %d; stderr: %s", res.exit, res.stderr)
		}
		binName, _ := fenceFixtureAsset(t)
		if !strings.Contains(res.ghArgv, "--pattern "+binName+"*") {
			t.Errorf("gh argv = %q, want --pattern %s*", res.ghArgv, binName)
		}
	})

	t.Run("gh fence refuses an unmapped architecture instead of widening the pattern", func(t *testing.T) {
		fence := fenceContaining(t, ghFenceMarker)
		res, _ := runReadmeSnippet(t, fence, "Darwin", "sparc", "")
		if res.exit == 0 {
			t.Errorf("exit = 0 on an unsupported architecture; empty case-fallback widened the glob")
		}
		if strings.Contains(res.ghArgv, "kyacode-darwin-*") {
			t.Errorf("gh argv = %q — a widened globout must never reach gh", res.ghArgv)
		}
	})
}
