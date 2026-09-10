// net.go — ezx.net: a native HTTP client (curl/wget replacement). It performs
// full HTTP requests and downloads with timeout, redirect control, header and
// query support, JSON bodies, and optional SHA-256 verification — no curl, no
// wget, no shell. Implemented entirely with the Go standard library.
package script

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/supanadit/ezx/internal/repository"
)

// NetModule exposes ezx.net.
type NetModule struct {
	ctx context.Context
}

// NewNetModule returns a NetModule using the given context (interrupts
// in-flight requests when the app shuts down).
func NewNetModule(ctx context.Context) *NetModule {
	return &NetModule{ctx: ctx}
}

// httpOpts mirrors the options accepted by net.http.
type httpOpts struct {
	// URL is the request target.
	URL string
	// Method is the HTTP method; default GET.
	Method string
	// Headers are per-request headers (object of name → value).
	Headers map[string]string
	// Query are query parameters merged onto the URL.
	Query map[string]string
	// Body is the raw request body (mutually exclusive with JSON).
	Body string
	// JSON is an object serialized with JSON and sent as the body.
	JSON any
	// Timeout bounds the whole request (ns); default 30s.
	Timeout int64
	// FollowRedirects, default true; false returns the redirect response.
	FollowRedirects *bool
	// ExpectStatus restricts acceptable status codes (array of ints).
	ExpectStatus []int
	// Check throws an Error when the response status is unacceptable.
	Check bool
}

// HTTP performs a single HTTP request and returns
// { ok, status, statusText, headers, body }. Transport failures throw. With
// Check true (or CheckStatusCode), an unacceptable status also throws — the
// curl -f equivalent.
func (m *NetModule) HTTP(opts httpOpts) (map[string]any, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("net.http requires url")
	}
	ro := repository.HTTPOptions{
		URL:         opts.URL,
		Method:      opts.Method,
		Headers:     opts.Headers,
		Query:       opts.Query,
		Body:        opts.Body,
		JSON:        opts.JSON,
		Timeout:     time.Duration(opts.Timeout),
		ExpectStatus: opts.ExpectStatus,
	}
	if opts.Timeout <= 0 {
		ro.Timeout = 30 * time.Second
	}
	if opts.FollowRedirects != nil {
		ro.FollowRedirects = *opts.FollowRedirects
	} else {
		ro.FollowRedirects = true
	}
	res, err := repository.HTTPRequest(m.ctx, ro, opts.Check)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":         res.OK,
		"status":     res.Status,
		"statusText": res.StatusText,
		"headers":    res.Headers,
		"body":       res.Body,
	}, nil
}

// downloadOpts mirrors the options accepted by net.download.
type downloadOpts struct {
	// Timeout bounds the whole download (ns); default 60s.
	Timeout int64
	// Mode is the destination permission bits (e.g. 0o700).
	Mode uint32
	// Overwrite replaces an existing destination; default true.
	Overwrite *bool
	// VerifySha256, when set, requires the downloaded file's SHA-256 hex
	// digest to match.
	VerifySha256 string
	// Headers are extra per-request headers.
	Headers map[string]string
}

// Download fetches url into dest, streaming to disk, and returns
// { bytes, sha256, dest }. With VerifySha256, a digest mismatch throws and the
// partial file is removed.
func (m *NetModule) Download(url, dest string, opts ...downloadOpts) (map[string]any, error) {
	if url == "" || dest == "" {
		return nil, fmt.Errorf("net.download requires url and dest")
	}
	do := repository.DownloadOptions{Overwrite: true}
	if len(opts) > 0 {
		if opts[0].Timeout > 0 {
			do.Timeout = time.Duration(opts[0].Timeout)
		}
		if opts[0].Mode != 0 {
			do.Mode = os.FileMode(opts[0].Mode)
		}
		if opts[0].Overwrite != nil {
			do.Overwrite = *opts[0].Overwrite
		}
		do.VerifySha256 = opts[0].VerifySha256
		do.Headers = opts[0].Headers
	}
	n, digest, err := repository.Download(m.ctx, url, dest, do)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bytes": n, "sha256": digest, "dest": dest}, nil
}