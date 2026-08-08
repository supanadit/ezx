package system

import (
	"os"

	"github.com/supanadit/ezx/domain"
)

// fileEditor implements domain.FileEditor by delegating to the same operations
// used by the declarative Operations path. It wraps a target file path.
type fileEditor struct {
	target string
}

// newFileEditor returns a FileEditor bound to the given target path.
func newFileEditor(target string) domain.FileEditor {
	return &fileEditor{target: target}
}

func (e *fileEditor) Path() string {
	return e.target
}

func (e *fileEditor) Read() (string, error) {
	return readContent(e.target)
}

func (e *fileEditor) ReadLines() ([]string, error) {
	return readLines(e.target)
}

func (e *fileEditor) WriteLines(lines []string) error {
	return writeLines(e.target, lines)
}

func (e *fileEditor) Replace(content string) error {
	if err := ensureParentDir(e.target); err != nil {
		return err
	}
	return os.WriteFile(e.target, []byte(content), 0o644)
}

func (e *fileEditor) Append(content string) error {
	return appendContent(e.target, content)
}

func (e *fileEditor) Remove(pattern string) error {
	return opRemove(e.target, domain.FileOperation{Pattern: pattern}, envSource{})
}

func (e *fileEditor) Ensure(line string) error {
	return opEnsure(e.target, domain.FileOperation{Value: line}, envSource{})
}

func (e *fileEditor) Upsert(pattern, value string) error {
	return opSetProperty(e.target, domain.FileOperation{Pattern: pattern, Value: value}, envSource{})
}

func (e *fileEditor) InsertBefore(pattern, content string) error {
	return opInsert(e.target, domain.FileOperation{Pattern: pattern, Value: content}, envSource{}, false)
}

func (e *fileEditor) InsertAfter(pattern, content string) error {
	return opInsert(e.target, domain.FileOperation{Pattern: pattern, Value: content}, envSource{}, true)
}

func (e *fileEditor) ReplaceBlock(startPattern, endPattern, value string) error {
	return opReplaceBlock(e.target, domain.FileOperation{Pattern: startPattern, BlockEnd: endPattern, Value: value}, envSource{})
}

func (e *fileEditor) SetBlock(startPattern, endPattern, marker, value string) error {
	return opSetBlock(e.target, domain.FileOperation{Pattern: startPattern, BlockEnd: endPattern, Marker: marker, Value: value}, envSource{})
}
