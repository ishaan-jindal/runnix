package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndLanguages(t *testing.T) {
	h := NewRouter(RouterConfig{})

	for path, want := range map[string]int{
		"/healthz":   http.StatusOK,
		"/readyz":    http.StatusOK,
		"/languages": http.StatusOK,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s = %d, want %d", path, rec.Code, want)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/languages", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		Languages []string `json:"languages"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"python": true, "javascript": true, "go": true, "java": true,
		"ruby": true, "rust": true, "cpp": true,
	}
	if len(body.Languages) != len(want) {
		t.Fatalf("languages = %v, want %d entries", body.Languages, len(want))
	}
	for _, lang := range body.Languages {
		if !want[lang] {
			t.Fatalf("unexpected language %q (got %v)", lang, body.Languages)
		}
		delete(want, lang)
	}
}

func TestStubsAre501WithoutSecret(t *testing.T) {
	h := NewRouter(RouterConfig{})

	// Auth routes stay scaffold stubs without a database.
	req := httptest.NewRequest(http.MethodPost, "/auth/register", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("stub = %d, want 501", rec.Code)
	}

	// Implemented tenant routes report 503 without a database.
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/executions"},
		{http.MethodGet, "/executions"},
		{http.MethodGet, "/executions/abc"},
		{http.MethodPost, "/tenants"},
		{http.MethodGet, "/tenants/abc"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

func TestReadyzChecks(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	h := NewRouter(RouterConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static readyz = %d, want 200", rec.Code)
	}

	h = NewRouter(RouterConfig{ReadyCheck: func(context.Context) error {
		return errors.New("postgres unavailable")
	}})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failing readyz = %d, want 503", rec.Code)
	}
}

func TestRequestIDPropagated(t *testing.T) {
	h := NewRouter(RouterConfig{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func TestDemoConsole(t *testing.T) {
	h := NewRouter(RouterConfig{})
	req := httptest.NewRequest(http.MethodGet, "/demo", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /demo = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if body := rec.Body.String(); len(body) == 0 || !strings.Contains(body, "/executions") {
		t.Fatal("demo console missing executions UI")
	}
}
