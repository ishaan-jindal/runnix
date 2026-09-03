package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/ishaan-jindal/runnix/internal/executions"
	"github.com/ishaan-jindal/runnix/internal/http/handlers"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RouterConfig wires the gateway router. JWTSecret empty in dev disables auth
// on tenant routes so the service boots without secrets; production must set it.
// Pool nil means no database: auth routes stay 501 stubs (scaffold behavior).
type RouterConfig struct {
	JWTSecret         string
	MembershipChecker middleware.MembershipChecker
	Pool              *pgxpool.Pool
}

// NewRouter builds the chi router. Auth routes are live when Pool is set,
// tenant routes are stubbed 501 until their slice lands (deferred: gateway-mvp).
func NewRouter(cfg RouterConfig) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", handlers.Health)
	r.Get("/readyz", handlers.Ready)
	r.Handle("/metrics", handlers.Metrics())

	r.Get("/languages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"languages": executions.SupportedLanguages})
	})

	if cfg.Pool != nil {
		authH := &handlers.AuthHandler{Pool: cfg.Pool, JWTSecret: cfg.JWTSecret}
		r.Post("/auth/register", authH.Register)
		r.Post("/auth/login", authH.Login)
		r.Post("/auth/refresh", authH.Refresh)

		if cfg.MembershipChecker == nil {
			queries := storedb.New(cfg.Pool)
			cfg.MembershipChecker = middleware.DBChecker(queries)
		}
	} else {
		r.Post("/auth/register", stub("register"))
		r.Post("/auth/login", stub("login"))
		r.Post("/auth/refresh", stub("refresh"))
	}

	// Everything below is tenant-scoped (deferred: gateway-mvp). Scaffold: auth enforced
	// when JWTSecret is set, handlers stubbed 501.
	r.Group(func(r chi.Router) {
		if cfg.JWTSecret != "" {
			check := cfg.MembershipChecker
			if check == nil {
				check = func(_ context.Context, _, _ string) (string, bool) { return "", false }
			}
			r.Use(middleware.RequireAuth(cfg.JWTSecret, check))
		}
		r.Post("/executions", stub("create execution"))
		r.Get("/executions", stub("list executions"))
		r.Get("/executions/{id}", stub("get execution"))
		r.Post("/tenants", stub("create tenant"))
		r.Get("/tenants/{id}", stub("get tenant"))
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
