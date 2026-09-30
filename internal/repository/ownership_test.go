package repository

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLookupNumericUIDGID(t *testing.T) {
	// Numeric UID/GID resolution does not touch /etc/passwd or /etc/group.
	if got, err := lookupUID("1000"); err != nil || got != 1000 {
		t.Fatalf("lookupUID(1000) = %d, %v; want 1000, nil", got, err)
	}
	if got, err := lookupGID("1000"); err != nil || got != 1000 {
		t.Fatalf("lookupGID(1000) = %d, %v; want 1000, nil", got, err)
	}
	if got, err := lookupUID(""); err != nil || got != -1 {
		t.Fatalf("lookupUID(\"\") = %d, %v; want -1, nil", got, err)
	}
	if got, err := lookupGID(""); err != nil || got != -1 {
		t.Fatalf("lookupGID(\"\") = %d, %v; want -1, nil", got, err)
	}
}

func TestLookupIDFromFile(t *testing.T) {
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	write(t, passwd, "# comment\nroot:x:0:0:root:/root:/bin/bash\napp:x:1001:1001::/home/app:/bin/sh\n")

	got, err := lookupIDFromFile(passwd, "app", 0, 2)
	if err != nil || got != 1001 {
		t.Fatalf("lookupIDFromFile(app) = %d, %v; want 1001, nil", got, err)
	}
	if _, err := lookupIDFromFile(passwd, "missing", 0, 2); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookupIDFromFile(missing) err = %v, want os.ErrNotExist", err)
	}
}

func TestChownNumericOwner(t *testing.T) {
	// chown with a numeric owner:group resolves without touching /etc/passwd
	// or /etc/group, so it works in any environment. We only assert it does
	// not error for the current user's numeric UID (chowning to your own UID
	// is always permitted).
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "x\n")

	uid := os.Getuid()
	owner := strconv.Itoa(uid)
	if err := chown(target, owner); err != nil {
		t.Fatalf("chown(%q) to own uid: %v", owner, err)
	}
	// user:group form with numeric group.
	if err := chown(target, owner+":"+owner); err != nil {
		t.Fatalf("chown(%q) to own uid:gid: %v", owner+":"+owner, err)
	}
}
