//go:build windows

package ladder

import "syscall"

// procctlTestConsoleAvailable mirrors procctl's host probe
// (GetConsoleWindow) so the wiring test can skip headless hosts without
// exporting internals. Windows-tagged: syscall.NewLazyDLL does not build
// on POSIX, and the CTRL_BREAK wiring this gate arms is Windows-only.
func procctlTestConsoleAvailable() bool {
	getConsoleWindow := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	h, _, _ := getConsoleWindow.Call()
	return h != 0
}
