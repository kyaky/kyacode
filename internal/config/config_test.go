package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kyacode/internal/gateway"
)

// withHome isolates a test in its own fake $HOME so config paths stay
// hermetic; both Dir() and TokenPath() derive from os.UserHomeDir().
// USERPROFILE must be covered too: on Windows os.UserHomeDir() reads
// USERPROFILE and ignores HOME, so without it every config test would
// read and mutate the developer's real ~/.kyacode.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvGateway, "")
	return home
}

func TestSaveTokenForces0600DespiteLaxUmask(t *testing.T) {
	home := withHome(t)

	old := umaskForTest(0o002)
	defer umaskForTest(old)

	if err := SaveToken("sk-test-123456"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	p := filepath.Join(home, ".kyacode", "token")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 && !chmodIsNoop() {
		t.Fatalf("token mode = %o, want 600 (umask must not dilute it)", got)
	}

	// Re-save over a pre-existing lax-mode file: the chmod must restore
	// 0600 there too, not only on fresh writes.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken("sk-test-123457"); err != nil {
		t.Fatalf("SaveToken re-save: %v", err)
	}
	fi, err = os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 && !chmodIsNoop() {
		t.Fatalf("re-saved token mode = %o, want 600", got)
	}
}

// chmodIsNoop reports whether this platform cannot faithfully represent
// POSIX permission bits: Go's os.Chmod on Windows maps non-read-only modes
// to 0666 and read-only to 0444, and file mode reporting always derives
// 0666 for writable files (the three low set bits are unrepresentable in
// the NT ACLs Go manipulates). The 0600-by-design assertions degrade to
// save-succeeds checks there.
func chmodIsNoop() bool { return runtime.GOOS == "windows" }

func TestLoadCorruptConfigIsHardError(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "config.json")
	if err := os.WriteFile(corrupt, []byte("{{{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load must fail hard on a corrupt config, not fall back to the default gateway")
	}
}

// Load must never fall back to DefaultGateway when the user's config
// cannot be resolved; a $HOME-less process is the sharpest case (the
// root path is unresolvable, so no per-user gateway can be known).
func TestLoadFailsClosedWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv(EnvGateway, "")
	if _, err := Load(); err == nil {
		t.Fatal("Load must fail closed when it cannot resolve any gateway")
	}
}

func TestDeleteTokenErrorFidelity(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "token")

	err := DeleteToken()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("DeleteToken on missing file = %v, want a not-found error", err)
	}

	// Root bypasses DAC permissions, so the permission-error branch is only
	// observable as a non-root user. Windows dir 0500 does not deny unlink
	// either (chmod 0500 maps to flags that do not stop RemoveFile), so
	// this branch stays unix-only.
	if os.Getuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("chmod-on-directory unlink denial is not testable on this platform")
	}
	// Seed the token FIRST (dir still writable), then make the dir
	// read-only so unlink fails for a reason that is NOT not-found.
	if err := os.WriteFile(p, []byte("k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	err = DeleteToken()
	if err == nil || strings.Contains(err.Error(), "not found") {
		t.Fatalf("DeleteToken with unlink failure = %v, want a non-'not found' error (real cause must not be masked)", err)
	}
}

func TestEnvGatewayOverridesAndTrims(t *testing.T) {
	withHome(t)
	t.Setenv(EnvGateway, "  https://gw.example  ")
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Gateway != "https://gw.example" {
		t.Fatalf("gateway = %q", s.Gateway)
	}
}

// normalizeGatewayForTest exposes the exported NormalizeGateway as a single
// canonical call for table tests.
func normalizeGatewayForTest(u string) string { return NormalizeGateway(u) }

// A /v1-suffixed gateway URL (the habit from OpenAI-style base URLs) must
// normalize to the bare base: OpenAIBase appends /v1 itself, so the stored
// form with the suffix would double it into /v1/v1/… (surfaced as Claude
// Code's misleading "model may not exist" message on a 404 from
// /v1/v1/messages).
func TestV1SuffixStripsToTheBareBase(t *testing.T) {
	cases := map[string]string{
		"https://gw.example/v1/":   "https://gw.example",
		"https://gw.example/v1":    "https://gw.example",
		"https://gw.example":       "https://gw.example",
		"https://gw.example/v1/v1": "https://gw.example/v1",
		"http://127.0.0.1:8080/v1": "http://127.0.0.1:8080",
	}
	for in, want := range cases {
		if got := normalizeGatewayForTest(in); got != want {
			t.Errorf("strip /v1: %q => %q, want %q", in, got, want)
		}
	}

	// End to end: a stored /v1 URL loads as the bare base.
	home := withHome(t)
	t.Setenv(EnvGateway, "")
	if err := os.MkdirAll(filepath.Join(home, ".kyacode"), 0o700); err != nil {
		t.Fatal(err)
	}
	in := `{"gateway":"https://inference.example/v1"}`
	if err := os.WriteFile(filepath.Join(home, ".kyacode", "config.json"), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Gateway != "https://inference.example" {
		t.Fatalf("gateway = %q, want suffix stripped", s.Gateway)
	}
	// The append half of the /v1 rule lives on the launch side
	// (harness.Endpoint.OpenAIBase, pinned there).
}

// F6 residue: the /v1 ROUND-TRIP bridge. config owns INPUT normalization
// (strip a trailing /v1 so a pasted OpenAI-style url stores the base);
// gateway.V1Base owns the /v1 JOIN (exactly once, idempotent for a base
// that already carries it). Ownership of the two transforms is UNCHANGED
// (ticket-05 AC-2: config keeps input normalization — a different
// direction); this pins only that they COMPOSE: stable (idempotent where
// clean) and never reintroducing the doubled /v1/v1 that 404s, outside the
// documented deliberate pass-through (a stored url that already ends in
// /v1/v1 — gateway.go: "a base ending in /v1/v1 stays as-is"). A mutation
// of either transform half moves the composed form and goes red.
func TestGatewayV1RoundTripComposesStably(t *testing.T) {
	// store-accepted gateway urls (the ValidateGateway-accepted shapes are
	// https:// bases; http:// needs the loopback opt-in and adds nothing to
	// the /v1 composition, so the table is https).
	cases := []struct {
		u    string // the stored gateway url
		want string // exact V1Base(NormalizeGateway(u))
	}{
		{"https://gw.example", "https://gw.example/v1"},
		{"https://gw.example/", "https://gw.example/v1"},
		{"https://gw.example/v1", "https://gw.example/v1"},
		{"https://gw.example/sub", "https://gw.example/sub/v1"},
		{"https://gw.example/sub/v1", "https://gw.example/sub/v1"},
		// The deliberate pass-through class: the stored url already ends in
		// /v1/v1, so V1Base's own pass-through may keep a double /v1.
		{"https://gw.example/v1/v1", "https://gw.example/v1"},
		{"https://gw.example/v1/v1/v1", "https://gw.example/v1/v1"},
	}
	for _, c := range cases {
		got := gateway.V1Base(NormalizeGateway(c.u))
		if got != c.want {
			t.Errorf("V1Base(NormalizeGateway(%q)) = %q, want %q", c.u, got, c.want)
		}
		// The doubled /v1/v1 is legal ONLY for the documented deliberate
		// pass-through (a stored url that already ends in /v1/v1); every
		// other url must compose to a single /v1 (the 404 the normalization
		// exists to prevent).
		if !strings.HasSuffix(c.u, "/v1/v1") && strings.Contains(got, "/v1/v1") {
			t.Errorf("V1Base(NormalizeGateway(%q)) = %q yielded a /v1/v1 join", c.u, got)
		}
		// Stability for the clean class: a second full pass settles to the
		// same form (the triple-/v1 deliberate row is the documented
		// non-stable boundary — each pass strips one /v1).
		if !strings.Contains(got, "/v1/v1") {
			if again := gateway.V1Base(NormalizeGateway(got)); again != got {
				t.Errorf("round-trip not stable for %q: T=%q T(T)=%q", c.u, got, again)
			}
		}
	}
}

// Plaintext gateways must be refused by default: the stored token travels
// in an Authorization header. An opt-in alone is not enough either — the
// opt-in only exempts loopback targets (see
// TestInsecureOptInOnlyExemptsLoopback).
func TestLoadRefusesPlaintextGateway(t *testing.T) {
	withHome(t)
	t.Setenv(EnvGateway, "http://gw.example")
	if _, err := Load(); err == nil {
		t.Fatal("Load must refuse http:// gateway unless KYA_GATEWAY_INSECURE=1")
	}
	t.Setenv(EnvGatewayInsecure, "1")
	if _, err := Load(); err == nil {
		t.Fatal("a remote http:// gateway must stay refused even with the opt-in")
	}
	t.Setenv(EnvGateway, "http://127.0.0.1:8080")
	if _, err := Load(); err != nil {
		t.Fatalf("loopback http with the opt-in must load: %v", err)
	}
}

// Token()'s read-failure taxonomy: a missing token file keeps the loved
// friendly wording byte-for-byte, while a file that exists but cannot be
// read (unreadable on a multi-user host) must name its cause and must NOT
// point the operator back at `kyacode auth` — writes need the same access
// the read just failed on, so that remedy fails identically.
func TestTokenErrorTaxonomy(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "token")

	// Missing file: the exact friendly form (message-shape pin).
	_, err := Token()
	if err == nil || err.Error() != "no token stored (run `kyacode auth`)" {
		t.Fatalf("missing token error = %v, want the exact friendly form", err)
	}

	// Unreadable-existing-file fixture: mode 000 denies the owner's read
	// on POSIX DAC (root bypasses it; Windows chmod cannot express this
	// denial — the read-only attribute it maps to does not block reads),
	// so the fixture stays unix-non-root like the other DAC fixtures here.
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("unreadable-file fixture needs POSIX DAC as non-root")
	}
	if err := os.WriteFile(p, []byte("sk-test-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o600)
	_, err = Token()
	if err == nil {
		t.Fatal("reading an unreadable token must fail")
	}
	if !strings.HasPrefix(err.Error(), "reading token:") {
		t.Errorf("unreadable-file error = %v, want the cause-naming \"reading token:\" form", err)
	}
	if strings.Contains(err.Error(), "no token stored") {
		t.Errorf("unreadable-file error must not collapse into the not-stored message: %v", err)
	}
}

// The insecure opt-in is scoped to loopback: a remote plaintext gateway
// stays refused even with KYA_GATEWAY_INSECURE set, so the escape hatch
// can never widen the key's exposure beyond the local machine
// (readme-audit issue: kya-gateway-insecure-not-loopback-only).
func TestInsecureOptInOnlyExemptsLoopback(t *testing.T) {
	withHome(t)
	t.Setenv(EnvGatewayInsecure, "1")

	loop := []string{
		"http://127.0.0.1:8080",
		"http://localhost:3000",
		"http://[::1]:9000",
		"http://127.192.4.7",
		"http://box.localhost",
	}
	for _, u := range loop {
		t.Setenv(EnvGateway, u)
		if _, err := Load(); err != nil {
			t.Errorf("loopback %q must be allowed under the opt-in: %v", u, err)
		}
	}

	remote := []string{
		"http://gw.example",            // remote hostname
		"http://10.0.0.5",              // private but not loopback
		"http://192.168.1.1",           // private but not loopback
		"http://gw.example/@127.0.0.1", // loopback only in the path — must not count
	}
	for _, u := range remote {
		t.Setenv(EnvGateway, u)
		if _, err := Load(); err == nil {
			t.Errorf("non-loopback %q must stay refused even with the opt-in", u)
		}
	}

	// No opt-in: even loopback http is refused (default stays strict).
	t.Setenv(EnvGatewayInsecure, "")
	t.Setenv(EnvGateway, "http://127.0.0.1:8080")
	if _, err := Load(); err == nil {
		t.Fatal("loopback http must still require the opt-in")
	}
}

// Save persists config.json at 0600 — including over a pre-existing
// lax-mode file (os.WriteFile leaves the old mode untouched; the explicit
// chmod is the fix for readme-audit issue: config-save-mode-0600).
func TestSaveForces0600OnPreexistingLaxFile(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"gateway":"http://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(Setting{Gateway: "https://gw.example"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 && !chmodIsNoop() {
		t.Fatalf("config.json mode = %o, want 600 (chmod must cover pre-existing files)", got)
	}
	// Contents still parse as the saved setting.
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "https://gw.example") {
		t.Fatalf("saved content lost: %s", b)
	}
}

// A failed Save must leave the previous config.json byte-for-byte intact
// (a corrupted store hard-errors every later command) and must not leave
// temp residue that would accumulate in ~/.kyacode. Fixture: a
// write-denied directory stops the temp-file create; chmod-on-directory
// unlink/write denial is only expressible on POSIX DAC as non-root, so
// the branch stays unix-gated like the other DAC fixtures here.
func TestSaveFailureLeavesPreviousFileIntactAndNoResidue(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	good := `{"gateway":"https://gw.example"}` + "\n"
	if err := os.WriteFile(p, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("write-denied-directory fixture needs POSIX DAC as non-root")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	if err := Save(Setting{Gateway: "https://new.example"}); err == nil {
		t.Fatal("Save into a write-denied directory must fail")
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("previous config file vanished after a failed save: %v", err)
	}
	if string(b) != good {
		t.Fatalf("failed save damaged the old file: %q, want %q", b, good)
	}
	residue, err := filepath.Glob(filepath.Join(dir, "config.json.tmp*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(residue) != 0 {
		t.Fatalf("failed save left temp residue: %v", residue)
	}
}

// The residue contract must hold on the AFTER-create failure branch too
// — the write-denied-directory fixture above fails before any temp can
// exist, so its no-residue glob is vacuous proof for the later steps.
// Here the failure lands after the temp is written: the destination name
// is held by a directory, so the rename cannot land (POSIX EISDIR,
// Windows access-denied) and saveAtomic must reclaim the temp instead of
// leaving its key/config bytes behind.
func TestSaveRenameFailureRemovesTempFile(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "config.json")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dest) })

	if err := Save(Setting{Gateway: "https://gw.example"}); err == nil {
		t.Fatal("Save must fail when the destination name is occupied by a directory")
	}
	residue, _ := filepath.Glob(filepath.Join(dir, "config.json.tmp*"))
	if len(residue) != 0 {
		t.Fatalf("failed rename left temp residue: %v", residue)
	}
}

// Control-character tokens must be rejected at the boundary: they crash
// net/http header construction (CR/LF) and corrupt auth.json embedding.
func TestTokenShapeValidation(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".kyacode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "token")

	cases := map[string]string{
		"control char": "sk-ok\x01token",
		"CR":           "sk-ok\rtoken",
		"LF inside":    "sk-ok\ntoken",
		"too short":    "abc",
	}
	for name, tok := range cases {
		// Assert through the public seam only: a regression that drops
		// SaveToken's validation call must fire this row, and Errorf (not
		// Fatal) surfaces every accepting shape in one run. Provenance:
		// deleting SaveToken's validation guard kept the whole config
		// suite green including this test's old inverted form (mutation
		// verified 2026-09-16 by two independent judges) — the nested
		// both-accept shape was the gap.
		if err := SaveToken(tok); err == nil {
			t.Errorf("%s: SaveToken accepted %q", name, tok)
		}
	}
	// A valid token still works and reads back.
	if err := SaveToken("sk-valid-12345"); err != nil {
		t.Fatalf("SaveToken valid: %v", err)
	}
	if _, err := Token(); err != nil {
		t.Fatalf("Token valid: %v", err)
	}
	// An invalid token on disk (hand-crafted) fails the read path.
	if err := os.WriteFile(p, []byte("bad\r\n-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Token(); err == nil {
		t.Fatal("Token must reject control-character contents on read")
	}
}

// Read paths are pure (round-2 ticket 01): resolving or reading the store
// must not create ~/.kyacode as a side effect. Creation belongs to the
// write sites, exactly where they own persistence — every getter a
// read-only command rides (Load, Token, the two path getters, DeleteToken)
// leaves the filesystem untouched.
func TestReadPathsNeverCreateTheRoot(t *testing.T) {
	home := withHome(t)

	if _, err := Load(); err != nil {
		t.Fatalf("Load on a bare home must serve the default gateway: %v", err)
	}
	if _, err := Token(); err == nil || err.Error() != "no token stored (run `kyacode auth`)" {
		t.Fatalf("Token on a bare home = %v, want the friendly not-stored form", err)
	}
	if _, err := TokenPath(); err != nil {
		t.Fatalf("TokenPath must resolve without touching the disk: %v", err)
	}
	if _, err := Path(); err != nil {
		t.Fatalf("Path must resolve without touching the disk: %v", err)
	}
	if err := DeleteToken(); err == nil || err.Error() != "not found" {
		t.Fatalf("DeleteToken on a bare home = %v, want a clean not-found", err)
	}

	// The whole battery above must never have materialised the root.
	if _, err := os.Stat(filepath.Join(home, ".kyacode")); !os.IsNotExist(err) {
		t.Fatalf("a read path created ~/.kyacode as a side effect: %v", err)
	}
}

// The two write sites (the token save, the config save) own the root's
// creation exactly where they own persistence: a save from a bare home
// materialises ~/.kyacode 0700, ahead of the temp-file write it serves.
// Conservation pin for the creation MOVE (round-2 ticket 01): before, the
// creation rode the read-path getters — it must not be dropped there.
func TestWriteSitesCreateTheRoot(t *testing.T) {
	home := withHome(t)

	old := umaskForTest(0o002)
	defer umaskForTest(old)

	root := filepath.Join(home, ".kyacode")
	if err := SaveToken("sk-test-123456"); err != nil {
		t.Fatalf("SaveToken from a bare home: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the token save must create the root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kyacode", "token")); err != nil {
		t.Fatalf("the token save: %v", err)
	}
	// The write-site mkdir keeps the 0700 policy (unix-only reporting:
	// Windows chmod cannot represent a directory's 0700 faithfully).
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Fatalf("write-site-created root mode = %o, want 700", got)
		}
	}

	// Same leg for the config save, starting bare again.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := Save(Setting{Gateway: "https://gw.example"}); err != nil {
		t.Fatalf("Save from a bare home: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the config save must create the root: %v", err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("the freshly created store must load: %v", err)
	}
	if s.Gateway != "https://gw.example" {
		t.Fatalf("gateway = %q", s.Gateway)
	}
}

// The geometry the pre-existing DAC fixtures never covered: HOME ITSELF
// unwritable (0500) with NO ~/.kyacode — they all pre-created the root
// before chmodding. Reads must keep serving: Load the default gateway,
// Token the friendly not-stored shape. Raising the raw mkdir-EACCES the
// old read path raised here is the brick this pin kills (it rode the
// token-path-first resolver straight into `launch`/`models` and left
// `unauth` unable to answer either).
func TestLoadAndTokenServeAnUnwritableBareHome(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("unwritable-HOME fixture needs POSIX DAC as non-root")
	}
	withHome(t)
	home := os.Getenv("HOME")
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(home, 0o700)

	if _, err := Token(); err == nil || err.Error() != "no token stored (run `kyacode auth`)" {
		t.Fatalf("Token from an unwritable bare home = %v, want the friendly not-stored shape", err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load from an unwritable bare home = %v; reads must serve, not brick", err)
	}
	if s.Gateway != DefaultGateway {
		t.Fatalf("Load served %q, want the default gateway", s.Gateway)
	}
}

// A root name occupied by a regular FILE is the corrupt-store class,
// never the friendly miss classes: Windows classifies any READ under
// such a root as ErrNotExist (it would silently serve Load's defaults
// and Token's run-auth remedy otherwise — the regression the read-path
// purity briefly opened, empirically probed on go1.27/windows), and
// POSIX's raw ENOTDIR deserves the named diagnosis too. The save sites
// fail loudly there on their own (MkdirAll/CreateTemp), so this class
// is the read paths' first line of defense left.
func TestFileShapedRootIsTheCorruptStoreError(t *testing.T) {
	home := withHome(t)
	root := filepath.Join(home, ".kyacode")
	if err := os.WriteFile(root, []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil || err.Error() != corruptStoreError(root).Error() {
		t.Fatalf("Load = %v, want the corrupt-store sentence for %s", err, root)
	}
	_, err := Token()
	if err == nil {
		t.Fatal("Token over a file-shaped root must error")
	}
	if err.Error() != corruptStoreError(root).Error() {
		t.Fatalf("Token = %v, want the corrupt-store sentence", err)
	}
}

// The residue-reclaim contract inside saveAtomic (a kill -9 between
// temp-create and rename leaves <final>.tmpXXXX holding the plaintext
// key): the NEXT save reclaims it before writing its own temp. The old
// pattern was relative — filepath.Glob resolved it against the process
// CWD, so the reclaim could never fire in a real scenario (probed by
// the review round); the anchored pattern must actually reclaim.
func TestSaveReclaimsCrashResidueBeforeWritingItsTemp(t *testing.T) {
	home := withHome(t)
	if err := SaveToken("sk-test-123456"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	residue := filepath.Join(home, ".kyacode", "token.tmp42424242")
	if err := os.WriteFile(residue, []byte("leftover-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken("sk-test-654321"); err != nil {
		t.Fatalf("reclaiming save: %v", err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("the next save must reclaim crash residue (%s): %v", residue, err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".kyacode", "token"))
	if err != nil || strings.TrimSpace(string(b)) != "sk-test-654321" {
		t.Fatalf("the reclaiming save lost its own content: %q (%v)", b, err)
	}
}

// DeleteToken's doc pins the plaintext-key hazard: a token.tmp* left by
// a kill -9 between temp-create and rename still holds the key, so
// unauth must reclaim it and must not accept leaving it behind. The
// glob-and-remove loop was implemented but unpinned (deleting it kept
// the whole suite green — deletion experiment, 2026-09-16), so these
// rows drive DeleteToken directly, mirroring the saveAtomic residue-pin
// idiom above.
func TestDeleteTokenReclaimsCrashResidue(t *testing.T) {
	home := withHome(t)
	if err := SaveToken("sk-test-123456"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	residue := filepath.Join(home, ".kyacode", "token.tmp42424242")
	if err := os.WriteFile(residue, []byte("sk-test-123456"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken(); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("DeleteToken must reclaim the crash residue (%s): %v", residue, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kyacode", "token")); !os.IsNotExist(err) {
		t.Fatalf("DeleteToken must remove the final token file: %v", err)
	}
}

// The error branch: a residue name occupied by a NON-EMPTY DIRECTORY
// (portable ENOTEMPTY, no chmod tricks — the windows runner has no
// POSIX permission enforcement) must fail DeleteToken with the residue
// path named, AND the final token file must still exist — the early
// return leaves it, which is the branch's documented point: the key
// survives when cleanup fails rather than being masked as not-found.
func TestDeleteTokenResidueErrorNamesThePathAndKeepsTheKey(t *testing.T) {
	home := withHome(t)
	if err := SaveToken("sk-test-123456"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	residue := filepath.Join(home, ".kyacode", "token.tmp42424242")
	if err := os.MkdirAll(filepath.Join(residue, "holding"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := DeleteToken()
	if err == nil || !strings.Contains(err.Error(), "token.tmp42424242") {
		t.Fatalf("DeleteToken error = %v, want it to name the residue path", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kyacode", "token")); err != nil {
		t.Fatalf("the failed cleanup must leave the final token file in place: %v", err)
	}
}

// A residue without a final file (the crash happened before the first
// rename) is still reclaimed, and the not-found form is returned for
// the absent final — pinning that the glob loop runs BEFORE the final
// remove, the actual crash-before-rename ordering.
func TestDeleteTokenReclaimsResidueWithoutAFinalFile(t *testing.T) {
	home := withHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".kyacode"), 0o700); err != nil {
		t.Fatal(err)
	}
	residue := filepath.Join(home, ".kyacode", "token.tmp42424242")
	if err := os.WriteFile(residue, []byte("sk-test-123456"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := DeleteToken()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("DeleteToken with no final file = %v, want the not-found form after reclaiming the residue", err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("residue must be reclaimed even with no final file: %v", err)
	}
}
