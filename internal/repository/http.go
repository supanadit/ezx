package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTPOptions describes an HTTP request executed by HTTPRequest (a curl
// replacement: method, headers, query, JSON/raw body, timeout, redirects).
type HTTPOptions struct {
	// URL is the request target (query params may be supplied separately).
	URL string
	// Method is the HTTP method; default GET.
	Method string
	// Headers are per-request headers.
	Headers map[string]string
	// Query are query parameters merged onto the URL.
	Query map[string]string
	// Body is the raw request body (mutually exclusive with JSON).
	Body string
	// JSON, when set, is serialized with encoding/json and sent as the body
	// with Content-Type: application/json.
	JSON any
	// Timeout bounds the whole request; 0 uses the default (30s).
	Timeout time.Duration
	// FollowRedirects, when true, follows redirects up to 10 hops (curl/wget
	// default at the script layer, where the option is a *bool defaulting
	// true). At this repository level the zero value (false) does not follow;
	// callers set it explicitly.
	FollowRedirects bool
	// ExpectStatus, when non-empty, restricts acceptable status codes. When
	// empty, any 2xx is acceptable.
	ExpectStatus []int
	// Accept is the set of status codes that should NOT be treated as an
	// error even when outside ExpectStatus... (kept simple: Check only).
}

// HTTPResult is the script-visible response of an HTTP request.
type HTTPResult struct {
	// OK reports whether the status code was acceptable.
	OK bool
	// Status is the numeric status code.
	Status int
	// StatusText is the standard text for the status code.
	StatusText string
	// Headers are the response headers.
	Headers map[string]string
	// Body is the full response body.
	Body string
}

// HTTPRequest performs a single HTTP request. It returns the response with the
// body fully read. An error is returned for transport failures (network, TLS,
// timeout) and, when Check is set, for unacceptable status codes.
func HTTPRequest(ctx context.Context, opts HTTPOptions, check bool) (*HTTPResult, error) {
	method := strings.ToUpper(opts.Method)
	if method == "" {
		method = http.MethodGet
	}

	u, err := url.Parse(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("http: invalid URL %q: %w", opts.URL, err)
	}
	q := u.Query()
	for k, v := range opts.Query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	var body io.Reader
	if opts.JSON != nil {
		data, err := json.Marshal(opts.JSON)
		if err != nil {
			return nil, fmt.Errorf("http: marshal JSON body: %w", err)
		}
		body = bytes.NewReader(data)
	} else if opts.Body != "" {
		body = strings.NewReader(opts.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("http: build request: %w", err)
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}
	if opts.JSON != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, u.String(), err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: read body: %w", method, u.String(), err)
	}

	statusOK := len(opts.ExpectStatus) == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300
	if len(opts.ExpectStatus) > 0 {
		for _, code := range opts.ExpectStatus {
			if resp.StatusCode == code {
				statusOK = true
				break
			}
		}
	}

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}

	res := &HTTPResult{
		OK:         statusOK,
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		Headers:    headers,
		Body:       string(data),
	}
	if check && !statusOK {
		return res, fmt.Errorf("http %s %s: unexpected status %d (want %v)", method, u.String(), resp.StatusCode, expectStatusString(opts.ExpectStatus))
	}
	return res, nil
}

// expectStatusString renders the acceptable status list for error messages.
func expectStatusString(expect []int) string {
	if len(expect) == 0 {
		return "2xx"
	}
	parts := make([]string, 0, len(expect))
	for _, c := range expect {
		parts = append(parts, fmt.Sprintf("%d", c))
	}
	return strings.Join(parts, " or ")
}

// DownloadOptions controls Download (a curl -o/wget replacement).
type DownloadOptions struct {
	// Timeout bounds the whole download; 0 uses the default (60s).
	Timeout time.Duration
	// Mode is the permission bits applied to the destination file (0 = default 0o644).
	Mode os.FileMode
	// Overwrite replaces an existing destination; default true.
	Overwrite bool
	// VerifySha256, when non-empty, requires the downloaded content's SHA-256
	// hex digest to match; a mismatch is an error and the file is removed.
	VerifySha256 string
	// Headers are extra per-request headers.
	Headers map[string]string
}

// Download fetches url into dest, streaming the body to disk (no body buffer).
// Returns the number of bytes written and, when VerifySha256 is set, the
// computed digest.
func Download(ctx context.Context, url, dest string, opts DownloadOptions) (bytesWritten int64, sha256Hex string, err error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("download: build request: %w", err)
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, "", err
	}
	if !opts.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return 0, "", nil
		}
	}

	perm := opts.Mode
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return 0, "", err
	}
	cleanup := func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dest)
		}
	}
	defer cleanup()

	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, hasher), resp.Body)
	if copyErr != nil {
		err = fmt.Errorf("download %s: write body: %w", url, copyErr)
		return n, "", err
	}
	if closeErr := out.Close(); closeErr != nil {
		err = fmt.Errorf("download %s: close: %w", url, closeErr)
		return n, "", err
	}
	if err := os.Chmod(dest, perm); err != nil {
		return n, "", err
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if opts.VerifySha256 != "" && !strings.EqualFold(opts.VerifySha256, digest) {
		err = fmt.Errorf("download %s: sha256 mismatch (got %s)", url, digest)
		return n, "", err
	}
	return n, digest, nil
}