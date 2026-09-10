package repository

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPRequestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("a") != "1" {
			t.Errorf("missing query param a=1")
		}
		if r.Header.Get("X-Test") != "yes" {
			t.Errorf("missing header X-Test")
		}
		fmt.Fprint(w, "hello-world")
	}))
	defer srv.Close()

	res, err := HTTPRequest(context.Background(), HTTPOptions{
		URL:     srv.URL + "/x",
		Method:  "GET",
		Query:   map[string]string{"a": "1"},
		Headers: map[string]string{"X-Test": "yes"},
	}, false)
	if err != nil {
		t.Fatalf("HTTPRequest: %v", err)
	}
	if !res.OK || res.Status != 200 {
		t.Fatalf("ok=%v status=%d", res.OK, res.Status)
	}
	if res.Body != "hello-world" {
		t.Fatalf("body = %q", res.Body)
	}
}

func TestHTTPRequestJSONAndCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(202)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	res, err := HTTPRequest(context.Background(), HTTPOptions{
		URL:    srv.URL,
		Method: "POST",
		JSON:   map[string]any{"k": "v"},
	}, true)
	if err != nil {
		t.Fatalf("HTTPRequest: %v", err)
	}
	if res.Status != 202 || !res.OK {
		t.Fatalf("status=%d ok=%v", res.Status, res.OK)
	}
}

func TestHTTPRequestCheckFailsOnUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	if _, err := HTTPRequest(context.Background(), HTTPOptions{URL: srv.URL}, true); err == nil {
		t.Fatal("expected error for 500 with check=true")
	}
	// check=false: 500 is reported, not thrown.
	res, err := HTTPRequest(context.Background(), HTTPOptions{URL: srv.URL}, false)
	if err != nil {
		t.Fatalf("HTTPRequest: %v", err)
	}
	if res.OK || res.Status != 500 {
		t.Fatalf("ok=%v status=%d", res.OK, res.Status)
	}
}

func TestHTTPRequestFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, "/land", http.StatusFound)
			return
		}
		fmt.Fprint(w, "landed")
	}))
	defer srv.Close()

	// Follow by default.
	res, err := HTTPRequest(context.Background(), HTTPOptions{
		URL:             srv.URL + "/hop",
		FollowRedirects: true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Body != "landed" {
		t.Fatalf("follow body = %q", res.Body)
	}
	// No follow: the 302 itself is the response.
	res, err = HTTPRequest(context.Background(), HTTPOptions{
		URL:             srv.URL + "/hop",
		FollowRedirects: false,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 302 {
		t.Fatalf("no-follow status = %d", res.Status)
	}
}

func TestDownloadVerifiesSha256(t *testing.T) {
	body := []byte("download me, please")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "pkg.bin")

	n, digest, err := Download(context.Background(), srv.URL, dst, DownloadOptions{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if int(n) != len(body) {
		t.Fatalf("bytes = %d, want %d", n, len(body))
	}
	if digest != SHA256Hex(body) {
		t.Fatalf("digest = %s", digest)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(body) {
		t.Fatalf("content = %q", data)
	}

	// Mismatched expected digest removes the partial file and errors.
	dst2 := filepath.Join(dir, "bad.bin")
	if _, _, err := Download(context.Background(), srv.URL, dst2, DownloadOptions{
		VerifySha256: "00000000000000000000000000000000",
	}); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, err := os.Lstat(dst2); !os.IsNotExist(err) {
		t.Fatalf("bad file was not cleaned up: %v", err)
	}
}