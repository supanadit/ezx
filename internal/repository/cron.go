package repository

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronField describes the parseable grammar of a single 5-field cron slot:
// "*", "*/N", "N", "N-M", "N,M,K", and "N-M/S". Value ranges mirror the shell
// scheduler conventions (weekday 0=Sunday).
type cronField struct {
	min, max int
	match    func(v int) bool
}

// Cron is a parsed 5-field cron expression: minute hour day-of-month month
// weekday.
type Cron struct {
	minute, hour, dom, month, wday cronField
	expr                           string
}

// ParseCron parses a 5-field cron expression. Any syntax or range error is
// returned; the empty string is invalid.
func ParseCron(expr string) (*Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q: expected 5 fields, got %d", expr, len(fields))
	}
	ranges := []struct {
		min, max int
	}{
		{0, 59}, // minute
		{0, 23}, // hour
		{1, 31}, // day of month
		{1, 12}, // month
		{0, 6},  // weekday (0=Sunday)
	}
	var c Cron
	parsed := [5]cronField{}
	for i, f := range fields {
		fd, err := parseCronField(f, ranges[i].min, ranges[i].max)
		if err != nil {
			return nil, fmt.Errorf("cron %q field %d: %w", expr, i+1, err)
		}
		parsed[i] = fd
	}
	c.minute, c.hour, c.dom, c.month, c.wday = parsed[0], parsed[1], parsed[2], parsed[3], parsed[4]
	c.expr = expr
	return &c, nil
}

// parseCronField parses a single field with the given value range.
func parseCronField(f string, min, max int) (cronField, error) {
	var parts []string
	if strings.Contains(f, ",") {
		for _, p := range strings.Split(f, ",") {
			if p == "" {
				return cronField{}, fmt.Errorf("empty list element in %q", f)
			}
			parts = append(parts, p)
		}
	} else {
		parts = []string{f}
	}

	var matchers []func(int) bool
	for _, p := range parts {
		m, err := parseCronAtom(p, min, max)
		if err != nil {
			return cronField{}, err
		}
		matchers = append(matchers, m)
	}
	return cronField{min: min, max: max, match: func(v int) bool {
		for _, m := range matchers {
			if m(v) {
				return true
			}
		}
		return false
	}}, nil
}

// parseCronAtom parses a single comma-free atom: "*", "*/N", "N", "N-M",
// "N-M/S".
func parseCronAtom(atom string, min, max int) (func(int) bool, error) {
	// bare "*" matches every value in the field's range.
	if atom == "*" {
		return func(int) bool { return true }, nil
	}
	// step form: "N-M/S" or "*/S"
	if stepIdx := strings.Index(atom, "/"); stepIdx >= 0 {
		base := atom[:stepIdx]
		step, err := strconv.Atoi(atom[stepIdx+1:])
		if err != nil || step <= 0 {
			return nil, fmt.Errorf("invalid step in %q", atom)
		}
		if base == "*" {
			return func(v int) bool { return v%step == 0 }, nil
		}
		lo, hi, err := parseRange(base, min, max)
		if err != nil {
			return nil, err
		}
		return func(v int) bool {
			if v < lo || v > hi {
				return false
			}
			return (v-lo)%step == 0
		}, nil
	}
	// range form: "N-M" (including single value N)
	lo, hi, err := parseRange(atom, min, max)
	if err != nil {
		return nil, err
	}
	return func(v int) bool { return v >= lo && v <= hi }, nil
}

// parseRange parses "N" (single) or "N-M" and validates bounds.
func parseRange(s string, min, max int) (int, int, error) {
	var lo, hi int
	var err error
	if idx := strings.Index(s, "-"); idx >= 0 {
		lo, err = strconv.Atoi(s[:idx])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid value %q", s[:idx])
		}
		hi, err = strconv.Atoi(s[idx+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid value %q", s[idx+1:])
		}
	} else {
		lo, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid value %q", s)
		}
		hi = lo
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("range %q is reversed", s)
	}
	if lo < min || hi > max {
		return 0, 0, fmt.Errorf("value in %q out of range %d-%d", s, min, max)
	}
	return lo, hi, nil
}

// Match reports whether t matches the cron expression. The weekday check is
// applied in addition to the day-of-month check, matching standard cron and
// the shell scheduler's behavior.
func (c *Cron) Match(t time.Time) bool {
	return c.minute.match(t.Minute()) &&
		c.hour.match(t.Hour()) &&
		c.dom.match(t.Day()) &&
		c.month.match(int(t.Month())) &&
		c.wday.match(int(t.Weekday()))
}

// Next returns the next time strictly after t that matches, searching up to a
// window of 5 years. It is used by the scheduler loop to sleep until the next
// cron fire time instead of polling every second.
func (c *Cron) Next(t time.Time) time.Time {
	window := 5 * 365 * 24 * time.Hour
	limit := t.Add(window)
	candidate := t.Truncate(time.Minute).Add(time.Minute)
	for candidate.Before(limit) {
		if c.Match(candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return t // unreachable for valid expressions within the window
}
