package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/google/uuid"
)

func TestSecurityHeadersAndAuthGuard(t *testing.T) {
	mgr, _ := jwtutil.NewManager("0123456789abcdef0123456789abcdef_strong_key!", 15*time.Minute)
	uid, _ := uuid.NewV7()
	validToken, _, _ := mgr.GenerateAccessToken(uid)

	protected := middleware.SecurityHeaders(
		middleware.RequireAuth(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUID, ok := middleware.UserIDFromContext(r.Context())
			if !ok || gotUID != uid {
				t.Fatalf("expected uid in context")
			}
			w.WriteHeader(http.StatusOK)
		})),
	)

	// 1. Tanpa token -> 401 + Security Headers tetap terpasang
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec1 := httptest.NewRecorder()
	protected.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec1.Code)
	}
	if rec1.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options header")
	}

	// 2. Dengan token valid -> 200 OK
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req2.Header.Set("Authorization", "Bearer "+validToken)
	rec2 := httptest.NewRecorder()
	protected.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
}

func TestIPRateLimiterBlocksBurst(t *testing.T) {
	limiter := middleware.NewIPRateLimiter(2)
	handler := limiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/google", nil)
		req.RemoteAddr = "203.0.113.10:54321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d expected 200, got %d", i+1, rec.Code)
		}
	}

	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/google", nil)
	req3.RemoteAddr = "203.0.113.10:54321"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 3rd request, got %d", rec3.Code)
	}
}
