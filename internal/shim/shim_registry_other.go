//go:build !windows

package shim

import "fmt"

// The desktop registry wiring is Windows-only (the MSIX activation
// behavior that makes the registry the only delivery channel is a
// Windows fact); elsewhere the command reports that plainly instead of
// pretending to wire something.

func DesktopCLIPATHState() (current string, ours bool, err error) {
	return "", false, nil
}

func DesktopRegistryOn() error {
	return fmt.Errorf("the desktop wiring is Windows-only")
}

func DesktopRegistryOff() error {
	return fmt.Errorf("the desktop wiring is Windows-only")
}
