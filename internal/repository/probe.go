package repository

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/supanadit/ezx/domain"
)

var probeHTTPClient = &http.Client{Timeout: 5 * time.Second}

// Check runs a single probe against the local system (exec/tcp/http).
func Check(ctx context.Context, probe domain.Probe) (bool, error) {
	if probe.MaxAttempts <= 0 {
		probe.MaxAttempts = 1
	}
	if probe.Interval <= 0 {
		probe.Interval = time.Second
	}
	if probe.Timeout <= 0 {
		probe.Timeout = time.Second
	}

	var lastErr error
	for attempt := 1; attempt <= probe.MaxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(probe.Interval):
			}
		}
		ok, err := checkOnce(ctx, probe)
		if err == nil && ok {
			return true, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, nil
}

func checkOnce(ctx context.Context, probe domain.Probe) (bool, error) {
	switch probe.Type {
	case domain.ProbeTypeTCP:
		return checkTCP(ctx, probe)
	case domain.ProbeTypeHTTP:
		return checkHTTP(ctx, probe)
	default: // ProbeTypeExec
		return checkExec(ctx, probe)
	}
}

func checkExec(ctx context.Context, probe domain.Probe) (bool, error) {
	if len(probe.Exec) == 0 {
		return false, fmt.Errorf("exec probe requires Exec command")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, probe.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, probe.Exec[0], probe.Exec[1:]...)
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return true, nil
}

func checkTCP(ctx context.Context, probe domain.Probe) (bool, error) {
	addr := net.JoinHostPort(probe.TCP.Host, fmt.Sprintf("%d", probe.TCP.Port))
	dialer := net.Dialer{Timeout: probe.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, err
	}
	_ = conn.Close()
	return true, nil
}

func checkHTTP(ctx context.Context, probe domain.Probe) (bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, probe.Timeout)
	defer cancel()
	method := probe.HTTP.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(reqCtx, method, probe.HTTP.URL, nil)
	if err != nil {
		return false, err
	}
	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if probe.HTTP.ExpectStatus != 0 {
		if resp.StatusCode != probe.HTTP.ExpectStatus {
			return false, fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		return true, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, fmt.Errorf("unexpected status %d", resp.StatusCode)
}
