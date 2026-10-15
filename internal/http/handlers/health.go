package handlers

import (
	"context"
	"net/http"
)

// Health returns liveness. No auth, no DB.
func Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "runnix-gateway"})
}

// ReadyHandler returns readiness. When check is set (gateway wires live
// Postgres + NATS checks) a failing dependency yields 503; nil keeps the
// static ok used in tests.
func ReadyHandler(check func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			if err := check(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}
