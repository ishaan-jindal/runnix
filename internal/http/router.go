package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/ishaan-jindal/runnix/internal/executions"
	"github.com/ishaan-jindal/runnix/internal/http/handlers"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
)

// RouterConfig wires the gateway router. JWTSecret empty in dev disables auth
// on /v1/* so scaffold boots without secrets; production must set it.
type RouterConfig struct {
	JWTSecret         string
	MembershipChecker middleware.MembershipChecker
}

// NewRouter builds the chi router with the tenant-first v1 surface.
// Full handlers land in Phase 1; scaffold returns 501 with the route shape.
func NewRouter(cfg RouterConfig) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", handlers.Health)
	r.Get("/readyz", handlers.Ready)
	r.Handle("/metrics", handlers.Metrics())

	r.Route("/v1", func(r chi.Router) {
		r.Get("/languages", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"languages": executions.SupportedLanguages})
		})

		// Everything below is tenant-scoped in Phase 1. Scaffold: auth enforced
		// when JWTSecret is set, handlers stubbed 501.
		r.Group(func(r chi.Router) {
			if cfg.JWTSecret != "" {
				check := cfg.MembershipChecker
				if check == nil {
					check = func(_, _ string) (string, bool) { return "", false }
				}
				r.Use(middleware.RequireAuth(cfg.JWTSecret, check))
			}
			r.Post("/executions", stub("create execution"))
			r.Get("/executions", stub("list executions"))
			r.Get("/executions/{id}", stub("get execution"))
			r.Post("/tenants", stub("create tenant"))
			r.Get("/tenants/{id}", stub("get tenant"))
		})

		// Auth endpoints: real logic in Phase 1.
		r.Post("/auth/register", stub("register"))
		r.Post("/auth/login", stub("login"))
		r.Post("/auth/refresh", stub("refresh"))
	})

	return r
}

func stub(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": name + " not implemented (scaffold)"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
