package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "DATABASE_URL", "NATS_URL", "JWT_SECRET", "ENV"} {
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
