package ai_test

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
	"github.com/artech/ucapkas_backend/internal/modules/ai"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type memoryAIStore struct {
	mu      sync.Mutex
	users   map[uuid.UUID]sqlc.User
	members map[uuid.UUID]map[uuid.UUID]string // wsID -> userID -> role
	logs    []sqlc.AiUsageLog
}

func newMemoryAIStore() *memoryAIStore {
	return &memoryAIStore{
		users:   make(map[uuid.UUID]sqlc.User),
		members: make(map[uuid.UUID]map[uuid.UUID]string),
	}
}

func (m *memoryAIStore) seed(userID, wsID uuid.UUID, tier string, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[userID] = sqlc.User{
		ID:       userID,
		Email:    "user@ucapkas.id",
		FullName: "Budi Bengkel",
		PlanTier: tier,
	}
	if m.members[wsID] == nil {
		m.members[wsID] = make(map[uuid.UUID]string)
	}
	m.members[wsID][userID] = role
}

func (m *memoryAIStore) GetWorkspaceRole(_ context.Context, workspaceID, userID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ws, ok := m.members[workspaceID]; ok {
		if role, ok := ws[userID]; ok {
			return role, nil
		}
	}
	return "", errors.New("not a member")
}

func (m *memoryAIStore) GetUserByID(_ context.Context, userID uuid.UUID) (sqlc.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return sqlc.User{}, errors.New("user not found")
	}
	return u, nil
}

func (m *memoryAIStore) CountMonthlyAIUsageByUser(_ context.Context, userID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, l := range m.logs {
		if l.UserID == userID {
			count++
		}
	}
	return count, nil
}

func (m *memoryAIStore) InsertAIUsageLog(_ context.Context, arg sqlc.InsertAIUsageLogParams) (sqlc.AiUsageLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := sqlc.AiUsageLog{
		ID:          arg.ID,
		WorkspaceID: arg.WorkspaceID,
		UserID:      arg.UserID,
		FeatureType: arg.FeatureType,
		Provider:    arg.Provider,
		TokensUsed:  arg.TokensUsed,
	}
	m.logs = append(m.logs, entry)
	return entry, nil
}

func setupAIRouter(t *testing.T) (*chi.Mux, *memoryAIStore, *jwtutil.Manager) {
	t.Helper()
	store := newMemoryAIStore()
	parser := ai.NewHybridParser("", "gemini-2.5-flash")
	svc := ai.NewService(store, parser)
	h := ai.NewHandler(svc)

	jwtMgr, err := jwtutil.NewManager("super_secret_test_key_at_least_32_bytes_long", 15*time.Minute)
	if err != nil {
		t.Fatalf("failed to create jwt manager: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/api/v1/workspaces/{workspaceID}/ai", func(ar chi.Router) {
		ar.Use(middleware.RequireAuth(jwtMgr))
		ar.Get("/quota", h.GetQuota)
		ar.Post("/parse-voice", h.ParseVoice)
		ar.Post("/parse-receipt", h.ParseReceipt)
	})
	return r, store, jwtMgr
}

func TestParseVoiceSlangAndMultiItem(t *testing.T) {
	r, store, jwtMgr := setupAIRouter(t)
	userID, _ := uuid.NewV7()
	wsID, _ := uuid.NewV7()
	store.seed(userID, wsID, "starter", "owner")

	token, _, _ := jwtMgr.GenerateAccessToken(userID)

	payload := map[string]string{
		"transcript": "Ada uang masuk dari Pak Husni buat ganti oli 2 botol @35rb sama busi gocap sama baut ceban, dibayar via QRIS",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/ai/parse-voice", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data struct {
			Intent        string              `json:"intent"`
			Type          string              `json:"type"`
			CustomerName  string              `json:"customer_name"`
			PaymentMethod string              `json:"payment_method"`
			TotalAmount   int64               `json:"total_amount"`
			Items         []ai.ParsedLineItem `json:"items"`
			Quota         ai.QuotaStatus      `json:"quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if env.Data.Intent != "buku_kas" {
		t.Errorf("expected intent buku_kas, got %s", env.Data.Intent)
	}
	if env.Data.Type != "inflow" {
		t.Errorf("expected type inflow, got %s", env.Data.Type)
	}
	if env.Data.PaymentMethod != "QRIS" {
		t.Errorf("expected payment_method QRIS, got %s", env.Data.PaymentMethod)
	}
	if len(env.Data.Items) != 3 {
		t.Fatalf("expected 3 items, got %d (%+v)", len(env.Data.Items), env.Data.Items)
	}
	// 2 * 35.000 (70.000) + gocap (50.000) + ceban (10.000) = 130.000
	if env.Data.TotalAmount != 130000 {
		t.Errorf("expected total_amount 130000, got %d", env.Data.TotalAmount)
	}
	if env.Data.Quota.Used != 1 || env.Data.Quota.Limit != 15 {
		t.Errorf("expected quota 1/15, got %d/%d", env.Data.Quota.Used, env.Data.Quota.Limit)
	}
}

func TestStarterAIQuotaEnforcementAndProUnlimited(t *testing.T) {
	r, store, jwtMgr := setupAIRouter(t)
	userID, _ := uuid.NewV7()
	wsID, _ := uuid.NewV7()
	store.seed(userID, wsID, "starter", "owner")

	token, _, _ := jwtMgr.GenerateAccessToken(userID)
	body, _ := json.Marshal(map[string]string{"preset_id": "sembako"})

	// Use up all 15 free Starter requests
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/ai/parse-receipt", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d failed unexpectedly with %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// 16th request on Starter tier must return 403 AI_QUOTA_EXCEEDED
	req16 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/ai/parse-receipt", bytes.NewReader(body))
	req16.Header.Set("Authorization", "Bearer "+token)
	rec16 := httptest.NewRecorder()
	r.ServeHTTP(rec16, req16)

	if rec16.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on 16th starter request, got %d: %s", rec16.Code, rec16.Body.String())
	}

	// Upgrade user to umkm_pro -> 16th request now succeeds
	store.seed(userID, wsID, "umkm_pro", "owner")
	reqPro := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/ai/parse-receipt", bytes.NewReader(body))
	reqPro.Header.Set("Authorization", "Bearer "+token)
	recPro := httptest.NewRecorder()
	r.ServeHTTP(recPro, reqPro)

	if recPro.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after upgrading to umkm_pro, got %d: %s", recPro.Code, recPro.Body.String())
	}
}
