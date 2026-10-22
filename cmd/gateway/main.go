// Command gateway runs the Runnix HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	gwhttp "github.com/ishaan-jindal/runnix/internal/http"

	"github.com/ishaan-jindal/runnix/internal/config"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "apply pending migrations then exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Printf("gateway: Postgres unavailable (%v) — auth routes stay stubbed", err)
	} else {
		defer db.Close()
	}

	if db != nil {
		if err := store.Migrate(ctx, db); err != nil {
			log.Fatalf("migrate: %v", err)
		}
	} else if *migrateOnly {
		log.Fatal("migrate-only: Postgres unavailable")
	}
	if *migrateOnly {
		log.Print("migrate-only: migrations applied")
		return
	}

	routerCfg := gwhttp.RouterConfig{
		JWTSecret:           cfg.JWTSecret,
		WebhookSecret:       cfg.WebhookSigningSecret,
		WebhookAllowPrivate: cfg.WebhookAllowPrivate,
		QuotasEnabled:       cfg.QuotasEnabled,
	}
	if db != nil {
		routerCfg.Pool = db
	}

	var natsClient *rnats.Client
	if cfg.NATSURL != "" {
		c, err := rnats.Connect(cfg.NATSURL)
		if err != nil {
			log.Printf("gateway: NATS unavailable at %s (%v) — execution submits will 502", cfg.NATSURL, err)
		} else {
			natsClient = c
			defer c.Close()
			if err := c.EnsureStreams(ctx); err != nil {
				log.Printf("gateway: ensure streams: %v — submits may 502", err)
			}
		}
	}
	if natsClient != nil {
		routerCfg.NATS = natsClient
	}
	routerCfg.ReadyCheck = readiness(db, natsClient)
	if cfg.Environment == "development" && cfg.JWTSecret == "" {
		// Unreachable in any other environment: config refuses to boot
		// without a secret. Tenant routes serve without authentication here.
		log.Print("gateway: WARNING: ENV=development with empty JWT_SECRET — tenant routes are unauthenticated")
	}
	handler := gwhttp.NewRouter(routerCfg)
	srv := &http.Server{
		Addr:    cfg.Addr(),
		Handler: handler,
		// Headers are tiny; a client that cannot send them in 5s is stuck.
		ReadHeaderTimeout: 5 * time.Second,
		// Request bodies are small JSON payloads (source + stdin).
		ReadTimeout: 15 * time.Second,
		// Covers the whole response lifetime. No endpoint long-polls — submit
		// returns 202 and polls are separate requests — so 30s is generous
		// enough for a slow uplink while still bounding a stuck handler.
		WriteTimeout: 30 * time.Second,
		// Keep-alive connections are cheap but not free.
		IdleTimeout: 60 * time.Second,
	}

	env := cfg.Environment
	if env == "" {
		env = "unset"
	}

	go func() {
		log.Printf("gateway listening on %s (%s)", cfg.Addr(), env)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-runCtx.Done()

	shutdownCtx, stopServer := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopServer()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// readiness pings Postgres and NATS with a short timeout for /readyz.
// A nil dependency reports unavailable rather than crashing.
func readiness(db *pgxpool.Pool, nc *rnats.Client) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var errs []error
		if db == nil {
			errs = append(errs, errors.New("postgres unavailable"))
		} else if err := db.Ping(ctx); err != nil {
			errs = append(errs, err)
		}
		if nc == nil {
			errs = append(errs, errors.New("nats unavailable"))
		} else if err := nc.Conn.FlushWithContext(ctx); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
}
