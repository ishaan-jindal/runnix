// Package config loads service configuration from the environment.
// In Kubernetes the same keys are provided via ConfigMap/Secret.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds gateway + dispatcher configuration.
type Config struct {
	Port        int
	DatabaseURL string
	NATSURL     string
	JWTSecret   string
	Environment string // development | staging | production

	// Dispatcher execution loop.
	ExecWorkers   int    // EXEC_WORKERS: concurrent sandbox workers
	RunnerImage   string // RUNNER_IMAGE: image untrusted code runs in
	RunnerRuntime string // RUNNER_RUNTIME: container runtime ("" = daemon default, "runsc" = gVisor)

	// Webhook delivery. Without a signing secret the gateway rejects
	// webhook_url submissions (webhooks off) and the dispatcher never signs.
	WebhookSigningSecret string // WEBHOOK_SIGNING_SECRET: HMAC-SHA256 key for signed delivery
	WebhookAllowPrivate  bool   // WEBHOOK_ALLOW_PRIVATE: allow private/loopback webhook hosts (dev/tests)

	// Stale-running reaper (dispatcher).
	ReapInterval   time.Duration // REAP_INTERVAL: sweep frequency
	ReapStaleAfter time.Duration // REAP_STALE_AFTER: "running" older than this is failed; must exceed the 60s max timeout_s
}

// Load reads configuration from the environment with sane local defaults.
func Load() (Config, error) {
	cfg := Config{
		Port:           4000,
		DatabaseURL:    "postgres://runnix:runnix@localhost:5432/runnix?sslmode=disable",
		NATSURL:        "nats://localhost:4222",
		Environment:    "development",
		ExecWorkers:    2,
		RunnerImage:    "runnix-runner-python:local",
		RunnerRuntime:  "runsc",
		ReapInterval:   time.Minute,
		ReapStaleAfter: 5 * time.Minute,
	}

	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid PORT %q: %w", v, err)
		}
		cfg.Port = p
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := os.Getenv("NATS_URL"); v != "" {
		cfg.NATSURL = v
	}
	if v := os.Getenv("ENV"); v != "" {
		cfg.Environment = v
	}
	if v := os.Getenv("EXEC_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid EXEC_WORKERS %q", v)
		}
		cfg.ExecWorkers = n
	}
	if v := os.Getenv("RUNNER_IMAGE"); v != "" {
		cfg.RunnerImage = v
	}
	if v := os.Getenv("RUNNER_RUNTIME"); v != "" {
		cfg.RunnerRuntime = v
	}
	if v := os.Getenv("WEBHOOK_ALLOW_PRIVATE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid WEBHOOK_ALLOW_PRIVATE %q", v)
		}
		cfg.WebhookAllowPrivate = b
	}
	if v := os.Getenv("REAP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid REAP_INTERVAL %q", v)
		}
		cfg.ReapInterval = d
	}
	if v := os.Getenv("REAP_STALE_AFTER"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid REAP_STALE_AFTER %q", v)
		}
		cfg.ReapStaleAfter = d
	}
	cfg.JWTSecret = os.Getenv("JWT_SECRET")
	if cfg.Environment != "development" && cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required outside development")
	}
	cfg.WebhookSigningSecret = os.Getenv("WEBHOOK_SIGNING_SECRET")
	return cfg, nil
}

// Addr returns the listen address for the HTTP server.
func (c Config) Addr() string {
	return ":" + strconv.Itoa(c.Port)
}
