//go:build linux

package repository

import (
	"os"

	"golang.org/x/sys/unix"
)

// signalToGroup sends sig to the process group identified by pgid.
func signalToGroup(pgid int, sig os.Signal) error {
	return unix.Kill(-pgid, sig.(unix.Signal))
}
