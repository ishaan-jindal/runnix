package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ishaan-jindal/runnix/internal/executions"
	"github.com/ishaan-jindal/runnix/internal/http/handlers"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/quotas"
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
	// QuotasEnabled wires a per-tenant submit-quota checker into the
	// executions handler when Pool is set. Off by default for zero-value
	// configs (unit tests); the gateway sets it from QUOTAS_ENABLED.
	QuotasEnabled bool
	// ReadyCheck, when set, backs /readyz with live dependency checks.
	ReadyCheck func(ctx context.Context) error
}

// NewRouter builds the stdlib mux. Auth routes are live when Pool is set;
// tenant routes are live executions/tenants handlers when Pool is set.
func NewRouter(cfg RouterConfig) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handlers.Health)
	mux.HandleFunc("GET /readyz", handlers.ReadyHandler(cfg.ReadyCheck))
	mux.HandleFunc("GET /demo", handlers.Demo)

	mux.HandleFunc("GET /languages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"languages": executions.SupportedLanguages})
	})

	if cfg.Pool != nil {
		authH := &handlers.AuthHandler{Pool: cfg.Pool, JWTSecret: cfg.JWTSecret}
		usersH := handlers.NewUsersHandler(cfg.Pool)
		mux.HandleFunc("POST /auth/register", authH.Register)
		mux.HandleFunc("POST /auth/login", authH.Login)
		mux.HandleFunc("POST /auth/refresh", authH.Refresh)
		mux.HandleFunc("POST /auth/logout", authH.Logout)
		mux.Handle("POST /auth/logout-all", middleware.RequireUser(cfg.JWTSecret)(http.HandlerFunc(authH.LogoutAll)))
		mux.Handle("GET /users/me", middleware.RequireUser(cfg.JWTSecret)(http.HandlerFunc(usersH.Me)))

		if cfg.MembershipChecker == nil {
			queries := storedb.New(cfg.Pool)
			cfg.MembershipChecker = middleware.DBChecker(queries)
		}
	} else {
		stub := func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented (scaffold)"})
		}
		for _, rt := range [][2]string{
			{"POST", "/auth/register"},
			{"POST", "/auth/login"},
			{"POST", "/auth/refresh"},
			{"POST", "/auth/logout"},
			{"POST", "/auth/logout-all"},
			{"GET", "/users/me"},
		} {
			mux.HandleFunc(rt[0]+" "+rt[1], stub)
		}
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
		if cfg.QuotasEnabled {
			execH.Quota = quotas.NewPostgresChecker(cfg.Pool, nil)
		}
		tenH := handlers.NewTenantsHandler(cfg.Pool)
		apiH := handlers.NewAPIKeysHandler(cfg.Pool)
		createExec, listExec, getExec = execH.Create, execH.List, execH.Get
		createTenant, getTenant = tenH.Create, tenH.Get
		createKey, listKeys, deleteKey = apiH.Create, apiH.List, apiH.Delete
	} else {
		down := func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database not connected"})
		}
		createExec, listExec, getExec = down, down, down
		createTenant, getTenant = down, down
		createKey, listKeys, deleteKey = down, down, down
	}

	// Tenant-scoped routes. Auth is enforced when JWTSecret is set.
	tenantAuth := func(next http.Handler) http.Handler { return next }
	if cfg.JWTSecret != "" {
		check := cfg.MembershipChecker
		if check == nil {
			check = func(_ context.Context, _, _ string) (string, bool) { return "", false }
		}
		var keys middleware.APIKeyChecker
		if cfg.Pool != nil {
			keys = handlers.NewAPIKeysHandler(cfg.Pool).CheckAPIKey
		}
		tenantAuth = middleware.RequireAuthWithAPIKeys(cfg.JWTSecret, check, keys)
	}
	mux.Handle("POST /executions", tenantAuth(http.HandlerFunc(createExec)))
	mux.Handle("GET /executions", tenantAuth(http.HandlerFunc(listExec)))
	mux.Handle("GET /executions/{id}", tenantAuth(http.HandlerFunc(getExec)))
	mux.Handle("POST /tenants", tenantAuth(http.HandlerFunc(createTenant)))
	mux.Handle("GET /tenants/{id}", tenantAuth(http.HandlerFunc(getTenant)))
	mux.Handle("POST /tenants/{id}/api-keys", tenantAuth(http.HandlerFunc(createKey)))
	mux.Handle("GET /tenants/{id}/api-keys", tenantAuth(http.HandlerFunc(listKeys)))
	mux.Handle("DELETE /tenants/{id}/api-keys/{keyId}", tenantAuth(http.HandlerFunc(deleteKey)))

	return recoverer(middleware.RequestID(mux))
}

// recoverer restores chi Recoverer behavior (500 on panics).
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// errPublisher fails every publish; used when NATS is unreachable so the
// submit path degrades to 502 instead of panicking on a nil publisher.
type errPublisher struct{ err error }

func (p errPublisher) PublishSubmit(context.Context, rnats.SubmitMessage) error { return p.err }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
