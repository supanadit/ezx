package repository

import (
	"testing"
	"time"
)

// mustParse builds a time in UTC from the given components.
func mustTime(y int, mo time.Month, d, h, min int) time.Time {
	return time.Date(y, mo, d, h, min, 0, 0, time.UTC)
}

func TestCronMatch(t *testing.T) {
	tests := []struct {
		name string
		expr string
		now  time.Time
		want bool
	}{
		// Exact values.
		{"exact minute", "0 * * * *", mustTime(2024, 1, 1, 2, 0), true},
		{"exact minute no match", "0 * * * *", mustTime(2024, 1, 1, 2, 1), false},
		{"exact hour/day", "0 2 * * *", mustTime(2024, 1, 1, 2, 0), true},
		{"exact hour no match", "0 2 * * *", mustTime(2024, 1, 1, 3, 0), false},
		// Asterisk.
		{"all minutes", "* * * * *", mustTime(2024, 1, 1, 2, 37), true},
		{"all hours", "0 * * * *", mustTime(2024, 1, 1, 13, 0), true},
		// Step values (*/N).
		{"step minutes", "*/15 * * * *", mustTime(2024, 1, 1, 2, 0), true},
		{"step minutes on boundary", "*/15 * * * *", mustTime(2024, 1, 1, 2, 15), true},
		{"step minutes off boundary", "*/15 * * * *", mustTime(2024, 1, 1, 2, 7), false},
		// Comma lists (e.g. DIFF_CRON default "20 2,8,14,20 * * *").
		{"comma hours match", "20 2,8,14,20 * * *", mustTime(2024, 1, 1, 8, 20), true},
		{"comma hours other", "20 2,8,14,20 * * *", mustTime(2024, 1, 1, 8, 21), false},
		{"comma hours no match", "20 2,8,14,20 * * *", mustTime(2024, 1, 1, 5, 20), false},
		// Ranges (N-M).
		{"range minutes", "0-5 * * * *", mustTime(2024, 1, 1, 2, 3), true},
		{"range minutes off", "0-5 * * * *", mustTime(2024, 1, 1, 2, 6), false},
		// Step within range (N-M/S).
		{"range step minutes", "1-10/2 * * * *", mustTime(2024, 1, 1, 2, 1), true},
		{"range step minutes mid", "1-10/2 * * * *", mustTime(2024, 1, 1, 2, 5), true},
		{"range step minutes off", "1-10/2 * * * *", mustTime(2024, 1, 1, 2, 6), false},
		{"range step minutes below", "1-10/2 * * * *", mustTime(2024, 1, 1, 2, 0), false},
		// Month and weekday.
		{"month match", "0 0 1 6 *", mustTime(2024, 6, 1, 0, 0), true},
		{"month no match", "0 0 1 6 *", mustTime(2024, 1, 1, 0, 0), false},
		{"weekday match", "0 0 * * 1", mustTime(2024, 1, 1, 0, 0), true}, // 2024-01-01 is a Monday
		{"weekday no match", "0 0 * * 1", mustTime(2024, 1, 2, 0, 0), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q) error: %v", tt.expr, err)
			}
			if got := c.Match(tt.now); got != tt.want {
				t.Errorf("Match(%q, %v) = %v, want %v", tt.expr, tt.now, got, tt.want)
			}
		})
	}
}

func TestCronParseErrors(t *testing.T) {
	tests := []string{
		"",
		"0 2 * *",              // 4 fields
		"0 2 * * * *",          // 6 fields
		"* * * *",              // 4 fields
		"60 * * * *",           // minute out of range
		"* 24 * * *",           // hour out of range
		"* * 32 * *",           // day out of range
		"* * * 13 *",           // month out of range
		"* * * * 7",            // weekday out of range
		"*/0 * * * *",          // zero step
		"a * * * *",            // non-numeric
		"1-2-3 * * * *",        // malformed range
		"*/x * * * *",          // non-numeric step
		"1, * * * *",           // empty list element
	}
	for _, expr := range tests {
		t.Run(expr, func(t *testing.T) {
			if _, err := ParseCron(expr); err == nil {
				t.Errorf("ParseCron(%q) expected error, got nil", expr)
			}
		})
	}
}

func TestCronNext(t *testing.T) {
	c, err := ParseCron("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// 2024-01-01 00:00 should yield 2024-01-01 02:00.
	next := c.Next(mustTime(2024, 1, 1, 0, 0))
	want := mustTime(2024, 1, 1, 2, 0)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}
