//go:build !windows

package config

import "syscall"

func umaskForTest(mask int) int {
	return syscall.Umask(mask)
}
