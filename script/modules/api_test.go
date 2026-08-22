package scriptmodules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"
	"github.com/labstack/echo/v4"
)

func TestApiModuleRoutes(t *testing.T) {
	rt := goja.New()
	e := echo.New()
	m := NewApiModule(e, rt)

	// Register a POST that returns an object and a GET returning a string.
	if _, err := rt.RunString(`
		var postHandler = function() { return { ok: true, type: "full" }; };
		var getHandler  = function() { return "hello"; };
	`); err != nil {
		t.Fatalf("define handlers: %v", err)
	}
	postFn, _ := goja.AssertFunction(rt.Get("postHandler"))
	getFn, _ := goja.AssertFunction(rt.Get("getHandler"))

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
	rt := goja.New()
	m := NewApiModule(nil, rt) // nil router = no server configured
	if _, err := rt.RunString(`var fn = function() {};`); err != nil {
		t.Fatalf("define handler: %v", err)
	}
	fn, _ := goja.AssertFunction(rt.Get("fn"))
	if err := m.Get("/x", fn); err != ErrNoAPIServer {
		t.Fatalf("Get with nil router err = %v, want ErrNoAPIServer", err)
	}
}

func TestApiModuleDuplicateRoute(t *testing.T) {
	rt := goja.New()
	e := echo.New()
	m := NewApiModule(e, rt)
	if _, err := rt.RunString(`var fn = function() {};`); err != nil {
		t.Fatalf("define handler: %v", err)
	}
	fn, _ := goja.AssertFunction(rt.Get("fn"))
	if err := m.Get("/dup", fn); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if err := m.Get("/dup", fn); err == nil {
		t.Fatal("duplicate route should error")
	}
}
