//go:build linux

package repository

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// DiskFreePercent returns the free space percentage for the mount containing
// path (0-100). It is a generic liveness helper for container health checks.
func DiskFreePercent(path string) (int, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	total := st.Blocks
	free := st.Bavail
	if total == 0 {
		return 0, nil
	}
	return int(free * 100 / total), nil
}

// DiskFreeMB returns the free space in megabytes for the mount containing path.
func DiskFreeMB(path string) (int, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int(st.Bavail * uint64(st.Bsize) / (1024 * 1024)), nil
}

// ProcessRunning reports whether a process whose comm equals name (or contains
// it) is currently running. It scans /proc — a dependency-free ps/pgrep
// alternative for container health checks.
func ProcessRunning(name string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join("/proc", e.Name(), "stat")); err != nil {
			continue
		}
		comm := readProcComm(e.Name())
		if comm == name || strings.Contains(comm, name) {
			return true
		}
	}
	return false
}

// ZombieCount returns the number of processes currently in zombie (Z) state.
func ZombieCount() int {
	count := 0
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// Field 3 (after the comm in parens) is the process state.
		rest := string(data[strings.LastIndexByte(string(data), ')')+2:])
		fields := strings.Fields(rest)
		if len(fields) > 0 && fields[0] == "Z" {
			count++
		}
	}
	return count
}

// readProcComm reads the comm (executable name) of a process by PID.
func readProcComm(pid string) string {
	f, err := os.Open(filepath.Join("/proc", pid, "comm"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		return sc.Text()
	}
	return ""
}
