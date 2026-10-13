//go:build windows

package config

// Windows has no settable process umask: the default ACL inheritance of the
// temp dir decides final modes. Return the mask unchanged so the deferred
// restore is a no-op; the 0600 assertions still verify what SaveToken
// produces natively on this platform.
func umaskForTest(mask int) int { return mask }
