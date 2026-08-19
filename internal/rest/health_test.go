package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// fakeHealthService implements domain.HealthService with a controllable ready
// flag.
type fakeHealthService struct{ live, ready bool }

func (f *fakeHealthService) Live() bool      { return f.live }
func (f *fakeHealthService) Ready() bool     { return f.ready }
func (f *fakeHealthService) SetReady(b bool) { f.ready = b }

func TestHealthEndpoints(t *testing.T) {
	svc := &fakeHealthService{live: true, ready: false}
	e := echo.New()
	NewHealthHandler(e, svc)

	// /livez → 200 (alive).
	rec := doRequest(t, e, "/livez")
	if rec.Code != http.StatusOK {
		t.Fatalf("livez code = %d, want 200", rec.Code)
	}

	// /readyz → 503 (not ready).
	rec = doRequest(t, e, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz code = %d, want 503", rec.Code)
	}

	// /healthz → 503 (alive but not ready).
	rec = doRequest(t, e, "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz code = %d, want 503", rec.Code)
	}

	// Flip ready.
	svc.ready = true
	rec = doRequest(t, e, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz after ready = %d, want 200", rec.Code)
	}
	rec = doRequest(t, e, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz after ready = %d, want 200", rec.Code)
	}
}

func doRequest(t *testing.T, e *echo.Echo, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestResponseIsJSON(t *testing.T) {
	svc := &fakeHealthService{live: true, ready: true}
	e := echo.New()
	NewHealthHandler(e, svc)
	rec := doRequest(t, e, "/livez")
	var body string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("livez body not JSON: %v (%q)", err, rec.Body.String())
	}
}
