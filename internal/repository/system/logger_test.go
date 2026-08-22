package system

import (
	"bytes"
	"testing"
)

func TestLoggerLiteralPercent(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger()
	l.SetOutput(&buf)

	// No args: message is literal, "%p" must not mangle.
	msg := "archive_command: env -u X pgbackrest archive-push %p"
	emit := func(f string, a ...any) { l.Info(f, a...) }
	emit(msg)
	if got := buf.String(); !bytes.Contains([]byte(got), []byte("archive-push %p")) {
		t.Errorf("literal %% mangled by Sprintf: %q", got)
	}
	if bytes.Contains([]byte(buf.String()), []byte("%!")) {
		t.Errorf("percent leaked into Sprintf output: %q", buf.String())
	}
}

func TestLoggerFormatArgs(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger()
	l.SetOutput(&buf)

	// With args, formatting still applies.
	l.Info("[%s] value=%d", "ctx", 42)
	if got := buf.String(); !bytes.Contains([]byte(got), []byte("[ctx] value=42")) {
		t.Errorf("format args not applied: %q", got)
	}
}