package scriptmodules

import (
	"context"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

// ProbeModule exposes ezx.probe: generic health-check primitives backed by the
// Go probe engine. Scripts compose these for comprehensive container readiness
// checks (TCP/HTTP/exec connectivity, disk space, process presence, zombies).
type ProbeModule struct {
	ctx context.Context
}

// NewProbeModule returns a ProbeModule using the given context.
func NewProbeModule(ctx context.Context) *ProbeModule {
	return &ProbeModule{ctx: ctx}
}

// TCP reports whether a TCP connection to host:port succeeds.
func (m *ProbeModule) TCP(host string, port int) bool {
	ok, _ := repository.Check(m.ctx, domain.Probe{
		Type: domain.ProbeTypeTCP,
		TCP:  domain.TCPProbe{Host: host, Port: port},
	})
	return ok
}

// HTTP reports whether an HTTP GET to url returns an acceptable status.
// expectStatus, when non-zero, is the required status; zero accepts any 2xx.
func (m *ProbeModule) HTTP(url string, expectStatus int) bool {
	ok, _ := repository.Check(m.ctx, domain.Probe{
		Type: domain.ProbeTypeHTTP,
		HTTP: domain.HTTPProbe{URL: url, ExpectStatus: expectStatus},
	})
	return ok
}

// Exec reports whether the command exits 0.
func (m *ProbeModule) Exec(cmd ...string) bool {
	ok, _ := repository.Check(m.ctx, domain.Probe{
		Type: domain.ProbeTypeExec,
		Exec: cmd,
	})
	return ok
}

// DiskOK reports whether the mount containing path has at least minFreeMB
// free and minFreePercent free (both checked).
func (m *ProbeModule) Disk(path string, minFreePercent, minFreeMB int) bool {
	percent, err1 := repository.DiskFreePercent(path)
	mb, err2 := repository.DiskFreeMB(path)
	if err1 != nil || err2 != nil {
		return false
	}
	return percent >= minFreePercent && mb >= minFreeMB
}

// Process reports whether a process matching name is running.
func (m *ProbeModule) Process(name string) bool {
	return repository.ProcessRunning(name)
}

// Zombies returns the number of zombie processes.
func (m *ProbeModule) Zombies() int {
	return repository.ZombieCount()
}
