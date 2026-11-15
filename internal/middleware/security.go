package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/artech/ucapkas_backend/internal/pkg/response"
	"github.com/google/uuid"
)

type ctxKey string

const userIDKey ctxKey = "auth_user_id"

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func LimitBodyBytes(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func RequireAuth(jwtMgr *jwtutil.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
			if !strings.HasPrefix(authHeader, "Bearer ") {
				response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Sesi login tidak ditemukan. Silakan masuk kembali.")
				return
			}
			rawToken := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
			uid, err := jwtMgr.ValidateAccessToken(rawToken)
			if err != nil {
				response.Error(w, http.StatusUnauthorized, "TOKEN_EXPIRED", "Sesi akses telah berakhir. Silakan muat ulang sesi.")
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, uid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	uid, ok := ctx.Value(userIDKey).(uuid.UUID)
	return uid, ok
}

type ipBucket struct {
	count   int
	resetAt time.Time
}

func NewIPRateLimiter(requestsPerMinute int) func(http.Handler) http.Handler {
	var mu sync.Mutex
	buckets := make(map[string]*ipBucket)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			now := time.Now()

			mu.Lock()
			// Opportunistic cleanup to keep memory tiny on 1GB VPS
			if len(buckets) > 2000 {
				for k, v := range buckets {
					if now.After(v.resetAt) {
						delete(buckets, k)
					}
				}
			}
			b, exists := buckets[ip]
			if !exists || now.After(b.resetAt) {
				b = &ipBucket{count: 0, resetAt: now.Add(time.Minute)}
				buckets[ip] = b
			}
			b.count++
			overLimit := b.count > requestsPerMinute
			mu.Unlock()

			if overLimit {
				response.Error(w, http.StatusTooManyRequests, "RATE_LIMITED", "Terlalu banyak percobaan. Silakan tunggu sebentar.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
