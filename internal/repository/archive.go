package repository

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ArchiveFormat identifies an archive layout from its filename extension.
type ArchiveFormat int

const (
	ArchiveUnknown ArchiveFormat = iota
	ArchiveTar
	ArchiveTarGz
	ArchiveZip
	ArchiveGz
)

// DetectFormat maps a filename to an ArchiveFormat by extension. The ".tgz"
// shorthand is recognized alongside its long form.
func DetectFormat(path string) ArchiveFormat {
	p := strings.ToLower(path)
	switch {
	case strings.HasSuffix(p, ".tar.gz"), strings.HasSuffix(p, ".tgz"):
		return ArchiveTarGz
	case strings.HasSuffix(p, ".tar"):
		return ArchiveTar
	case strings.HasSuffix(p, ".zip"):
		return ArchiveZip
	case strings.HasSuffix(p, ".gz"):
		return ArchiveGz
	default:
		return ArchiveUnknown
	}
}

// ExtractOptions controls ArchiveExtract.
type ExtractOptions struct {
	// Mode is the permission bits applied to extracted files when the archive
	// carries none. 0 keeps archive modes.
	Mode os.FileMode
	// StripComponents removes N leading path components from each entry
	// (tar --strip-components equivalent).
	StripComponents int
}

// ArchiveExtract extracts the archive at path into dest. The format is
// detected from the filename extension. Paths escaping dest are rejected
// (zip-slip / tar traversal protection).
func ArchiveExtract(path, dest string, opts ExtractOptions) error {
	format := DetectFormat(path)
	if format == ArchiveUnknown {
		return fmt.Errorf("archive: unsupported format for %q (want .tar, .tar.gz/.tgz, .zip, .gz)", path)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	switch format {
	case ArchiveTar:
		return extractTar(f, dest, opts)
	case ArchiveTarGz:
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		return extractTar(gz, dest, opts)
	case ArchiveZip:
		return extractZip(f, dest, opts)
	case ArchiveGz:
		return extractSingleGzip(f, path, dest, opts)
	}
	return fmt.Errorf("archive: unreachable format")
}

// stripPath removes the first n path components of a cleaned relative path.
// Returns an empty string when the entry has no remaining components or when
// the entry is the root "." marker.
func stripPath(name string, n int) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." {
		return "", nil // root directory entry: skip silently
	}
	if clean == "" {
		return "", fmt.Errorf("archive: empty entry path")
	}
	if n <= 0 {
		return clean, nil
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) <= n {
		return "", nil // entry fully stripped (skip it)
	}
	return filepath.Join(parts[n:]...), nil
}

// safeJoin joins entry onto dest, rejecting paths that escape dest.
func safeJoin(dest, entry string) (string, error) {
	clean := filepath.Clean(entry)
	if clean == "" || clean == "." {
		return "", fmt.Errorf("archive: empty entry path")
	}
	target := filepath.Join(dest, clean)
	rel, err := filepath.Rel(dest, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive: entry %q escapes destination", entry)
	}
	return target, nil
}

// extractTar extracts a tar stream (optionally gzip/zstd-decompressed).
func extractTar(r io.Reader, dest string, opts ExtractOptions) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		entry, err := stripPath(hdr.Name, opts.StripComponents)
		if err != nil {
			return err
		}
		if entry == "" {
			continue
		}
		target, err := safeJoin(dest, entry)
		if err != nil {
			return err
		}

		mode := os.FileMode(hdr.Mode).Perm()
		if opts.Mode != 0 && mode == 0 {
			mode = opts.Mode.Perm()
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			linkTarget, err := safeJoin(dest, hdr.Linkname)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		default:
			// Char/block devices, fifos: skip (not needed for entrypoints).
			continue
		}
	}
}

// extractZip extracts a zip archive from an open file.
func extractZip(f *os.File, dest string, opts ExtractOptions) error {
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, stat.Size())
	if err != nil {
		return err
	}
	for _, zf := range zr.File {
		entry, err := stripPath(zf.Name, opts.StripComponents)
		if err != nil {
			return err
		}
		if entry == "" {
			continue
		}
		target, err := safeJoin(dest, entry)
		if err != nil {
			return err
		}
		mode := zf.Mode()
		if opts.Mode != 0 && mode.Perm() == 0 {
			mode = opts.Mode
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, mode.Perm()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeOut := out.Close()
		closeRC := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOut != nil || closeRC != nil {
			if closeOut != nil {
				return closeOut
			}
			return closeRC
		}
	}
	return nil
}

// extractSingleGzip decompresses a single-file .gz archive, stripping the
// .gz suffix for the output name.
func extractSingleGzip(f *os.File, path, dest string, opts ExtractOptions) error {
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	return writeDecompressed(gz, dest, filepath.Base(strings.TrimSuffix(path, ".gz")), opts)
}

func writeDecompressed(r io.Reader, dest, name string, opts ExtractOptions) error {
	mode := opts.Mode
	if mode == 0 {
		mode = 0o644
	}
	target, err := safeJoin(dest, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, r)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// CreateOptions controls ArchiveCreate.
type CreateOptions struct {
	// BaseDir, when set, is stripped from source paths when computing entry
	// names (paths below baseDir become relative to it).
	BaseDir string
}

// ArchiveSource is one entry added to an archive: either a plain path (entry
// name derived from the path) or { Source, Dest } for an explicit entry name.
type ArchiveSource struct {
	// Source is the filesystem path to archive.
	Source string
	// Dest is the entry name inside the archive; empty derives it from Source
	// (relative to CreateOptions.BaseDir when set).
	Dest string
}

// ArchiveCreate creates an archive at path from sources. Format is detected
// from the destination extension. Directories are added recursively.
func ArchiveCreate(path string, sources []ArchiveSource, opts CreateOptions) error {
	format := DetectFormat(path)
	if format == ArchiveUnknown || format == ArchiveGz {
		return fmt.Errorf("archive: unsupported create format for %q (want .tar, .tar.gz/.tgz, .zip)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}

	// tar writer owns the stream it wraps (closed via explicit Close).
	switch format {
	case ArchiveTarGz:
		gw := gzip.NewWriter(out)
		if err := writeTarStream(gw, sources, opts); err != nil {
			_ = out.Close()
			return err
		}
		if err := gw.Close(); err != nil {
			_ = out.Close()
			return err
		}
	case ArchiveTar:
		if err := writeTarStream(out, sources, opts); err != nil {
			_ = out.Close()
			return err
		}
	case ArchiveZip:
		zw := zip.NewWriter(out)
		if err := writeZipStream(zw, sources, opts); err != nil {
			_ = out.Close()
			return err
		}
		if err := zw.Close(); err != nil {
			_ = out.Close()
			return err
		}
	}
	return out.Close()
}

func writeTarStream(w io.Writer, sources []ArchiveSource, opts CreateOptions) error {
	tw := tar.NewWriter(w)
	for _, src := range sources {
		if src.Source == "" {
			return fmt.Errorf("archive: entry requires source")
		}
		name := src.Dest
		if name == "" {
			name = deriveEntryName(src.Source, opts.BaseDir)
		}
		if err := addTarEntry(tw, src.Source, name); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeZipStream(zw *zip.Writer, sources []ArchiveSource, opts CreateOptions) error {
	for _, src := range sources {
		if src.Source == "" {
			return fmt.Errorf("archive: entry requires source")
		}
		name := src.Dest
		if name == "" {
			name = deriveEntryName(src.Source, opts.BaseDir)
		}
		if err := addZipEntry(zw, src.Source, name); err != nil {
			return err
		}
	}
	return nil
}

// deriveEntryName computes the archive entry name for a source path: relative
// to BaseDir when set, otherwise the cleaned path.
func deriveEntryName(source, baseDir string) string {
	if baseDir != "" {
		if rel, err := filepath.Rel(baseDir, source); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(filepath.Clean(source))
}

func addTarEntry(tw *tar.Writer, source, name string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return addTarDir(tw, source, name)
	}
	return addTarFile(tw, source, name, info)
}

func addTarDir(tw *tar.Writer, source, name string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(source, path)
		if relErr != nil {
			return relErr
		}
		entryName := name
		if rel != "." {
			entryName = filepath.ToSlash(filepath.Join(name, rel))
		}
		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Name:     entryName,
				Mode:     int64(info.Mode().Perm()),
				Typeflag: tar.TypeDir,
				ModTime:  info.ModTime(),
			})
		}
		return addTarFile(tw, path, entryName, info)
	})
}

func addTarFile(tw *tar.Writer, source, name string, info os.FileInfo) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(info.Mode().Perm()),
		Size:     info.Size(),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeReg,
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = target
		hdr.Size = 0
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func addZipEntry(zw *zip.Writer, source, name string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return addZipDir(zw, source, name)
	}
	return addZipFile(zw, source, name)
}

func addZipDir(zw *zip.Writer, source, name string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(source, path)
		if relErr != nil {
			return relErr
		}
		entryName := name
		if rel != "." {
			entryName = filepath.ToSlash(filepath.Join(name, rel))
		}
		if info.IsDir() {
			if !strings.HasSuffix(entryName, "/") {
				entryName += "/"
			}
			_, err := zw.Create(entryName)
			return err
		}
		return addZipFile(zw, path, entryName)
	})
}

func addZipFile(zw *zip.Writer, source, name string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(target))
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}