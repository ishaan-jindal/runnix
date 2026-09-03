// Command gateway runs the Runnix HTTP API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gwhttp "github.com/ishaan-jindal/runnix/internal/http"

	"github.com/ishaan-jindal/runnix/internal/config"
	"github.com/ishaan-jindal/runnix/internal/store"
)

func main() {
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

	routerCfg := gwhttp.RouterConfig{JWTSecret: cfg.JWTSecret}
	if db != nil {
		routerCfg.Pool = db.Pool
	}
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
