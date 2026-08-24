package script

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/runtime"
)

// fakeInvoker is a technology-free runtime.Invoker: the "script function" is
// a Go func() (any, error). It proves delivery code needs no engine type.
type fakeInvoker struct{}

func (fakeInvoker) Call(fn any, _ ...any) (any, error) {
	f, ok := fn.(func() (any, error))
	if !ok {
		return nil, errors.New("not a callable handler")
	}
	return f()
}

func TestApiModuleRoutes(t *testing.T) {
	e := echo.New()
	m := NewApiModule(e, fakeInvoker{})

	postFn := func() (any, error) { return map[string]any{"ok": true, "type": "full"}, nil }
	getFn := func() (any, error) { return "hello", nil }

	if err := m.Post("/backup/full", postFn); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if err := m.Get("/backup/status", getFn); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// POST returns JSON object.
	req := httptest.NewRequest(http.MethodPost, "/backup/full", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST code = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("POST body not JSON: %v", err)
	}
	if body["ok"] != true || body["type"] != "full" {
		t.Fatalf("POST body = %v, want {ok:true,type:full}", body)
	}

	// GET returns JSON string.
	req = httptest.NewRequest(http.MethodGet, "/backup/status", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d, want 200", rec.Code)
	}
	var s string
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("GET body not JSON string: %v", err)
	}
	if s != "hello" {
		t.Fatalf("GET body = %q, want hello", s)
	}
}

func TestApiModuleNoServer(t *testing.T) {
	m := NewApiModule(nil, fakeInvoker{}) // nil router = no server configured
	if err := m.Get("/x", func() (any, error) { return nil, nil }); err != ErrNoAPIServer {
		t.Fatalf("Get with nil router err = %v, want ErrNoAPIServer", err)
	}
}

func TestApiModuleNoCallbacks(t *testing.T) {
	m := NewApiModule(echo.New(), nil) // nil invoker = engine without callbacks
	if err := m.Get("/x", func() (any, error) { return nil, nil }); err != ErrNoCallbacks {
		t.Fatalf("Get with nil invoker err = %v, want ErrNoCallbacks", err)
	}
}

func TestApiModuleDuplicateRoute(t *testing.T) {
	e := echo.New()
	m := NewApiModule(e, fakeInvoker{})
	fn := func() (any, error) { return nil, nil }
	if err := m.Get("/dup", fn); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if err := m.Get("/dup", fn); err == nil {
		t.Fatal("duplicate route should error")
	}
}

// compile-time check that runtime.Invoker is satisfied by the fake.
var _ runtime.Invoker = fakeInvoker{}
