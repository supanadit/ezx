//go:build !linux

package repository

// DiskFreePercent is unsupported on non-Linux platforms.
func DiskFreePercent(path string) (int, error) { return 0, nil }

// DiskFreeMB is unsupported on non-Linux platforms.
func DiskFreeMB(path string) (int, error) { return 0, nil }

// ProcessRunning is unsupported on non-Linux platforms.
func ProcessRunning(name string) bool { return false }

// ZombieCount is unsupported on non-Linux platforms.
func ZombieCount() int { return 0 }
