// Command gateway runs the Runnix HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gwhttp "github.com/ishaan-jindal/runnix/internal/http"

	"github.com/ishaan-jindal/runnix/internal/config"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store"
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
		if err := store.Migrate(ctx, db.Pool); err != nil {
			log.Fatalf("migrate: %v", err)
		}
	} else if *migrateOnly {
		log.Fatal("migrate-only: Postgres unavailable")
	}
	if *migrateOnly {
		log.Print("migrate-only: migrations applied")
		return
	}

	routerCfg := gwhttp.RouterConfig{JWTSecret: cfg.JWTSecret}
	if db != nil {
		routerCfg.Pool = db.Pool
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
	handler := gwhttp.NewRouter(routerCfg)
	srv := &http.Server{Addr: cfg.Addr(), Handler: handler}

	go func() {
		log.Printf("gateway listening on %s (%s)", cfg.Addr(), cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, stopServer := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopServer()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// readiness pings Postgres and NATS with a short timeout for /readyz.
// A nil dependency reports unavailable rather than crashing.
func readiness(db *store.DB, nc *rnats.Client) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var errs []error
		if db == nil {
			errs = append(errs, errors.New("postgres unavailable"))
		} else if err := db.Pool.Ping(ctx); err != nil {
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
