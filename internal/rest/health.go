// Package rest is the HTTP delivery layer (clean architecture). Handlers
// depend on domain Service interfaces and register routes; they contain no
// business logic. Mirrors the go-clean-arch internal/rest layout.
package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/domain"
)

// HealthHandler registers and serves the health/readiness endpoints.
type HealthHandler struct {
	Service domain.HealthService
}

// NewHealthHandler registers /livez, /readyz, and /healthz on the router. It
// depends on the domain HealthService Port (no adapter wiring here).
func NewHealthHandler(e *echo.Echo, svc domain.HealthService) {
	h := &HealthHandler{Service: svc}
	e.GET("/livez", h.Livez)
	e.GET("/readyz", h.Readyz)
	e.GET("/healthz", h.Healthz)
}

// Livez reports process liveness (always 200 while ezx is up).
func (h *HealthHandler) Livez(c echo.Context) error {
	if h.Service.Live() {
		return c.JSON(http.StatusOK, "ok")
	}
	return c.JSON(http.StatusServiceUnavailable, "dead")
}

// Readyz reports readiness (200 when the supervised process is ready).
func (h *HealthHandler) Readyz(c echo.Context) error {
	if h.Service.Ready() {
		return c.JSON(http.StatusOK, "ready")
	}
	return c.JSON(http.StatusServiceUnavailable, "not ready")
}

// Healthz combines liveness and readiness.
func (h *HealthHandler) Healthz(c echo.Context) error {
	if h.Service.Live() && h.Service.Ready() {
		return c.JSON(http.StatusOK, "ok")
	}
	return c.JSON(http.StatusServiceUnavailable, "unhealthy")
}
