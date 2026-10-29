package handlers

import (
	_ "embed"
	"net/http"
)

//go:embed demo.html
var demoPage []byte

// Demo serves the single-file API demo console. Same-origin by design:
// the gateway sends no CORS headers, so the console must be served from
// the API itself for fetch() to work. No auth, no database needed.
func Demo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(demoPage)
}
