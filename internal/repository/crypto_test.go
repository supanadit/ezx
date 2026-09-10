package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSHA256KnownVector(t *testing.T) {
	// sha256("abc") — well-known test vector.
	if got := SHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256(abc) = %s", got)
	}
}

func TestSHA256File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SHA256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256File = %s", got)
	}
}

func TestBase64Roundtrip(t *testing.T) {
	orig := []byte("postgres:secret")
	enc := Base64Encode(orig)
	if enc != "cG9zdGdyZXM6c2VjcmV0" {
		t.Fatalf("base64 = %q", enc)
	}
	dec, err := Base64Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != string(orig) {
		t.Fatalf("roundtrip = %q", dec)
	}
	if _, err := Base64Decode("!!!not-base64!!!"); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestRandomHex(t *testing.T) {
	a, err := RandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("lengths = %d, %d", len(a), len(b))
	}
	if a == b {
		t.Fatal("two random values collided")
	}
	if _, err := RandomHex(0); err == nil {
		t.Fatal("expected error for n=0")
	}
}