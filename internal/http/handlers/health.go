package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Health returns liveness. No auth, no DB.
func Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "runnix-gateway"})
}

// Ready returns readiness. Phase 1: ping Postgres + NATS; scaffold: static ok.
func Ready(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

// Metrics exposes Prometheus metrics.
func Metrics() http.Handler {
	return promhttp.Handler()
}
