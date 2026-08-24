package script

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/runtime"
)

// ErrNoAPIServer is returned when a script registers an API route but no HTTP
// server is configured (EZX_HEALTH_ADDR is unset).
var ErrNoAPIServer = errors.New("ezx.api requires EZX_HEALTH_ADDR to be set")

// ErrNoCallbacks is returned when a script registers an API route but the
// active scripting engine cannot call back into script functions.
var ErrNoCallbacks = errors.New("ezx.api requires a scripting engine with callback support")

// ApiModule exposes ezx.api: user-defined HTTP routes on the same server that
// serves the built-in health endpoints (EZX_HEALTH_ADDR). Scripts bind any
// path to a function handler, e.g. to manually trigger a scheduled backup:
//
//	const { api, scheduler } = require("ezx");
//	api.post("/backup/full", () => { scheduler.trigger("backup-full"); return { ok: true }; });
//	api.get("/backup/status", () => scheduler.status("backup-full"));
//
// The handler's return value is JSON-encoded into the response. Handlers are
// opaque values — this module is scripting-language agnostic and delegates
// invocation to the runtime.Invoker provided by the active engine adapter.
//
// Concurrency note: handlers run on HTTP goroutines. They are safe here
// because the bootstrap script blocks in chain.run (Go supervision, no script
// executing) once routes are registered, so the runtime is not executing
// script code concurrently. Avoid returning promises/long-running work from a
// handler.
type ApiModule struct {
	e   *echo.Echo
	inv runtime.Invoker
}

// NewApiModule returns an ApiModule bound to the given router and callback
// invoker. A nil router means no HTTP server is configured; route
// registration returns ErrNoAPIServer. A nil invoker means the engine cannot
// call back into scripts; route registration returns ErrNoCallbacks.
func NewApiModule(e *echo.Echo, inv runtime.Invoker) *ApiModule {
	return &ApiModule{e: e, inv: inv}
}

// Get registers a handler for an HTTP GET path.
func (m *ApiModule) Get(path string, handler any) error {
	return m.register(http.MethodGet, path, handler)
}

// Post registers a handler for an HTTP POST path.
func (m *ApiModule) Post(path string, handler any) error {
	return m.register(http.MethodPost, path, handler)
}

// Put registers a handler for an HTTP PUT path.
func (m *ApiModule) Put(path string, handler any) error {
	return m.register(http.MethodPut, path, handler)
}

// Delete registers a handler for an HTTP DELETE path.
func (m *ApiModule) Delete(path string, handler any) error {
	return m.register(http.MethodDelete, path, handler)
}

// register binds the script handler as an echo handler on the shared router.
func (m *ApiModule) register(method, path string, handler any) error {
	if m.e == nil {
		return ErrNoAPIServer
	}
	if m.inv == nil {
		return ErrNoCallbacks
	}
	// Route conflicts on the shared echo are returned by echo; surface them.
	if m.e.Routes() != nil {
		for _, r := range m.e.Routes() {
			if r.Method == method && r.Path == path {
				return errors.New("route already registered: " + method + " " + path)
			}
		}
	}
	m.e.Add(method, path, func(c echo.Context) error {
		res, err := m.inv.Call(handler)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		if res != nil {
			return c.JSON(http.StatusOK, res)
		}
		return c.NoContent(http.StatusOK)
	})
	return nil
}
