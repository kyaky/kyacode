package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// The app-server lease is the exclusive, crash-released ownership mark on
// a persistent home: at most one live app-server may own a home at a
// time. The mechanism exists because of an upstream contention class
// (openai/codex #41085): multiple app-server processes sharing one
// CODEX_HOME fight over its IPC router and grow its SQLite WAL without
// bound. The GUI shape makes that class concrete — two VS Code windows
// each spawn their own app-server against the same home — so ownership
// is enforced, not hoped for:
//
//   - The shim (the process the editor spawns as its app-server) holds
//     the lease for the child's whole lifetime.
//   - `kyacode launch codex-gui` only PROBES the lease and refuses with
//     a recovery instruction when it is held — it never regenerates the
//     managed layer under a live server (an app-server reads config at
//     startup only; a silent rewrite would be picked up never).
//   - The lock is an OS handle lock, so a crashed or killed holder frees
//     it without any sweep: the next acquirer wins cleanly. This is the
//     crash-recovery contract — no stale locks survive their owner.
//
// The lease file itself is a plain 0600 file inside the home; it holds
// no key and its bytes are never read, only its lock state matters.
const leaseName = "app-server.lease"

// Lease is one held ownership mark. Release is idempotent in effect:
// unlocking twice errors once and closing twice is a no-op, so callers
// on defer paths cannot double-release into corruption.
type Lease struct {
	f      *os.File
	closed bool
}

// AcquireLease takes the exclusive lease on home's app-server.lock file,
// failing immediately (never queuing) when another process holds it: the
// caller is the shim about to spawn the server, and a second server must
// refuse, not wait.
func AcquireLease(home string) (*Lease, error) {
	f, err := os.OpenFile(filepath.Join(home, leaseName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("app-server lease on %s is held by a live server: %w", home, err)
	}
	return &Lease{f: f}, nil
}

// Release lets go of the lease. It unlocks and closes the handle; the
// close is what actually frees the lock for a crashed-holder scenario
// (the OS reclaims handles), so both steps run even if one errors.
func (l *Lease) Release() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	uerr := unlockExclusive(l.f)
	cerr := l.f.Close()
	if uerr != nil {
		return uerr
	}
	return cerr
}

// LeaseHeld reports whether home's lease is currently held. It never
// creates the file (a probe on a never-launched home is simply free) and
// its brief acquisition is a test only: the lock is released at once, so
// a concurrent shim's acquire is not locked out by the probe — the probe
// answers "is a server live right now", it does not reserve anything.
func LeaseHeld(home string) (bool, error) {
	p := filepath.Join(home, leaseName)
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := lockExclusive(f); err != nil {
		return true, nil
	}
	_ = unlockExclusive(f)
	return false, nil
}
