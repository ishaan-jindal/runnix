package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Health returns liveness. No auth, no DB.
func Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "runnix-gateway"})
}

// ReadyHandler returns readiness. When check is set (gateway wires live
// Postgres + NATS checks) a failing dependency yields 503; nil keeps the
// static ok used in tests.
func ReadyHandler(check func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if check != nil {
			if err := check(r.Context()); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "error": err.Error()})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	}
}

// Metrics exposes Prometheus metrics.
func Metrics() http.Handler {
	return promhttp.Handler()
}
