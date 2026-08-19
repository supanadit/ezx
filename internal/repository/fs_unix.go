//go:build !windows

package repository

import "syscall"

// syscallUmask sets the process umask and returns the previous value.
func syscallUmask(mask int) int {
	return syscall.Umask(mask)
}
