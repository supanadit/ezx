// archive.go — ezx.archive: native create/extract for tar, tar.gz, tar.zst,
// and zip archives (tar/gunzip replacements). Implemented with the Go standard
// library only — no external programs.
package script

import (
	"fmt"
	"os"

	"github.com/supanadit/ezx/internal/repository"
)

// ArchiveModule exposes ezx.archive.
type ArchiveModule struct{}

// NewArchiveModule returns an ArchiveModule.
func NewArchiveModule() *ArchiveModule {
	return &ArchiveModule{}
}

// extractOpts holds options for archive.extract.
type extractOpts struct {
	// Mode is the permission bits applied to extracted files when the archive
	// carries none.
	Mode uint32
	// StripComponents removes N leading path components from each entry
	// (tar --strip-components equivalent).
	StripComponents int
}

// Extract unpacks the archive at path into dest. The format is detected from
// the filename extension (.tar, .tar.gz/.tgz, .zip, .gz).
// Entries escaping dest are rejected.
func (m *ArchiveModule) Extract(path, dest string, opts ...extractOpts) error {
	eo := repository.ExtractOptions{}
	if len(opts) > 0 {
		eo.Mode = os.FileMode(opts[0].Mode)
		eo.StripComponents = opts[0].StripComponents
	}
	return repository.ArchiveExtract(path, dest, eo)
}

// archiveSource is one entry added by archive.create: { source, dest }.
type archiveSource struct {
	// Source is the filesystem path to archive (file or directory).
	Source string
	// Dest is the entry name inside the archive; empty derives it from Source.
	Dest string
}

// createOpts holds options for archive.create.
type createOpts struct {
	// BaseDir strips this prefix from entry names derived from source paths.
	BaseDir string
}

// Create writes an archive at path from entries (files or directories, added
// recursively). Each entry may be a plain path string, or an object
// { source, dest } for an explicit entry name. Format is detected from the
// destination extension (.tar, .tar.gz/.tgz, .zip).
func (m *ArchiveModule) Create(path string, entries []any, opts ...createOpts) error {
	co := repository.CreateOptions{}
	if len(opts) > 0 {
		co.BaseDir = opts[0].BaseDir
	}
	sources := make([]repository.ArchiveSource, 0, len(entries))
	for i, e := range entries {
		switch v := e.(type) {
		case string:
			sources = append(sources, repository.ArchiveSource{Source: v})
		case archiveSource:
			sources = append(sources, repository.ArchiveSource{Source: v.Source, Dest: v.Dest})
		case map[string]any:
			src, _ := v["source"].(string)
			dst, _ := v["dest"].(string)
			sources = append(sources, repository.ArchiveSource{Source: src, Dest: dst})
		default:
			return fmt.Errorf("archive.create: entry %d must be a path string or { source, dest }", i)
		}
	}
	return repository.ArchiveCreate(path, sources, co)
}