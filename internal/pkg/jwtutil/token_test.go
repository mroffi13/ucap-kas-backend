package jwtutil_test

import (
	"testing"
	"time"

	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/google/uuid"
)

func TestAccessAndRefreshTokens(t *testing.T) {
	mgr, err := jwtutil.NewManager("0123456789abcdef0123456789abcdef_strong_key!", 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	uid, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7 error: %v", err)
	}

	accessToken, expiresIn, err := mgr.GenerateAccessToken(uid)
	if err != nil {
		t.Fatalf("generate access token failed: %v", err)
	}
	if expiresIn != 900 {
		t.Fatalf("expected expiresIn 900, got %d", expiresIn)
	}

	parsedUID, err := mgr.ValidateAccessToken(accessToken)
	if err != nil {
		t.Fatalf("validate access token failed: %v", err)
	}
	if parsedUID != uid {
		t.Fatalf("expected uid %s, got %s", uid, parsedUID)
	}

	// Tampered token must fail validation
	if _, err := mgr.ValidateAccessToken(accessToken + "tampered"); err == nil {
		t.Fatal("expected tampered token validation to fail")
	}

	rawRefresh, hashHex, err := jwtutil.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("generate refresh token failed: %v", err)
	}
	if len(hashHex) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d", len(hashHex))
	}
	if jwtutil.HashRefreshToken(rawRefresh) != hashHex {
		t.Fatal("hash mismatch for refresh token")
	}
}
