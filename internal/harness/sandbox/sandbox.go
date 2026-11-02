// Package sandbox owns the per-launch sandbox lifecycle: the throwaway
// dir that carries a sandbox-wired runner's config (and therefore the
// real key), born under ~/.kyacode and reclaimed when the launch
// returns. Runners cross only this package's exported surface — New,
// Write, WriteJSON, WriteYAML, Root, HomeTriad, Credentials, the
// key-channel options, the idempotent Cleanup, and the sandbox NAME
// grammar (NameShape) — so the "on every error path, delete the
// key-bearing dir" discipline lives here once instead of in eight
// launcher bodies.
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// KeyEnvName is the env var the gateway key rides into the child
// process. The module's credential env row and the env-REFERENCE file
// sites (codex's TOML env_key, dsh's apiKeyEnv) all derive from this
// one constant, so the name cannot drift between the two key channels.
const KeyEnvName = "KYA_API_KEY"

// Channel is a runner's declared key channel: the classes in which the
// real key legally lands — a sandbox file value, a file reference to
// the env var's NAME, or the process env value. Declared at New time
// so the env channel is emitted by the module per class, not assembled
// in a runner body: a FileOnly runner cannot leak a credential row it
// never has one to emit, and an EnvRef/Dual runner's row cannot be
// forgotten.
type Channel int

const (
	// FileOnly: the key lives in the sandbox file only; the child env
	// carries no credential row (hermes, omp, cline).
	FileOnly Channel = iota
	// EnvRef: the sandbox file references the key by the env var's
	// name (KeyEnvName) and the value rides the process env (dsh,
	// codex's config.toml).
	EnvRef
	// Dual: the key value is in the sandbox file AND the value rides
	// the process env (droid, pi, openclaw, codex's auth.json).
	Dual
)

// Option configures a Sandbox at New time.
type Option func(*settings)

// settings holds the declared facts an Option sets.
type settings struct {
	channel Channel
	token   string
}

// Key declares the runner's key channel and the token that channel
// governs. The token is stored so the module — not the runner body —
// assembles the credential env row (Credentials) for the EnvRef and
// Dual classes; FileOnly ignores it for env purposes.
func Key(channel Channel, token string) Option {
	return func(s *settings) {
		s.channel = channel
		s.token = token
	}
}

// Sandbox is one per-launch throwaway dir: created under ~/.kyacode,
// carries the runner's config (and therefore the real key), and is
// reclaimed by Cleanup when the launch returns.
type Sandbox struct {
	root    string
	channel Channel
	token   string
	// persistent marks a Named home: one whose lifecycle is the user's,
	// not the launch's. Cleanup consults it — a persistent home is never
	// reclaimed by a launch exit, whatever path calls Cleanup.
	persistent bool

	mu   sync.Mutex
	done bool
}

// namePattern is the MkdirTemp pattern New passes for prefix: the
// "<prefix>-<pid>-" head whose tail os.MkdirTemp fills with its random
// suffix, so a created dir's name is exactly <prefix>-<pid>-<random>.
// This head construction and NameShape's matcher are the grammar's ONE
// home — the sweep consumes the matcher and re-derives nothing.
func namePattern(prefix string, pid int) string {
	return prefix + "-" + strconv.Itoa(pid) + "-"
}

// NameShape returns the anchored matcher for the sandbox dir names New
// creates under prefix: <prefix>-<pid>-<random>, with a DECIMAL pid and
// a DECIMAL-digit random suffix (os/tempfile nextRandom). The orphan
// sweep matches through this predicate instead of re-deriving the
// grammar, so creation and matching cannot drift apart — drift would
// either leave key-bearing survivors (matcher narrower than creation)
// or sweep user dirs (wider). The stdlib coupling note lives here,
// beside the creation it describes: if a future Go widens the random
// alphabet, matchers built from this shape safely stop matching new
// sandboxes (no data loss; a fixture test flags the stdlib change). A
// bare-prefix check would put user-named dirs sharing a prefix
// (codex-archive, codex-1-backup) in the blast radius of
// `kyacode status` — the whole shape must be anchored.
//
// Compile failure is impossible by construction: regexp.QuoteMeta escapes
// every regexp metacharacter, so the interpolated prefix is always a
// literal — an anchored always-valid pattern.
func NameShape(prefix string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `-[0-9]+-[0-9]+$`)
}

// persistentNameShape is the guard against a Named name that would look
// like a pid-random sandbox tail to the orphan sweep: the sweep matches
// ^<prefix>-<pid>-<random>$ for registry prefixes, so a persistent home
// named "codex-1-2" could be reclaimed by a concurrent `kyacode status`.
// Any name whose whole shape is alpha-pid-decimal is refused here — the
// grammar's cheapest collision-proofing, checked at the only place a
// Named name enters the tree.
var persistentNameShape = regexp.MustCompile(`^[a-z0-9]+-[0-9]+-[0-9]+$`)

// Named reopens (or creates) a FIXED-NAME private dir under ~/.kyacode:
// the persistent-home shape the codex GUI runner needs. Where New builds
// a throwaway per-launch dir that dies with the launch, a Named home
// outlives every launch on purpose — a GUI's app-server keeps running
// inside the editor long after `kyacode launch` has returned — and its
// persistence is structural, not hopeful:
//
//   - The name is fixed, so it can never match the orphan sweep's
//     <prefix>-<pid>-<random> grammar (NameShape) — `kyacode status`
//     cannot reclaim a live GUI home, and no registration can change
//     that, because the name carries no pid-random tail at all.
//   - Cleanup on a Named sandbox is deliberately a NO-OP: the home's
//     lifecycle belongs to the operator (remove it by hand or with a
//     future maintenance command), never to a launch exit path. A
//     deferred w.Cleanup() that reaches a Named sandbox therefore
//     touches nothing.
//   - The dir is created exactly like a sandbox root: create-if-missing
//     0700 with existing-mode conservation and the fail-closed write
//     probe, so a home the launch cannot write to fails before any
//     wiring file lands in it.
//
// The name must be a single lowercase path segment; a name shaped like a
// pid-random sandbox tail is refused (see persistentNameShape) rather
// than allowed to enter the sweep's blast radius.
func Named(name string, opts ...Option) (*Sandbox, error) {
	if name == "" {
		return nil, fmt.Errorf("sandbox: empty name")
	}
	if !persistentNameOK(name) {
		return nil, fmt.Errorf("sandbox: %q is not a valid persistent-home name", name)
	}
	if persistentNameShape.MatchString(name) {
		return nil, fmt.Errorf("sandbox: %q collides with the orphan sweep's pid-random shape — pick a name with a non-numeric tail", name)
	}
	s := settings{channel: FileOnly}
	for _, o := range opts {
		o(&s)
	}
	d, err := rootDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(d, name)
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	return &Sandbox{root: root, channel: s.channel, token: s.token, persistent: true}, nil
}

// persistentNameOK validates a Named home's name as a single lowercase
// path segment (no separators, no dots, no whitespace) so the name can
// never escape ~/.kyacode or smuggle a subpath.
func persistentNameOK(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return name[0] >= 'a' && name[0] <= 'z'
}

// New creates a fresh, collision-free sandbox root under ~/.kyacode
// named <prefix>-<pid>-<random> (0700). MkdirTemp refuses to clobber
// an existing path, so two launches that race on the same PID cannot
// share — or RemoveAll — each other's live sandbox (which holds the
// real key). The writability probe on the Root dir fails closed: an
// unwritable ~/.kyacode errors before any sandbox dir exists.
func New(prefix string, opts ...Option) (*Sandbox, error) {
	if prefix == "" {
		return nil, fmt.Errorf("sandbox: empty prefix")
	}
	s := settings{channel: FileOnly}
	for _, o := range opts {
		o(&s)
	}
	d, err := rootDir()
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(d, namePattern(prefix, os.Getpid()))
	if err != nil {
		return nil, err
	}
	return &Sandbox{root: root, channel: s.channel, token: s.token}, nil
}

// Root is the sandbox dir's absolute path.
func (s *Sandbox) Root() string { return s.root }

// WriteJSON marshals v (MarshalIndent, two-space — the byte form every
// JSON config runner already wrote) and lands it through Write, so the
// marshal/guard/write block each runner repeated lives here once,
// inheriting MkdirAll, the 0600 write and the escape refusal. It PANICS
// on an unmarshalable value — the must-idiom contract in its new home:
// a silently empty config would send the harness away without the key.
// Write failures keep their error channel.
func (s *Sandbox) WriteJSON(rel string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("sandbox: marshal %s: %v", rel, err))
	}
	return s.Write(rel, b)
}

// WriteYAML marshals v (yaml.Marshal — the byte form every YAML config
// runner already wrote) and lands it through Write, mirroring WriteJSON's
// contract. The two methods deliberately never route through each other's
// marshaller: the byte forms are per-format facts, not unifiable.
func (s *Sandbox) WriteYAML(rel string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("sandbox: marshal %s: %v", rel, err))
	}
	return s.Write(rel, b)
}

// Write writes body to a sandbox-relative path: it creates any missing
// parent dirs (MkdirAll 0700 — the uniform subdir rule; the per-dir
// writability probe is deliberately absent, the root's fail-closed
// probe at New already covers that failure class) and writes through
// writePrivate, so a key-bearing file is never even briefly wider than
// 0600. Paths that would resolve outside the sandbox root are refused
// — a key-bearing file planted outside the 0700 dir the lifecycle
// reclaims is exactly the leak the sandbox exists to prevent.
func (s *Sandbox) Write(rel string, body []byte) error {
	if filepath.IsAbs(rel) {
		return fmt.Errorf("sandbox: %q is not a sandbox-relative path", rel)
	}
	abs := filepath.Join(s.root, rel)
	if abs == s.root || !strings.HasPrefix(abs, s.root+string(filepath.Separator)) {
		return fmt.Errorf("sandbox: %q escapes the sandbox root", rel)
	}
	if dir := filepath.Dir(abs); dir != s.root {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return writePrivate(abs, body)
}

// HomeTriad is the Windows home-redirect triad the HOME-replacing
// runners append after their HOME row: on Windows, os.homedir() and the
// .NET/Node home lookups read USERPROFILE / HOMEDRIVE+HOMEPATH, not
// HOME — so HOME=sandbox alone leaves the harness writing to the real
// user profile (with the key embedded there). The triad is emitted once
// here, never in a runner body; the sandbox lives under the user
// profile, so the drive is never empty. Off-Windows it returns nil —
// there is no redirect to emit.
func (s *Sandbox) HomeTriad() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	drive := filepath.VolumeName(s.root) // e.g. "C:" — never empty, the root is under the user profile
	return []string{
		"USERPROFILE=" + s.root,
		"HOMEDRIVE=" + drive,
		"HOMEPATH=" + strings.TrimPrefix(s.root, drive),
	}
}

// Credentials is the child-env credential row the declared key channel
// exports: one KeyEnvName=value row for the EnvRef and Dual classes, and
// nothing for FileOnly — where the key lives in the sandbox file and the
// env must carry no credential at all. Assembling the row here (from the
// declared channel, not from a runner body) is what makes the channel
// hold by construction: a FileOnly runner cannot leak a row it never has,
// and an EnvRef/Dual runner cannot forget one.
func (s *Sandbox) Credentials() []string {
	if s.channel == FileOnly {
		return nil
	}
	return []string{KeyEnvName + "=" + s.token}
}

// Cleanup reclaims the sandbox dir. Idempotent: the launch path can
// hand the same cleanup func to the interrupt ladder AND the deferred
// post-child-exit path, so a second invocation is a silent no-op — it
// neither re-retries nor re-logs.
//
// On Windows the just-exited child (or an AV/indexer handle) can keep
// files open, so RemoveAll can fail transiently; retry briefly before
// giving up. A sandbox that survives cleanup holds the real key, so
// failing loudly matters: exhaustion writes the key-safety warning to
// stderr rather than disappearing.
func (s *Sandbox) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	if s.persistent {
		// A Named home persists by design (see Named): a launch exit —
		// clean, interrupted, or forced — must never reclaim it. Marking
		// done keeps the idempotency contract without touching the dir.
		s.done = true
		return
	}
	s.done = true
	err := os.RemoveAll(s.root)
	for i := 0; i < 5 && err != nil; i++ {
		time.Sleep(100 * time.Millisecond)
		err = os.RemoveAll(s.root)
	}
	if err != nil {
		// A surviving sandbox holds the real key — never fail silently.
		// This is the library's one declared unseamed stderr line: Cleanup
		// reaches the launch boundary as a bare func() (the interrupt ladder
		// and the deferred post-child-exit path), so no writer seam is in
		// scope and the failure cannot ride a returned error.
		fmt.Fprintf(os.Stderr, "kyacode: WARNING sandbox cleanup failed (holds key): %s: %v\n", s.root, err)
	}
}
