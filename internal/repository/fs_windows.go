//go:build windows

package repository

// syscallUmask is unsupported on Windows; returns the input unchanged.
func syscallUmask(mask int) int { return mask }
