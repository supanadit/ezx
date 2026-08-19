// Package repository provides driver-agnostic shared helpers for the swappable
// repository drivers. It mirrors the helper placement in go-clean-arch
// (internal/repository/helper.go): helpers that are common to all drivers live
// here, not under a specific <driver>/ directory.
package repository

import (
	"fmt"
	"regexp"
	"strings"
)

// Lookup returns the value of an environment variable from a KEY=VALUE slice
// and whether the variable was found (including empty values).
func Lookup(environ []string, name string) (string, bool) {
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && k == name {
			return v, true
		}
	}
	return "", false
}

// Get returns the value of an environment variable from a KEY=VALUE slice,
// or def if the variable is unset or empty.
func Get(environ []string, name, def string) string {
	v, ok := Lookup(environ, name)
	if !ok || v == "" {
		return def
	}
	return v
}

// IsSet reports whether an environment variable is present and non-empty.
func IsSet(environ []string, name string) bool {
	v, ok := Lookup(environ, name)
	return ok && v != ""
}

// HasValue reports whether an environment variable equals a specific value
// (exact, case-sensitive match).
func HasValue(environ []string, name, value string) bool {
	v, ok := Lookup(environ, name)
	return ok && v == value
}

// IsTruthy reports whether an environment variable is truthy: true|1|yes|on|y,
// case-insensitive. Unset or empty values are falsy.
func IsTruthy(environ []string, name string) bool {
	v, ok := Lookup(environ, name)
	return ok && IsTruthyValue(v)
}

// IsFalsy reports whether an environment variable is falsy (unset, empty, or
// not truthy).
func IsFalsy(environ []string, name string) bool {
	return !IsTruthy(environ, name)
}

// IsTruthyValue reports whether a raw value string is truthy: true|1|yes|on|y,
// case-insensitive. Matches the shell is_truthy helper.
func IsTruthyValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on", "y":
		return true
	default:
		return false
	}
}

// IsFalsyValue reports whether a raw value string is falsy.
func IsFalsyValue(value string) bool {
	return !IsTruthyValue(value)
}

// NormalizeBool normalizes a value to "true", "false", or "" (unrecognized).
// true|1|yes|on → "true"; false|0|no|off → "false"; else "". Matches the shell
// normalize_bool helper. Note: "y" is truthy (see IsTruthyValue) but is NOT
// normalized to "true" here, mirroring the shell divergence.
func NormalizeBool(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return "true"
	case "false", "0", "no", "off":
		return "false"
	default:
		return ""
	}
}

// Filter removes environment variables whose name exactly matches one of names or
// matches any of the regex patterns from the given environ slice. It returns a new
// slice; the input is not modified. It is the Go counterpart of shell `unset VAR`
// and `env -u VAR` patterns.
//
// An invalid regex in patterns is returned as an error — a malformed pattern is a
// configuration bug that must fail loudly rather than silently leak variables
// through to the spawned process.
func Filter(environ, names, patterns []string) ([]string, error) {
	if len(names) == 0 && len(patterns) == 0 {
		return environ, nil
	}
	exact := make(map[string]struct{}, len(names))
	for _, n := range names {
		exact[n] = struct{}{}
	}
	var res []string
	if len(patterns) > 0 {
		re, err := compilePatterns(patterns)
		if err != nil {
			return nil, err
		}
		for _, kv := range environ {
			name, _, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if _, hit := exact[name]; hit {
				continue
			}
			if re.MatchString(name) {
				continue
			}
			res = append(res, kv)
		}
		return res, nil
	}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, hit := exact[name]; hit {
			continue
		}
		res = append(res, kv)
	}
	return res, nil
}

func compilePatterns(patterns []string) (*regexp.Regexp, error) {
	var parts []string
	for _, p := range patterns {
		if _, err := regexp.Compile(p); err != nil {
			return nil, fmt.Errorf("invalid filter pattern %q: %w", p, err)
		}
		parts = append(parts, "(?:"+p+")")
	}
	return regexp.Compile("(?:" + strings.Join(parts, "|") + ")")
}

// Match holds a single environment variable match from Enumerate.
type Match struct {
	// Name is the full variable name.
	Name string
	// Value is the variable value.
	Value string
	// Captures holds the regex capture groups: index 1 and up correspond to the
	// shell BASH_REMATCH groups; index 0 is the full name match.
	Captures []string
}

// Enumerate returns all environment variables whose name matches the regex
// pattern. Capture groups are available in each Match.Captures.
func Enumerate(environ []string, pattern string) ([]Match, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		sub := re.FindStringSubmatch(name)
		if sub == nil {
			continue
		}
		matches = append(matches, Match{
			Name:     name,
			Value:    value,
			Captures: sub,
		})
	}
	return matches, nil
}
