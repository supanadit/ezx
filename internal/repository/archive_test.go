package repository

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app")
	if err := os.MkdirAll(filepath.Join(src, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "conf/app.ini"), []byte("port=5432\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "bundle.tar.gz")
	if err := ArchiveCreate(archivePath, []ArchiveSource{{Source: src, Dest: "."}}, CreateOptions{}); err != nil {
		t.Fatalf("ArchiveCreate: %v", err)
	}

	outDir := filepath.Join(dir, "out")
	if err := ArchiveExtract(archivePath, outDir, ExtractOptions{}); err != nil {
		t.Fatalf("ArchiveExtract: %v", err)
	}
	for _, rel := range []string{"main.sh", "conf/app.ini"} {
		if _, err := os.Stat(filepath.Join(outDir, rel)); err != nil {
			t.Fatalf("missing extracted %s: %v", rel, err)
		}
	}
}

func TestExtractStripComponents(t *testing.T) {
	dir := t.TempDir()

	// Build a tar.gz with a leading "parent/" component.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	content := []byte("x")
	if err := tw.WriteHeader(&tar.Header{Name: "parent/deep/file.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "p.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	if err := ArchiveExtract(archivePath, outDir, ExtractOptions{StripComponents: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "deep/file.txt")); err != nil {
		t.Fatalf("strip-1 failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "parent")); !os.IsNotExist(err) {
		t.Fatal("expected stripped leading component to be absent")
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "../../evil.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "evil.tar")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	if err := ArchiveExtract(archivePath, outDir, ExtractOptions{}); err == nil {
		t.Fatal("expected traversal rejection")
	}
	for _, evil := range []string{filepath.Join(dir, "evil.txt"), filepath.Join(filepath.Dir(dir), "evil.txt")} {
		if _, err := os.Stat(evil); !os.IsNotExist(err) {
			t.Fatalf("traversal file leaked to %s", evil)
		}
	}
}

func TestExtractSingleGzip(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("compressed content")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "data.txt.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	if err := ArchiveExtract(archivePath, outDir, ExtractOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("content = %q", data)
	}
}

func TestArchiveDetectAndRejectUnsupported(t *testing.T) {
	if DetectFormat("a.tar.gz") != ArchiveTarGz || DetectFormat("a.tgz") != ArchiveTarGz {
		t.Fatal("tgz detection failed")
	}
	if DetectFormat("a.tar") != ArchiveTar || DetectFormat("a.zip") != ArchiveZip || DetectFormat("a.gz") != ArchiveGz {
		t.Fatal("format detection failed")
	}
	if DetectFormat("a.unknown") != ArchiveUnknown {
		t.Fatal("unknown format not rejected")
	}
	if err := ArchiveExtract("x.unknown", t.TempDir(), ExtractOptions{}); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestCreateZip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(src, []byte("zip me"), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(dir, "bundle.zip")
	if err := ArchiveCreate(zipPath, []ArchiveSource{{Source: src, Dest: "file.txt"}}, CreateOptions{}); err != nil {
		t.Fatalf("ArchiveCreate zip: %v", err)
	}
	outDir := filepath.Join(dir, "out")
	if err := ArchiveExtract(zipPath, outDir, ExtractOptions{}); err != nil {
		t.Fatalf("ArchiveExtract zip: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "zip me" {
		t.Fatalf("content = %q", data)
	}
}