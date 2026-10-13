// Package config holds kyacode global settings: the gateway base URL and the
// stored token path. Token lives at ~/.kyacode/token (0600); gateway at
// ~/.kyacode/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"kyacode/internal/gateway"
	"kyacode/internal/rootdir"
)

const (
	// DefaultGateway is the gateway base WITHOUT /v1. Anthropic-style clients
	// (claude) append /v1/messages themselves; OpenAI-style consumers append
	// /v1/chat/completions (the harness's Endpoint.OpenAIBase derives the
	// openai-style base).
	DefaultGateway = "https://kyaky.ai"
	EnvGateway     = "KYA_GATEWAY"
)

// EnvGatewayInsecure opts into plaintext http:// gateways for the rare
// trusted-local case; the default refuses to send the Bearer key in
// cleartext.
const EnvGatewayInsecure = "KYA_GATEWAY_INSECURE"

// isLoopbackHost reports whether the URL's host is a loopback address or
// localhost. The insecure opt-in is scoped to loopback so a fat-fingered
// KYA_GATEWAY cannot ship the Bearer key to a remote host in the clear.
func isLoopbackHost(u string) bool {
	host := u
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	// Drop any user:pass@ prefix.
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	// Bracketed IPv6 first so the port cut below cannot eat the address.
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end < 0 {
			return false
		}
		host = host[1:end]
	} else if i := strings.LastIndex(host, ":"); i >= 0 {
		// host:port (IPv4 or a name) — strip the port.
		host = host[:i]
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NormalizeGateway is the canonical user-facing normalization for a gateway
// base URL: a documented one-line delegate to the gateway module's
// CanonicalBase — the /v1 base grammar's strip half is owned there, beside
// its join half. The delegate is the guarantee that the `gateway set`
// path and the Load read path normalize the stored form IDENTICALLY: two
// call sites, one strip. Callers must still validate the scheme
// separately via ValidateGateway.
func NormalizeGateway(g string) string { return gateway.CanonicalBase(g) }

// ValidateGateway is the exported gate for callers that persist a gateway
// URL themselves (the `gateway` command): it applies the same rules Load
// enforces on read, so a URL that survives it can never brick the next
// Load behind a hard config error.
func ValidateGateway(u string) error {
	insecure := strings.TrimSpace(os.Getenv(EnvGatewayInsecure)) != ""
	switch {
	case strings.HasPrefix(u, "https://"):
		return nil
	case strings.HasPrefix(u, "http://") && insecure && isLoopbackHost(u):
		return nil
	case strings.HasPrefix(u, "http://") && insecure:
		return fmt.Errorf("gateway %q is plaintext http:// to a non-loopback host; %s only exempts loopback targets",
			u, EnvGatewayInsecure)
	}
	return fmt.Errorf("gateway %q must be https:// (set %s=1 for plaintext http testing)", u, EnvGatewayInsecure)
}

// Dir returns ~/.kyacode — pure resolution, no creation. The path comes
// from rootdir (the single owner of the dir's name), and nothing on the
// read paths (Load, Token, DeleteToken, the two path getters) touches
// the filesystem beyond the reads they serve: a read-only command on a
// bare home materialises nothing. Creation belongs to the write sites
// (ensureRoot): the token save and the config save mkdir exactly where
// they already own persistence. The policy is deliberately probe-less:
// a readable-but-unwritable root (the POSIX 0500 class) must keep
// serving read-only commands — the fail-closed write probe is the
// launch-side sandbox policy, and rootdir's package doc states the
// split (and the 0300-vs-0500 permission classes).
func Dir() (string, error) {
	return rootdir.Path()
}

// TokenPath is where the API token is stored (0600).
func TokenPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "token"), nil
}

// Setting is the persistent config file.
type Setting struct {
	Gateway string `json:"gateway"`
}

// Path returns the config.json path.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// corruptRoot reports whether the store's ROOT name is occupied by a
// regular file — the corrupted-store class every store read must
// classify before serving a miss. It moved to a named error the day the
// mkdir probe left Dir(): Windows resolves any read under such a root
// to the same ErrNotExist-class syscall error as an absent file
// (empirically probed: ERROR_PATH_NOT_FOUND, classified by
// syscall.Errno.Is as os.ErrNotExist), so a plain IsNotExist branch
// would silently serve Load's defaults and Token's friendly run-auth
// remedy over a store shape nothing can ever save into. POSIX answers
// ENOTDIR (never IsNotExist) and lands here too, getting the same named
// diagnosis instead of a raw syscall dump. A Stat failure itself is not
// the corruption verdict: the raw read error carries it.
func corruptRoot(root string) bool {
	fi, err := os.Stat(root)
	return err == nil && !fi.IsDir()
}

// corruptStoreError is the single sentence each store read site reports
// for a file-shaped root — the corruption is exactly one thing, so the
// remedy must read exactly one way (it is NOT the missing-token class:
// `kyacode auth` cannot fix a disk shape).
func corruptStoreError(root string) error {
	return fmt.Errorf("%s is a file, not a directory — kyacode's key/config store cannot load from it; remove or rename the file, then run `kyacode auth` (and `kyacode gateway <url>` to re-persist settings)", root)
}

// Load reads persistent settings, applying env override for gateway.
// A malformed config.json is a hard error, with no silent fallback — and
// no env-override escape either: the parse error fires before the env
// override is applied, deliberately. A store the user's hand could have
// edited must never let kyacode resolve the key's destination to a
// gateway nobody vetted; repair is `kyacode gateway <url>` or editing the
// file by hand (see cmdGateway: the set path skips Load entirely and
// overwrites after local validation, so repair is always possible).
func Load() (Setting, error) {
	s := Setting{Gateway: DefaultGateway}
	p, err := Path()
	if err == nil {
		b, err := os.ReadFile(p)
		if err == nil {
			if err := json.Unmarshal(b, &s); err != nil {
				return s, fmt.Errorf("parse %s: %w", p, err)
			}
		} else if corruptRoot(filepath.Dir(p)) {
			// Classify BEFORE the friendlies: a file-shaped root reads
			// as "missing file" on Windows and would otherwise serve the
			// default gateway over a corrupt store (see corruptRoot).
			return s, corruptStoreError(filepath.Dir(p))
		} else if !os.IsNotExist(err) {
			return s, err
		}
	} else {
		return s, err
	}
	if g := strings.TrimSpace(os.Getenv(EnvGateway)); g != "" {
		s.Gateway = g
	}
	// Normalize both sources (env override wins, so normalize once here,
	// after the override). See the gateway module's CanonicalBase for why
	// the stored canonical form must exclude the /v1 suffix.
	s.Gateway = gateway.CanonicalBase(s.Gateway)
	if err := ValidateGateway(s.Gateway); err != nil {
		return Setting{}, err
	}
	return s, nil
}

// saveAtomic is the single persistence shape for everything under
// ~/.kyacode (config.json and the token alike — two sites that must not
// drift the way they did when Save was a bare WriteFile): the temp file is
// created EXCLUSIVELY (os.CreateTemp, 0600 at birth) under its unique name
// so the on-disk copy is never even briefly readable or shared mid-write,
// chmod'd on the handle, then renamed over the final name so a crash or a
// concurrent reader never observes a truncated file. Any failure removes
// the temp file.
//
// Two concurrent savers are NOT serialized and MUST NOT share a temp name:
// with a deterministic name, a second saver's O_TRUNC could land between
// the first's write and rename, corrupting the final file on rename.
// CreateTemp's unique O_EXCL names make the interleaving safe — each
// saver renames a complete temp it alone wrote, and the final content is
// the last rename to land (last-writer-wins, the documented contract; no
// lock feature).
//
// Windows caveat: the rename REPLACES the destination, so a save fails
// with a sharing error while another process holds the final file open
// without delete-share (an editor or AV window on the token/config file)
// — where the old in-place write could truncate through. That is the
// rare, honest failure of the atomic shape; hand-edit-then-save now
// requires closing the editor first.
func saveAtomic(p string, data []byte) error {
	// Reclaim crash residue from earlier saves (a kill -9 between create
	// and rename leaves <final>.tmpXXXX holding the key/config bytes):
	// the next saver reclaims it before writing its own temp. The glob
	// pattern is ANCHORED to the file's own directory: a bare
	// filepath.Base pattern is relative and filepath.Glob would resolve
	// it against the process CWD — a reclaim that only ever fired when
	// the CWD happened to be ~/.kyacode, never in a real kill-residue
	// scenario.
	if stale, gerr := filepath.Glob(filepath.Join(filepath.Dir(p), filepath.Base(p)+".tmp*")); gerr == nil {
		for _, stalePath := range stale {
			os.Remove(stalePath)
		}
	}
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("chmod %s: %w (file may be more permissive than 0600)", tmp, err))
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ensureRoot is the write sites' creation step: ~/.kyacode is mkdirAll'd
// 0700 exactly where a save owns persistence — ahead of the temp-file
// write saveAtomic needs it for — and failing closed there is how a save
// into a hostile tree already fails. No probe: a readable-but-unwritable
// root keeps serving reads (see Dir and rootdir's package doc).
func ensureRoot(d string) error {
	return os.MkdirAll(d, 0o700)
}

// Save writes persistent settings atomically (see saveAtomic): the running
// config.json survives a failed or crashed save byte-for-byte, and a
// concurrent reader only ever sees a complete file. The 0600 discipline
// matches the token's (mode holds a URL, not a credential, but every file
// in ~/.kyacode stays uniformly tight). This is one of the two write
// sites: ensureRoot creates the root right here, ahead of the persistence
// that requires it — the read paths (Load, and Dir underneath) stay pure.
func Save(s Setting) error {
	p, err := Path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureRoot(filepath.Dir(p)); err != nil {
		return err
	}
	return saveAtomic(p, append(b, '\n'))
}

// validateTokenShape rejects tokens that cannot travel safely:
// control characters (CR/LF would make net/http reject the Authorization
// header outright, and would corrupt the codex auth.json embedding).
func validateTokenShape(tok string) error {
	if len(tok) < 8 {
		return fmt.Errorf("token too short (got %d chars, want >= 8)", len(tok))
	}
	for _, r := range tok {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("token contains control characters (only printable keys are storable)")
		}
	}
	return nil
}

// SaveToken writes the API token atomically through saveAtomic: closed
// umask window (create at mode 0, chmod on the handle, 0600 final) and a
// rename over the final name, so even under a lax umask the token is
// never briefly world-readable and a crash mid-write cannot truncate it.
// Like Save, this is one of the two write sites: ensureRoot creates the
// root right here, ahead of the persistence that requires it.
func SaveToken(tok string) error {
	p, err := TokenPath()
	if err != nil {
		return err
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return fmt.Errorf("empty token")
	}
	if err := validateTokenShape(tok); err != nil {
		return err
	}
	if err := ensureRoot(filepath.Dir(p)); err != nil {
		return err
	}
	return saveAtomic(p, []byte(tok+"\n"))
}

// ErrNoToken is the missing-token-file case of Token()'s read taxonomy —
// the only failure whose remedy is running `kyacode auth` again. Callers
// branch on it with errors.Is to keep the friendly wording while
// surfacing any other read failure's cause (status does; see cmdStatus).
var ErrNoToken = errors.New("no token stored (run `kyacode auth`)")

// Token reads the stored token.
//
// Read-failure taxonomy: only a missing token file reports the friendly
// ErrNoToken form — that is the common pre-auth case and callers surface
// that wording to users. Any OTHER read failure (an unreadable file on a
// multi-user host, an I/O error) surfaces its cause instead: collapsing
// it into "not stored — run auth" would send the operator to the same
// fate DeleteToken's comment describes from the write side — a re-auth
// that needs the access the read just failed on. The empty and
// malformed-content cases after a successful read keep their own errors.
func Token() (string, error) {
	p, err := TokenPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if corruptRoot(filepath.Dir(p)) {
			return "", corruptStoreError(filepath.Dir(p))
		}
		if os.IsNotExist(err) {
			return "", ErrNoToken
		}
		return "", fmt.Errorf("reading token: %w — check the file's permissions/ownership first; `kyacode auth` needs the same access and cannot fix an unreadable file", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("stored token is empty")
	}
	if err := validateTokenShape(tok); err != nil {
		return "", fmt.Errorf("stored token is invalid — run `kyacode auth` again: %w", err)
	}
	return tok, nil
}

// DeleteToken removes the stored token — and with it any saveAtomic
// residue a kill -9 between temp-create and rename may have left: a
// token.tmp* still holds the plaintext key, so unauth must not accept
// leaving it behind.
func DeleteToken() error {
	p, err := TokenPath()
	if err != nil {
		return err
	}
	if temps, gerr := filepath.Glob(p + ".tmp*"); gerr == nil {
		for _, t := range temps {
			if rerr := os.Remove(t); rerr != nil && !os.IsNotExist(rerr) {
				return fmt.Errorf("remove %s: %w", t, rerr)
			}
		}
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("not found")
		}
		// Do not mask real failures (e.g. permission errors on a
		// multi-user host) as "not found" — the key would survive.
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}
