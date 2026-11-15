package config_test

import (
	"testing"

	"github.com/artech/ucapkas_backend/internal/config"
)

func TestLoadRejectsShortJWTSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://ucapkas:secret@localhost:5432/ucapkas?sslmode=disable")
	t.Setenv("JWT_SECRET", "terlalu-pendek")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when JWT_SECRET is shorter than 32 chars, got nil")
	}
}

func TestLoadDisablesDevMockInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://ucapkas:secret@localhost:5432/ucapkas?sslmode=require")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef_strong_key!")
	t.Setenv("ALLOW_DEV_MOCK_AUTH", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AllowDevMockAuth {
		t.Fatal("expected AllowDevMockAuth to be forced false in production")
	}
}

func TestLoadValidConfig(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PORT", "8080")
	t.Setenv("DATABASE_URL", "postgres://ucapkas:secret@localhost:5432/ucapkas?sslmode=disable")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef_strong_key!")
	t.Setenv("GOOGLE_CLIENT_IDS", "ios-client.apps.googleusercontent.com,android-client.apps.googleusercontent.com")
	t.Setenv("ALLOW_DEV_MOCK_AUTH", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(cfg.GoogleClientIDs) != 2 {
		t.Fatalf("expected 2 Google client IDs, got %d", len(cfg.GoogleClientIDs))
	}
	if !cfg.AllowDevMockAuth {
		t.Fatal("expected AllowDevMockAuth to be true in development when explicitly set")
	}
}
