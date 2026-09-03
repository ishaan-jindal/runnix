// Package config loads service configuration from the environment.
// In Kubernetes the same keys are provided via ConfigMap/Secret.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds gateway + dispatcher configuration.
type Config struct {
	Port        int
	DatabaseURL string
	NATSURL     string
	JWTSecret   string
	Environment string // development | staging | production
}

// Load reads configuration from the environment with sane local defaults.
func Load() (Config, error) {
	cfg := Config{
		Port:        4000,
		DatabaseURL: "postgres://runnix:runnix@localhost:5432/runnix?sslmode=disable",
		NATSURL:     "nats://localhost:4222",
		Environment: "development",
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
	cfg.JWTSecret = os.Getenv("JWT_SECRET")
	if cfg.Environment != "development" && cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required outside development")
	}
	return cfg, nil
}

// Addr returns the listen address for the HTTP server.
func (c Config) Addr() string {
	return ":" + strconv.Itoa(c.Port)
}
