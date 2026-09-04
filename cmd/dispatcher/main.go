// Command dispatcher consumes JetStream submit messages and runs each
// execution in a Docker sandbox (runsc/gVisor in development), writing
// results back to Postgres, publishing a summary to exec.result.<id>, and
// delivering signed completion webhooks.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/ishaan-jindal/runnix/internal/config"
	"github.com/ishaan-jindal/runnix/internal/dispatcher"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/ishaan-jindal/runnix/internal/webhooks"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// A dispatcher without Postgres, NATS, or Docker is useless: fail fast.
	db, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("dispatcher: Postgres unavailable: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(ctx, db.Pool); err != nil {
		log.Fatalf("dispatcher: migrate: %v", err)
	}

	nc, err := rnats.Connect(cfg.NATSURL)
	if err != nil {
		log.Fatalf("dispatcher: NATS unavailable: %v", err)
	}
	defer nc.Close()
	if err := nc.EnsureStreams(ctx); err != nil {
		log.Fatalf("dispatcher: ensure streams: %v", err)
	}
	js, err := nc.JS()
	if err != nil {
		log.Fatalf("dispatcher: jetstream: %v", err)
	}

	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("dispatcher: docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	runner := dispatcher.NewDockerRunner(cli, cfg.RunnerImage, cfg.RunnerRuntime)
	if err := runner.EnsureImage(ctx); err != nil {
		log.Fatalf("dispatcher: runner image %s: %v", cfg.RunnerImage, err)
	}
	if err := runner.Sweep(ctx); err != nil {
		log.Printf("dispatcher: sweep leftover containers: %v", err)
	}

	queries := storedb.New(db.Pool)
	d := &dispatcher.Dispatcher{
		Store:     queries,
		Publisher: nc,
		Runner:    runner,
		JS:        js,
		Workers:   cfg.ExecWorkers,
		Logger:    log.New(os.Stdout, "dispatcher ", log.LstdFlags),
	}
	if cfg.WebhookSigningSecret != "" {
		d.Webhooks = &webhooks.Deliverer{
			Secret:       cfg.WebhookSigningSecret,
			AllowPrivate: cfg.WebhookAllowPrivate,
			Logger:       d.Logger,
		}
	}
	d.Reaper = &dispatcher.Reaper{
		Store:      queries,
		Interval:   cfg.ReapInterval,
		StaleAfter: cfg.ReapStaleAfter,
		Logger:     d.Logger,
	}

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("dispatcher running (workers=%d image=%s runtime=%q webhooks=%v reaper=%s/%s)",
		cfg.ExecWorkers, cfg.RunnerImage, cfg.RunnerRuntime, d.Webhooks != nil, cfg.ReapInterval, cfg.ReapStaleAfter)

	if err := d.Run(runCtx); err != nil {
		log.Fatalf("dispatcher: %v", err)
	}
	log.Print("dispatcher stopped")
}
