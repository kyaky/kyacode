//go:build !windows

package ladder

// procctlTestConsoleAvailable is the POSIX twin of the Windows console
// gate: the CTRL_BREAK rung it arms does not exist off-Windows, and the
// test's own GOOS skip always fires before this value is consulted.
func procctlTestConsoleAvailable() bool { return false }
