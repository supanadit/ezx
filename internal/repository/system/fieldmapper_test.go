package system

import "testing"

func TestLowerName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"TCP", "tcp"},   // all-caps name → fully lowercased
		{"HTTP", "http"}, // all-caps name → fully lowercased
		{"PID", "pid"},   // all-caps name → fully lowercased
		{"FS", "fs"},     // all-caps name → fully lowercased
		{"Exec", "exec"},
		{"Process", "process"},
		{"BinaryPath", "binaryPath"},
		{"TCPHost", "tCPHost"},       // acronym-boundary behavior preserved
		{"HTTPResponse", "hTTPResponse"}, // acronym-boundary behavior preserved
	}
	for _, c := range cases {
		if got := lowerName(c.in); got != c.want {
			t.Errorf("lowerName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}