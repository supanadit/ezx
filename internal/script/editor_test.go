package script

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEditorModuleOpenAndRead verifies the script-facing editor wrapper
// exposes the file-editing surface to scripts (the ezx.editor module).
func TestEditorModuleOpenAndRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	if err := os.WriteFile(target, []byte("a=1\nb=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewEditorModule()
	ed := m.Open(target)
	if ed.Path() != target {
		t.Fatalf("Path() = %q, want %q", ed.Path(), target)
	}
	if got := ed.Read(); got != "a=1\nb=2\n" {
		t.Fatalf("Read() = %q, want %q", got, "a=1\nb=2\n")
	}
	lines := ed.ReadLines()
	if len(lines) != 2 || lines[0] != "a=1" {
		t.Fatalf("ReadLines() = %v, want [a=1 b=2]", lines)
	}
}

func TestEditorModuleWriteAndMutate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	if err := os.WriteFile(target, []byte("a=1\nb=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ed := NewEditorModule().Open(target)

	// WriteLines
	if err := ed.WriteLines([]string{"x=1", "y=2"}); err != nil {
		t.Fatalf("WriteLines: %v", err)
	}
	if got := ed.Read(); got != "x=1\ny=2\n" {
		t.Fatalf("after WriteLines = %q", got)
	}

	// Replace
	if err := ed.Replace("replaced"); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := ed.Read(); got != "replaced" {
		t.Fatalf("after Replace = %q", got)
	}

	// Append
	if err := ed.Append("appended"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := ed.Read(); got != "replaced\nappended\n" {
		t.Fatalf("after Append = %q", got)
	}

	// Ensure
	if err := ed.Ensure("appended"); err != nil {
		t.Fatalf("Ensure present: %v", err)
	}
	if err := ed.Ensure("newline"); err != nil {
		t.Fatalf("Ensure absent: %v", err)
	}
	if got := ed.Read(); got != "replaced\nappended\nnewline\n" {
		t.Fatalf("after Ensure = %q", got)
	}

	// Remove
	if err := ed.Remove("^appended$"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := ed.Read(); got != "replaced\nnewline\n" {
		t.Fatalf("after Remove = %q", got)
	}

	// Upsert
	if err := ed.Upsert("^newline$", "newline=2"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := ed.Read(); got != "replaced\nnewline=2\n" {
		t.Fatalf("after Upsert = %q", got)
	}

	// InsertBefore / InsertAfter
	if err := ed.InsertBefore("^newline=2$", "before"); err != nil {
		t.Fatalf("InsertBefore: %v", err)
	}
	if err := ed.InsertAfter("^newline=2$", "after"); err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	if got := ed.Read(); got != "replaced\nbefore\nnewline=2\nafter\n" {
		t.Fatalf("after InsertBefore/After = %q", got)
	}

	// ReplaceBlock (BlockEnd searched mid-string, so unanchored)
	if err := ed.ReplaceBlock("newline=2", "after", "newline=3\nafter"); err != nil {
		t.Fatalf("ReplaceBlock: %v", err)
	}
	if got := ed.Read(); got != "replaced\nbefore\nnewline=3\nafter" {
		t.Fatalf("after ReplaceBlock = %q", got)
	}

	// SetBlock (idempotent)
	if err := ed.SetBlock("newline=3", "after", "before", "newline=4\nafter"); err != nil {
		t.Fatalf("SetBlock: %v", err)
	}
	if err := ed.SetBlock("newline=4", "after", "before", "newline=4\nafter"); err != nil {
		t.Fatalf("SetBlock second: %v", err)
	}
	if got := ed.Read(); got != "replaced\nnewline=4\nafter\nbefore\n" {
		t.Fatalf("after SetBlock = %q", got)
	}
}

func TestEditorModuleReadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	ed := NewEditorModule().Open(filepath.Join(dir, "missing"))
	if got := ed.Read(); got != "" {
		t.Fatalf("Read() = %q, want empty", got)
	}
	if lines := ed.ReadLines(); lines != nil {
		t.Fatalf("ReadLines() = %v, want nil", lines)
	}
}
