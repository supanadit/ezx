package repository

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// This file holds the technology-agnostic ownership helpers: resolving a
// "user:group" or "user" specification against the local account tables and
// applying it. Stdlib only, no domain types, so it is a shared helper any
// driven adapter may use.

// chown changes file ownership from "user:group" or "user".
func chown(path, owner string) error {
	user, group, _ := strings.Cut(owner, ":")
	uid, err := lookupUID(user)
	if err != nil {
		return err
	}
	gid := -1
	if group != "" {
		gid, err = lookupGID(group)
		if err != nil {
			return err
		}
	}
	return os.Chown(path, uid, gid)
}

// lookupUID resolves a user name (or numeric UID) to a UID by parsing /etc/passwd.
func lookupUID(name string) (int, error) {
	if name == "" {
		return -1, nil
	}
	if uid, err := strconv.Atoi(name); err == nil {
		return uid, nil
	}
	uid, err := lookupIDFromFile("/etc/passwd", name, 0, 2)
	if err != nil {
		return -1, fmt.Errorf("unknown user %q: %w", name, err)
	}
	return uid, nil
}

// lookupGID resolves a group name (or numeric GID) to a GID by parsing /etc/group.
func lookupGID(name string) (int, error) {
	if name == "" {
		return -1, nil
	}
	if gid, err := strconv.Atoi(name); err == nil {
		return gid, nil
	}
	gid, err := lookupIDFromFile("/etc/group", name, 0, 2)
	if err != nil {
		return -1, fmt.Errorf("unknown group %q: %w", name, err)
	}
	return gid, nil
}

// lookupIDFromFile finds the numeric ID of a named entry in a colon-separated table
// (e.g., /etc/passwd, /etc/group). nameField is the field index for the name and
// idField the field index for the numeric ID.
func lookupIDFromFile(path, name string, nameField, idField int) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) <= idField {
			continue
		}
		if fields[nameField] != name {
			continue
		}
		id, err := strconv.Atoi(fields[idField])
		if err != nil {
			return -1, err
		}
		return id, nil
	}
	return -1, os.ErrNotExist
}
