package config

import (
	"testing"
	"time"
)

// clearEnv blanks every config var so a test starts from defaults. Empty is
// equivalent to unset for Load (it only reads non-empty values).
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PORT", "DATABASE_URL", "NATS_URL", "JWT_SECRET", "ENV", "EXEC_WORKERS", "RUNNER_IMAGE", "RUNNER_RUNTIME", "WEBHOOK_SIGNING_SECRET", "WEBHOOK_ALLOW_PRIVATE", "REAP_INTERVAL", "REAP_STALE_AFTER", "STALE_QUEUED_AFTER", "QUOTAS_ENABLED"} {
		t.Setenv(k, "")
	}
}

// devEnv pins the only environment allowed to omit JWT_SECRET.
func devEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("ENV", "development")
}

func TestLoadDefaults(t *testing.T) {
	devEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Environment != "development" {
		t.Fatalf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.Port != 4000 {
		t.Fatalf("Port = %d, want 4000", cfg.Port)
	}
	if cfg.DatabaseURL == "" || cfg.NATSURL == "" {
		t.Fatal("expected default DATABASE_URL and NATS_URL")
	}
	if cfg.ExecWorkers != 2 || cfg.RunnerImage != "runnix-runner:local" || cfg.RunnerRuntime != "runsc" {
		t.Fatalf("dispatcher defaults = %+v", cfg)
	}
	if cfg.ReapInterval != time.Minute || cfg.ReapStaleAfter != 5*time.Minute {
		t.Fatalf("reaper defaults = %+v", cfg)
	}
	if cfg.WebhookSigningSecret != "" || cfg.WebhookAllowPrivate {
		t.Fatalf("webhook defaults = %+v", cfg)
	}
	if !cfg.QuotasEnabled {
		t.Fatalf("quotas default = %+v, want enabled", cfg)
	}
}

func TestLoadDispatcherEnv(t *testing.T) {
	devEnv(t)
	t.Setenv("EXEC_WORKERS", "4")
	t.Setenv("RUNNER_IMAGE", "runnix-runner:test")
	t.Setenv("RUNNER_RUNTIME", "runc")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.ExecWorkers != 4 || cfg.RunnerImage != "runnix-runner:test" || cfg.RunnerRuntime != "runc" {
		t.Fatalf("dispatcher config = %+v", cfg)
	}
}

func TestLoadQuotasEnv(t *testing.T) {
	devEnv(t)
	t.Setenv("QUOTAS_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.QuotasEnabled {
		t.Fatalf("quotas config = %+v, want disabled", cfg)
	}
}

func TestLoadRejectsBadWorkers(t *testing.T) {
	devEnv(t)
	t.Setenv("EXEC_WORKERS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for EXEC_WORKERS=0")
	}
}

// An unset ENV must fail closed: no default means no accidental unauthenticated
// deployment.
func TestLoadRequiresJWTSecretWhenEnvUnset(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil {
		t.Fatal("expected error when ENV and JWT_SECRET are both unset")
	}
}

// An unset ENV with a secret is a valid deployment: auth is enforced and the
// environment stays empty rather than being defaulted.
func TestLoadEnvUnsetWithJWTSecret(t *testing.T) {
	clearEnv(t)
	t.Setenv("JWT_SECRET", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Environment != "" {
		t.Fatalf("Environment = %q, want empty (unset keeps no default)", cfg.Environment)
	}
	if cfg.JWTSecret != "secret" {
		t.Fatalf("JWTSecret = %q, want secret", cfg.JWTSecret)
	}
}

// Only the exact string "development" relaxes the secret requirement.
func TestLoadRequiresJWTSecretOutsideDev(t *testing.T) {
	for _, env := range []string{"production", "staging", "dev", "Development", " "} {
		t.Run(env, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENV", env)
			if _, err := Load(); err == nil {
				t.Fatalf("expected error when JWT_SECRET missing with ENV=%q", env)
			}
		})
	}
}

func TestLoadWebhookAndReaperEnv(t *testing.T) {
	devEnv(t)
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
		{"QUOTAS_ENABLED", "yesplease"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			devEnv(t)
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() = nil, want error for %s=%q", tc.key, tc.val)
			}
		})
	}
}
