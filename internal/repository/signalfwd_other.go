//go:build !linux

package repository

import "os"

// signalToGroup sends sig to the process group identified by pgid. On
// non-Linux platforms this degrades to signaling the process directly.
func signalToGroup(_ int, sig os.Signal) error {
	return nil
}
