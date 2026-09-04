package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "DATABASE_URL", "NATS_URL", "JWT_SECRET", "ENV", "EXEC_WORKERS", "RUNNER_IMAGE", "RUNNER_RUNTIME", "WEBHOOK_SIGNING_SECRET", "WEBHOOK_ALLOW_PRIVATE", "REAP_INTERVAL", "REAP_STALE_AFTER"} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Port != 4000 {
		t.Fatalf("Port = %d, want 4000", cfg.Port)
	}
	if cfg.DatabaseURL == "" || cfg.NATSURL == "" {
		t.Fatal("expected default DATABASE_URL and NATS_URL")
	}
	if cfg.ExecWorkers != 2 || cfg.RunnerImage == "" || cfg.RunnerRuntime != "runsc" {
		t.Fatalf("dispatcher defaults = %+v", cfg)
	}
	if cfg.ReapInterval != time.Minute || cfg.ReapStaleAfter != 5*time.Minute {
		t.Fatalf("reaper defaults = %+v", cfg)
	}
	if cfg.WebhookSigningSecret != "" || cfg.WebhookAllowPrivate {
		t.Fatalf("webhook defaults = %+v", cfg)
	}
}

func TestLoadDispatcherEnv(t *testing.T) {
	t.Setenv("EXEC_WORKERS", "4")
	t.Setenv("RUNNER_IMAGE", "python:3.12-slim")
	t.Setenv("RUNNER_RUNTIME", "runc")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ExecWorkers != 4 || cfg.RunnerImage != "python:3.12-slim" || cfg.RunnerRuntime != "runc" {
		t.Fatalf("dispatcher config = %+v", cfg)
	}
}

func TestLoadRejectsBadWorkers(t *testing.T) {
	t.Setenv("EXEC_WORKERS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for EXEC_WORKERS=0")
	}
}

func TestLoadRequiresJWTSecretOutsideDev(t *testing.T) {
	t.Setenv("ENV", "production")
	if err := os.Unsetenv("JWT_SECRET"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected error when JWT_SECRET missing in production")
	}
}

func TestLoadWebhookAndReaperEnv(t *testing.T) {
	t.Setenv("WEBHOOK_SIGNING_SECRET", "hook-secret")
	t.Setenv("WEBHOOK_ALLOW_PRIVATE", "true")
	t.Setenv("REAP_INTERVAL", "2s")
	t.Setenv("REAP_STALE_AFTER", "10m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.WebhookSigningSecret != "hook-secret" || !cfg.WebhookAllowPrivate {
		t.Fatalf("webhook config = %+v", cfg)
	}
	if cfg.ReapInterval != 2*time.Second || cfg.ReapStaleAfter != 10*time.Minute {
		t.Fatalf("reaper config = %+v", cfg)
	}
}

func TestLoadRejectsBadWebhookAndReaperEnv(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"WEBHOOK_ALLOW_PRIVATE", "yesplease"},
		{"REAP_INTERVAL", "soon"},
		{"REAP_INTERVAL", "0s"},
		{"REAP_STALE_AFTER", "-5m"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() = nil, want error for %s=%q", tc.key, tc.val)
			}
		})
	}
}
