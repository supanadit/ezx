// Package envutil provides common environment variable lookup, boolean, and
// enumeration helpers for callback developers. It is the Go counterpart of the
// shared helpers.sh functions (is_truthy, normalize_bool) used across container
// entrypoint scripts, so porting shell logic to EZX callbacks stays familiar.
package envutil

import (
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
