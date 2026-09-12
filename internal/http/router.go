package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/ishaan-jindal/runnix/internal/executions"
	"github.com/ishaan-jindal/runnix/internal/http/handlers"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RouterConfig wires the gateway router. JWTSecret empty in dev disables auth
// on tenant routes so the service boots without secrets; production must set it.
// Pool nil means no database: auth routes stay 501 stubs and tenant routes
// report 503. NATS nil means JetStream is unreachable: execution submits fail
// with 502 rather than panicking (see errPublisher).
type RouterConfig struct {
	JWTSecret         string
	MembershipChecker middleware.MembershipChecker
	Pool              *pgxpool.Pool
	NATS              rnats.Publisher
	// WebhookSecret signs completion webhook delivery; empty disables
	// webhook_url submissions (rejected with 503).
	WebhookSecret string
	// WebhookAllowPrivate relaxes the webhook SSRF blocklist (dev/tests).
	WebhookAllowPrivate bool
	// ReadyCheck, when set, backs /readyz with live dependency checks.
	ReadyCheck func(ctx context.Context) error
}

// NewRouter builds the chi router. Auth routes are live when Pool is set;
// tenant routes are live executions/tenants handlers when Pool is set.
func NewRouter(cfg RouterConfig) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", handlers.Health)
	r.Get("/readyz", handlers.ReadyHandler(cfg.ReadyCheck))
	r.Handle("/metrics", handlers.Metrics())

	r.Get("/languages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"languages": executions.SupportedLanguages})
	})

	if cfg.Pool != nil {
		authH := &handlers.AuthHandler{Pool: cfg.Pool, JWTSecret: cfg.JWTSecret}
		usersH := handlers.NewUsersHandler(cfg.Pool)
		r.Post("/auth/register", authH.Register)
		r.Post("/auth/login", authH.Login)
		r.Post("/auth/refresh", authH.Refresh)
		r.Post("/auth/logout", authH.Logout)
		r.With(middleware.RequireUser(cfg.JWTSecret)).Post("/auth/logout-all", authH.LogoutAll)
		r.With(middleware.RequireUser(cfg.JWTSecret)).Get("/users/me", usersH.Me)

		if cfg.MembershipChecker == nil {
			queries := storedb.New(cfg.Pool)
			cfg.MembershipChecker = middleware.DBChecker(queries)
		}
	} else {
		r.Post("/auth/register", stub("register"))
		r.Post("/auth/login", stub("login"))
		r.Post("/auth/refresh", stub("refresh"))
		r.Post("/auth/logout", stub("logout"))
		r.Post("/auth/logout-all", stub("logout-all"))
		r.Get("/users/me", stub("me"))
	}

	var createExec, listExec, getExec, createTenant, getTenant http.HandlerFunc
	var createKey, listKeys, deleteKey http.HandlerFunc
	if cfg.Pool != nil {
		pub := cfg.NATS
		if pub == nil {
			pub = errPublisher{err: errors.New("nats unavailable")}
		}
		execH := &handlers.ExecutionsHandler{
			Store:                storedb.New(cfg.Pool),
			Publisher:            pub,
			WebhooksEnabled:      cfg.WebhookSecret != "",
			AllowPrivateWebhooks: cfg.WebhookAllowPrivate,
		}
		tenH := handlers.NewTenantsHandler(cfg.Pool)
		apiH := handlers.NewAPIKeysHandler(cfg.Pool)
		createExec, listExec, getExec = execH.Create, execH.List, execH.Get
		createTenant, getTenant = tenH.Create, tenH.Get
		createKey, listKeys, deleteKey = apiH.Create, apiH.List, apiH.Delete
	} else {
		createExec = unavailable("create execution")
		listExec = unavailable("list executions")
		getExec = unavailable("get execution")
		createTenant = unavailable("create tenant")
		getTenant = unavailable("get tenant")
		createKey = unavailable("create API key")
		listKeys = unavailable("list API keys")
		deleteKey = unavailable("revoke API key")
	}

	// Tenant-scoped routes. Auth is enforced when JWTSecret is set.
	r.Group(func(r chi.Router) {
		if cfg.JWTSecret != "" {
			check := cfg.MembershipChecker
			if check == nil {
				check = func(_ context.Context, _, _ string) (string, bool) { return "", false }
			}
			var keys middleware.APIKeyChecker
			if cfg.Pool != nil {
				keys = handlers.NewAPIKeysHandler(cfg.Pool).CheckAPIKey
			}
			r.Use(middleware.RequireAuthWithAPIKeys(cfg.JWTSecret, check, keys))
		}
		r.Post("/executions", createExec)
		r.Get("/executions", listExec)
		r.Get("/executions/{id}", getExec)
		r.Post("/tenants", createTenant)
		r.Get("/tenants/{id}", getTenant)
		r.Post("/tenants/{id}/api-keys", createKey)
		r.Get("/tenants/{id}/api-keys", listKeys)
		r.Delete("/tenants/{id}/api-keys/{keyId}", deleteKey)
	})

	return r
}

// errPublisher fails every publish; used when NATS is unreachable so the
// submit path degrades to 502 instead of panicking on a nil publisher.
type errPublisher struct{ err error }

func (p errPublisher) EnsureStreams(context.Context) error { return p.err }

func (p errPublisher) PublishSubmit(context.Context, rnats.SubmitMessage) error { return p.err }

func stub(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": name + " not implemented (scaffold)"})
	}
}

func unavailable(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": name + " unavailable: database not connected"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
