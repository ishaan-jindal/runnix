package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if len(body.Languages) != 1 || body.Languages[0] != "python" {
		t.Fatalf("languages = %v, want [python]", body.Languages)
	}
}

func TestStubsAre501WithoutSecret(t *testing.T) {
	h := NewRouter(RouterConfig{})
	req := httptest.NewRequest(http.MethodGet, "/executions/abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("stub = %d, want 501", rec.Code)
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
