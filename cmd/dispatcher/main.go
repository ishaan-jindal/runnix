// Command dispatcher subscribes to JetStream and creates K8s Jobs.
// Scaffold: validates NATS connectivity when NATS_URL is set, then idles.
// Real Job-per-execution logic lands in Phase 2 (K8s).
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ishaan-jindal/runnix/internal/config"
	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if cfg.NATSURL != "" {
		c, err := rnats.Connect(cfg.NATSURL)
		if err != nil {
			log.Printf("dispatcher: NATS unavailable at %s (%v) — running offline (scaffold)", cfg.NATSURL, err)
		} else {
			log.Printf("dispatcher: connected to NATS %s (streams %s/%s reserved)", cfg.NATSURL, rnats.StreamSubmit, rnats.StreamResult)
			defer c.Close()
		}
	}

	log.Print("dispatcher stub running (Job creation deferred to Phase 2). Press Ctrl-C to exit.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("dispatcher shutting down")
}
