package repository

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/supanadit/ezx/domain"
)

// BuildArgs assembles the CLI arguments for a spawned process from the environment:
// the static Arguments (with ${VAR} and ${VAR:-default} interpolation), then the
// declarative ArgOperations. If ArgsFunc is set it overrides ArgOperations and its
// result is appended after Arguments. Runs after file provisioning so arg-building
// callbacks see the full environment.
func BuildArgs(p domain.Process, environ []string) ([]string, error) {
	var args []string
	lookup := lookupFrom(environ)
	for _, a := range p.Arguments {
		args = append(args, expandEnvVarsFrom(a, nil, lookup))
	}
	if p.ArgsFunc != nil {
		extra, err := p.ArgsFunc(environ)
		if err != nil {
			return nil, fmt.Errorf("ArgsFunc: %w", err)
		}
		return append(args, extra...), nil
	}
	for _, op := range p.ArgOperations {
		produced, err := executeArgOperation(op, environ)
		if err != nil {
			return nil, err
		}
		args = append(args, produced...)
	}
	return args, nil
}

// lookupFrom adapts an environ slice into an os.LookupEnv-compatible function for
// interpolation.
func lookupFrom(environ []string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		return Lookup(environ, name)
	}
}

// argConditionMet reports whether an ArgOperation should produce arguments.
// ConditionFunc overrides When when set; otherwise the When EnvCondition gates.
func argConditionMet(op domain.ArgOperation, environ []string) bool {
	if op.ConditionFunc != nil {
		return op.ConditionFunc(environ)
	}
	return envConditionMet(op.When, environ)
}

// executeArgOperation runs a single ArgOperation, returning zero or more arguments.
func executeArgOperation(op domain.ArgOperation, environ []string) ([]string, error) {
	if !argConditionMet(op, environ) {
		return nil, nil
	}

	// Pattern-enum: repeat once per env var match, transforming the captured name.
	if op.FromEnvPattern != "" {
		matches, err := Enumerate(environ, op.FromEnvPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid FromEnvPattern %q: %w", op.FromEnvPattern, err)
		}
		var args []string
		for _, m := range matches {
			captured := m.Captures[1]
			name := captured
			if op.NameTransformFunc != nil {
				name = op.NameTransformFunc(captured)
			} else {
				name = applyNameTransform(captured, op.NameTransform)
			}
			src := envSource{value: m.Value, captures: m.Captures[1:], name: name}
			args = append(args, formatArg(op, src, environ)...)
		}
		return args, nil
	}

	// FromEnv: a single env var drives the value (and may gate a bare-flag toggle).
	if op.FromEnv != "" {
		value, ok := Lookup(environ, op.FromEnv)
		if op.Format == domain.ArgFormatBareFlag {
			if ok && IsTruthyValue(value) {
				return []string{op.Flag}, nil
			}
			return nil, nil
		}
		if !ok || value == "" {
			return nil, nil
		}
		if op.Split != "" {
			var args []string
			for _, part := range strings.Split(value, op.Split) {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				args = append(args, formatArg(op, envSource{value: part}, environ)...)
			}
			return args, nil
		}
		return formatArg(op, envSource{value: value}, environ), nil
	}

	// Literal: no env source. Gating was handled by argConditionMet (When/ConditionFunc).
	return formatArg(op, envSource{}, environ), nil
}

// formatArg assembles a single argument from an ArgOperation and a resolved source,
// according to the operation's Format.
func formatArg(op domain.ArgOperation, src envSource, environ []string) []string {
	value := interpolateArgValue(op, src, environ)
	switch op.Format {
	case domain.ArgFormatBareFlag:
		return []string{op.Flag}
	case domain.ArgFormatRaw:
		return []string{value}
	case domain.ArgFormatFlagSpace:
		return []string{op.Flag, value}
	default: // ArgFormatFlagValue
		return []string{op.Flag + "=" + value}
	}
}

// interpolateArgValue resolves an operation's Value template against a source,
// applying ValueTransformFunc to ${value}, then expanding ${VAR} and ${VAR:-default}
// references from the environment. An empty template defaults to "${value}".
func interpolateArgValue(op domain.ArgOperation, src envSource, environ []string) string {
	tpl := op.Value
	if tpl == "" {
		tpl = "${value}"
	}
	s := interpolationPattern.ReplaceAllStringFunc(tpl, func(m string) string {
		switch m {
		case "${value}":
			v := src.value
			if op.ValueTransformFunc != nil {
				v = op.ValueTransformFunc(v)
			}
			return v
		case "${name}":
			return src.name
		}
		idx, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}"))
		if err != nil || idx < 1 || idx > len(src.captures) {
			return m
		}
		return src.captures[idx-1]
	})
	return expandEnvVarsFrom(s, &src, lookupFrom(environ))
}
