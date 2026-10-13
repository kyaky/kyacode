//go:build !windows

package procctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ConfigureGroup is a deliberate no-op on POSIX: children must KEEP
// sharing the foreground process group. NEVER Setpgid here — a
// negative-pid group kill would signal kyacode itself, and the terminal's
// SIGINT/SIGHUP broadcast to the whole foreground group is what rung 1 of
// the interrupt ladder relies on.
func ConfigureGroup(cmd *exec.Cmd) {}

// GracefulInterrupt delivers the soft rung to the direct child.
func GracefulInterrupt(p *os.Process) error {
	return p.Signal(os.Interrupt)
}

// Terminate delivers the TERM rung to the direct child. The POSIX side
// genuinely has the rung.
func Terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// ForceTree kills the direct child (already-gone is success for
// idempotency). Daemonizing grandchildren remain the documented residue
// that `kyacode status`'s 24h orphan sweep reclaims.
func ForceTree(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil // already gone
	}
	if err := p.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	return nil
}
