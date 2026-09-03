// Command gateway runs the Runnix HTTP API (scaffold: routes + stubs).
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
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	handler := gwhttp.NewRouter(gwhttp.RouterConfig{JWTSecret: cfg.JWTSecret})
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
