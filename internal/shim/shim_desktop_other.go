//go:build !windows

package shim

import "fmt"

// The desktop personalities are Windows-only: the desktop app under
// discussion is the MSIX/Store Codex app, and the ancestor-chain
// discrimination (tool-help walk, PEB command-line reads) has no
// portability story. Everywhere else the shim keeps exactly the
// pre-desktop behavior: the enable flag governs, the launch path and
// the extension path are the only consumers.
const (
	desktopModeNone     = iota
	desktopModeVanilla
	desktopModeGateway
)

func resolveDesktopModeDiag() (int, []string) { return desktopModeNone, nil }

func desktopVanillaRuntime() (string, bool, error) {
	return "", false, fmt.Errorf("the Codex desktop backend is Windows-only")
}
