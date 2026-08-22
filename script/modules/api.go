package scriptmodules

import (
	"errors"
	"net/http"

	"github.com/dop251/goja"
	"github.com/labstack/echo/v4"
)

// ErrNoAPIServer is returned when a script registers an API route but no HTTP
// server is configured (EZX_HEALTH_ADDR is unset).
var ErrNoAPIServer = errors.New("ezx.api requires EZX_HEALTH_ADDR to be set")

// ApiModule exposes ezx.api: user-defined HTTP routes on the same server that
// serves the built-in health endpoints (EZX_HEALTH_ADDR). Scripts bind any
// path to a JS handler, e.g. to manually trigger a scheduled backup:
//
//	const { api, scheduler } = require("ezx");
//	api.post("/backup/full", () => { scheduler.trigger("backup-full"); return { ok: true }; });
//	api.get("/backup/status", () => scheduler.status("backup-full"));
//
// The handler's return value is JSON-encoded into the response.
//
// Concurrency note: handlers run on HTTP goroutines. They are safe here
// because the bootstrap script blocks in chain.run (Go supervision, no JS
// executing) once routes are registered, so the goja runtime is not executing
// JS concurrently. Avoid returning promises/long-running work from a handler.
type ApiModule struct {
	e  *echo.Echo
	rt *goja.Runtime
}

// NewApiModule returns an ApiModule bound to the given router and runtime. A
// nil router means no HTTP server is configured; route registration returns
// ErrNoAPIServer.
func NewApiModule(e *echo.Echo, rt *goja.Runtime) *ApiModule {
	return &ApiModule{e: e, rt: rt}
}

// Get registers a handler for an HTTP GET path.
func (m *ApiModule) Get(path string, handler goja.Callable) error {
	return m.register(http.MethodGet, path, handler)
}

// Post registers a handler for an HTTP POST path.
func (m *ApiModule) Post(path string, handler goja.Callable) error {
	return m.register(http.MethodPost, path, handler)
}

// Put registers a handler for an HTTP PUT path.
func (m *ApiModule) Put(path string, handler goja.Callable) error {
	return m.register(http.MethodPut, path, handler)
}

// Delete registers a handler for an HTTP DELETE path.
func (m *ApiModule) Delete(path string, handler goja.Callable) error {
	return m.register(http.MethodDelete, path, handler)
}

// register binds the JS handler as an echo handler on the shared router.
func (m *ApiModule) register(method, path string, handler goja.Callable) error {
	if m.e == nil {
		return ErrNoAPIServer
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
		res, err := handler(goja.Undefined())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		if !goja.IsUndefined(res) && !goja.IsNull(res) {
			return c.JSON(http.StatusOK, res.Export())
		}
		return c.NoContent(http.StatusOK)
	})
	return nil
}
