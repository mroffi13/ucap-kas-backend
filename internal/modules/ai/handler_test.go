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
	mu         sync.Mutex
	users      map[uuid.UUID]sqlc.User
	wsOwners   map[uuid.UUID]uuid.UUID            // wsID -> ownerID
	members    map[uuid.UUID]map[uuid.UUID]string // wsID -> userID -> role
	logs       []sqlc.AiUsageLog
	auditLogs  []sqlc.InsertAuditLogParams
	weeklyCap  bool // when false, simulates logs spread across earlier weeks in the same month
}

func newMemoryAIStore() *memoryAIStore {
	return &memoryAIStore{
		users:     make(map[uuid.UUID]sqlc.User),
		wsOwners:  make(map[uuid.UUID]uuid.UUID),
		members:   make(map[uuid.UUID]map[uuid.UUID]string),
		weeklyCap: true,
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
	if role == "owner" {
		m.wsOwners[wsID] = userID
	}
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

func (m *memoryAIStore) GetWorkspaceOwnerUser(_ context.Context, workspaceID uuid.UUID) (sqlc.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ownerID, ok := m.wsOwners[workspaceID]
	if !ok {
		return sqlc.User{}, errors.New("workspace not found")
	}
	u, ok := m.users[ownerID]
	if !ok {
		return sqlc.User{}, errors.New("owner not found")
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

func (m *memoryAIStore) CountMonthlyAIUsageByOwner(_ context.Context, ownerID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, l := range m.logs {
		if m.wsOwners[l.WorkspaceID] == ownerID {
			count++
		}
	}
	return count, nil
}

func (m *memoryAIStore) CountWeeklyAIUsageByOwner(_ context.Context, ownerID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.weeklyCap {
		return 0, nil
	}
	var count int64
	for _, l := range m.logs {
		if m.wsOwners[l.WorkspaceID] == ownerID {
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

func (m *memoryAIStore) InsertAuditLog(_ context.Context, arg sqlc.InsertAuditLogParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditLogs = append(m.auditLogs, arg)
	return nil
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
	if env.Data.TotalAmount != 130000 {
		t.Errorf("expected total_amount 130000, got %d", env.Data.TotalAmount)
	}
	if env.Data.Quota.Used != 1 || env.Data.Quota.Limit != 15 || env.Data.Quota.WeeklyUsed != 1 || env.Data.Quota.WeeklyLimit != 4 {
		t.Errorf("expected hybrid quota 1/4 weekly & 1/15 monthly, got %+v", env.Data.Quota)
	}
}

func TestStarterAIQuotaEnforcementAndProUnlimited(t *testing.T) {
	r, store, jwtMgr := setupAIRouter(t)
	ownerID, _ := uuid.NewV7()
	adminID, _ := uuid.NewV7()
	ws1ID, _ := uuid.NewV7()
	ws2ID, _ := uuid.NewV7()

	store.seed(ownerID, ws1ID, "starter", "owner")
	store.seed(ownerID, ws2ID, "starter", "owner")
	store.seed(adminID, ws1ID, "starter", "editor")

	ownerToken, _, _ := jwtMgr.GenerateAccessToken(ownerID)
	adminToken, _, _ := jwtMgr.GenerateAccessToken(adminID)
	body, _ := json.Marshal(map[string]string{"preset_id": "sembako"})

	// 1. Hybrid Weekly Limit (4x/week): Owner uses 2x in ws1, Admin uses 2x in ws1 -> 4/4 weekly used!
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws1ID.String()+"/ai/parse-receipt", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("owner request %d failed with %d", i+1, rec.Code)
		}
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws1ID.String()+"/ai/parse-receipt", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("admin request %d failed with %d", i+1, rec.Code)
		}
	}

	// 5th request in the same week (even in ws2 by owner!) must return 403 AI_QUOTA_EXCEEDED (weekly limit 4/4)
	req5 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws2ID.String()+"/ai/parse-receipt", bytes.NewReader(body))
	req5.Header.Set("Authorization", "Bearer "+ownerToken)
	rec5 := httptest.NewRecorder()
	r.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on 5th weekly starter request, got %d: %s", rec5.Code, rec5.Body.String())
	}

	// 2. Simulate subsequent weeks in the same month (weeklyCap = false) -> use remaining 11 out of 15 monthly quota across ws1 & ws2
	store.mu.Lock()
	store.weeklyCap = false
	store.mu.Unlock()

	for i := 4; i < 15; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws2ID.String()+"/ai/parse-receipt", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("monthly request %d failed unexpectedly with %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// 16th monthly request on Starter tier must return 403 AI_QUOTA_EXCEEDED
	req16 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws1ID.String()+"/ai/parse-receipt", bytes.NewReader(body))
	req16.Header.Set("Authorization", "Bearer "+adminToken)
	rec16 := httptest.NewRecorder()
	r.ServeHTTP(rec16, req16)
	if rec16.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on 16th monthly starter request, got %d: %s", rec16.Code, rec16.Body.String())
	}

	// 3. Upgrade owner to umkm_pro -> admin request in ws1 now succeeds
	store.seed(ownerID, ws1ID, "umkm_pro", "owner")
	reqPro := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws1ID.String()+"/ai/parse-receipt", bytes.NewReader(body))
	reqPro.Header.Set("Authorization", "Bearer "+adminToken)
	recPro := httptest.NewRecorder()
	r.ServeHTTP(recPro, reqPro)
	if recPro.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after upgrading owner to umkm_pro, got %d: %s", recPro.Code, recPro.Body.String())
	}
}
