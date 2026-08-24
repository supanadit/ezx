package script

import (
	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

// EditorModule exposes ezx.editor: open(path) returns a file-editor object
// whose methods edit the target file. It wraps domain.FileEditor.
type EditorModule struct{}

// NewEditorModule returns an EditorModule.
func NewEditorModule() *EditorModule {
	return &EditorModule{}
}

// Open returns a script-visible editor bound to the given path. The returned
// Editor exposes read/write/upsert/insert/block methods to scripts.
func (m *EditorModule) Open(path string) *FileEditor {
	return &FileEditor{inner: repository.OpenFileEditor(path)}
}

// FileEditor wraps domain.FileEditor so its methods are reflected onto the
// script-visible object with friendly names.
type FileEditor struct {
	inner domain.FileEditor
}

// Path returns the target file path.
func (e *FileEditor) Path() string { return e.inner.Path() }

// Read returns the entire file content ("" if missing).
func (e *FileEditor) Read() string {
	s, _ := e.inner.Read()
	return s
}

// ReadLines returns the file as an array of lines.
func (e *FileEditor) ReadLines() []string {
	lines, _ := e.inner.ReadLines()
	return lines
}

// WriteLines replaces the file content with the given lines.
func (e *FileEditor) WriteLines(lines []string) error { return e.inner.WriteLines(lines) }

// Replace overwrites the entire file content.
func (e *FileEditor) Replace(content string) error { return e.inner.Replace(content) }

// Append adds content at the end of the file.
func (e *FileEditor) Append(content string) error { return e.inner.Append(content) }

// Remove removes all lines matching the regex pattern.
func (e *FileEditor) Remove(pattern string) error { return e.inner.Remove(pattern) }

// Ensure ensures a line exists; appends if not found.
func (e *FileEditor) Ensure(line string) error { return e.inner.Ensure(line) }

// Upsert removes lines matching pattern, then appends value.
func (e *FileEditor) Upsert(pattern, value string) error { return e.inner.Upsert(pattern, value) }

// InsertBefore inserts content before the first line matching pattern.
func (e *FileEditor) InsertBefore(pattern, content string) error {
	return e.inner.InsertBefore(pattern, content)
}

// InsertAfter inserts content after the first line matching pattern.
func (e *FileEditor) InsertAfter(pattern, content string) error {
	return e.inner.InsertAfter(pattern, content)
}

// ReplaceBlock replaces the block from startPattern to endPattern with value.
func (e *FileEditor) ReplaceBlock(startPattern, endPattern, value string) error {
	return e.inner.ReplaceBlock(startPattern, endPattern, value)
}

// SetBlock removes the block (start→end) if present, then inserts value before
// marker (or appends if marker not found).
func (e *FileEditor) SetBlock(startPattern, endPattern, marker, value string) error {
	return e.inner.SetBlock(startPattern, endPattern, marker, value)
}
