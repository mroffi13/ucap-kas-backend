package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/modules/auth"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type stubVerifier struct{}

func (s *stubVerifier) Verify(_ context.Context, rawToken string) (*auth.VerifiedGoogleClaims, error) {
	if rawToken == "valid_google_token_budi" {
		return &auth.VerifiedGoogleClaims{
			Subject:  "google_sub_budi_001",
			Email:    "budi@bengkeljaya.id",
			FullName: "Budi Santoso",
			Picture:  "https://lh3.googleusercontent.com/a/budi",
		}, nil
	}
	return nil, errors.New("invalid signature")
}

type memoryAuthStore struct {
	mu          sync.Mutex
	usersBySub  map[string]sqlc.User
	usersByID   map[uuid.UUID]sqlc.User
	workspaces  map[uuid.UUID][]sqlc.ListWorkspacesByUserIDRow
	sessions    map[string]sqlc.AuthSession
	auditEvents []string
}

func newMemoryAuthStore() *memoryAuthStore {
	return &memoryAuthStore{
		usersBySub: make(map[string]sqlc.User),
		usersByID:  make(map[uuid.UUID]sqlc.User),
		workspaces: make(map[uuid.UUID][]sqlc.ListWorkspacesByUserIDRow),
		sessions:   make(map[string]sqlc.AuthSession),
	}
}

func (m *memoryAuthStore) UpsertUserAndDefaultWorkspaceTx(
	_ context.Context,
	params auth.LoginTxParams,
) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, exists := m.usersBySub[params.GoogleSub]
	if !exists {
		u = sqlc.User{
			ID:        params.NewUserID,
			GoogleSub: params.GoogleSub,
			Email:     params.Email,
			FullName:  params.FullName,
			AvatarUrl: params.AvatarURL,
			PlanTier:  "starter",
		}
	} else {
		u.Email = params.Email
		u.FullName = params.FullName
	}
	m.usersBySub[params.GoogleSub] = u
	m.usersByID[u.ID] = u

	wsList := m.workspaces[u.ID]
	if len(wsList) == 0 {
		wsRow := sqlc.ListWorkspacesByUserIDRow{
			ID:        params.NewWorkspaceID,
			OwnerID:   u.ID,
			Name:      params.DefaultWorkspaceName,
			ShortName: params.DefaultShortName,
			FocusType: params.FocusType,
			IsDefault: true,
			Role:      "owner",
		}
		wsList = []sqlc.ListWorkspacesByUserIDRow{wsRow}
		m.workspaces[u.ID] = wsList
	}

	m.sessions[params.RefreshTokenHash] = sqlc.AuthSession{
		ID:               params.SessionID,
		UserID:           u.ID,
		RefreshTokenHash: params.RefreshTokenHash,
		DeviceName:       params.DeviceName,
		IpAddress:        params.IPAddress,
		ExpiresAt:        params.ExpiresAt,
	}
	m.auditEvents = append(m.auditEvents, "LOGIN_GOOGLE")
	return u, wsList, nil
}

func (m *memoryAuthStore) GetSessionByHash(_ context.Context, hash string) (sqlc.AuthSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[hash]
	if !ok {
		return sqlc.AuthSession{}, pgx.ErrNoRows
	}
	return sess, nil
}

func (m *memoryAuthStore) RotateSessionTx(
	_ context.Context,
	oldSessionID uuid.UUID,
	next sqlc.CreateAuthSessionParams,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, sess := range m.sessions {
		if sess.ID == oldSessionID {
			sess.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			m.sessions[k] = sess
		}
	}
	m.sessions[next.RefreshTokenHash] = sqlc.AuthSession{
		ID:               next.ID,
		UserID:           next.UserID,
		RefreshTokenHash: next.RefreshTokenHash,
		DeviceName:       next.DeviceName,
		IpAddress:        next.IpAddress,
		ExpiresAt:        next.ExpiresAt,
	}
	return nil
}

func (m *memoryAuthStore) HandleTokenReuseAttack(_ context.Context, userID uuid.UUID, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, sess := range m.sessions {
		if sess.UserID == userID && !sess.RevokedAt.Valid {
			sess.RevokedAt = pgtype.Timestamptz{Time: now, Valid: true}
			m.sessions[k] = sess
		}
	}
	m.auditEvents = append(m.auditEvents, "REFRESH_TOKEN_REUSE_DETECTED")
	return nil
}

func (m *memoryAuthStore) RevokeSessionByHash(_ context.Context, userID uuid.UUID, hash string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[hash]; ok && sess.UserID == userID {
		sess.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		m.sessions[hash] = sess
	}
	return nil
}

func (m *memoryAuthStore) GetUserAndWorkspaces(_ context.Context, userID uuid.UUID) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[userID]
	if !ok {
		return sqlc.User{}, nil, pgx.ErrNoRows
	}
	return u, m.workspaces[userID], nil
}

func TestGoogleAuthFlowRotationAndReuseAttack(t *testing.T) {
	jwtMgr, _ := jwtutil.NewManager("0123456789abcdef0123456789abcdef_strong_key!", 15*time.Minute)
	store := newMemoryAuthStore()
	svc := auth.NewService(store, &stubVerifier{}, jwtMgr, 30*24*time.Hour)
	h := auth.NewHandler(svc)

	r := chi.NewRouter()
	r.Post("/api/v1/auth/google", h.GoogleSignIn)
	r.Post("/api/v1/auth/refresh", h.Refresh)
	r.Group(func(pr chi.Router) {
		pr.Use(middleware.RequireAuth(jwtMgr))
		pr.Get("/api/v1/auth/me", h.Me)
		pr.Post("/api/v1/auth/logout", h.Logout)
	})

	// 1. Forged Google token must return 401
	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/google", bytes.NewBufferString(`{"id_token":"forged","selected_focus":"umkm"}`))
	badRec := httptest.NewRecorder()
	r.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged token, got %d", badRec.Code)
	}

	// 2. Valid Google login creates user + "Dapur Rumah Tangga" workspace
	loginReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/google",
		bytes.NewBufferString(`{"id_token":"valid_google_token_budi","selected_focus":"rumah_tangga","device_name":"iPhone 15"}`),
	)
	loginRec := httptest.NewRecorder()
	r.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on Google login, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	var loginResp struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			User         struct {
				Email    string `json:"email"`
				FullName string `json:"full_name"`
			} `json:"user"`
			Workspaces []struct {
				Name      string `json:"name"`
				FocusType string `json:"focus_type"`
			} `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("json unmarshal error: %v", err)
	}
	if loginResp.Data.Workspaces[0].Name != "Dapur Rumah Tangga" {
		t.Fatalf("expected default workspace Dapur Rumah Tangga, got %s", loginResp.Data.Workspaces[0].Name)
	}

	// 3. Rotate refresh token -> returns new refresh_token
	firstRefresh := loginResp.Data.RefreshToken
	rotReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/refresh",
		bytes.NewBufferString(`{"refresh_token":"`+firstRefresh+`"}`),
	)
	rotRec := httptest.NewRecorder()
	r.ServeHTTP(rotRec, rotReq)
	if rotRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on token rotation, got %d: %s", rotRec.Code, rotRec.Body.String())
	}

	var rotResp struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rotRec.Body.Bytes(), &rotResp)
	secondRefresh := rotResp.Data.RefreshToken
	if secondRefresh == firstRefresh || secondRefresh == "" {
		t.Fatal("expected rotated refresh token to differ from first refresh token")
	}

	// 4. Token Reuse Attack: attacker replays firstRefresh -> must fail 401 AND revoke secondRefresh too!
	reuseReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/refresh",
		bytes.NewBufferString(`{"refresh_token":"`+firstRefresh+`"}`),
	)
	reuseRec := httptest.NewRecorder()
	r.ServeHTTP(reuseRec, reuseReq)
	if reuseRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on refresh token reuse attack, got %d", reuseRec.Code)
	}

	// Verify secondRefresh was also revoked by the reuse detector!
	afterAttackReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/refresh",
		bytes.NewBufferString(`{"refresh_token":"`+secondRefresh+`"}`),
	)
	afterAttackRec := httptest.NewRecorder()
	r.ServeHTTP(afterAttackRec, afterAttackReq)
	if afterAttackRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected active session to be revoked after token reuse attack, got %d", afterAttackRec.Code)
	}
}
