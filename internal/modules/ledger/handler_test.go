package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/modules/ledger"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type memoryLedgerStore struct {
	mu           sync.Mutex
	users        map[uuid.UUID]sqlc.User
	workspaces   map[uuid.UUID]sqlc.Workspace
	members      map[uuid.UUID]map[uuid.UUID]string // wsID -> userID -> role
	transactions map[uuid.UUID][]sqlc.CashTransaction
	lineItems    map[uuid.UUID][]sqlc.TransactionLineItem
}

func newMemoryLedgerStore() *memoryLedgerStore {
	return &memoryLedgerStore{
		users:        make(map[uuid.UUID]sqlc.User),
		workspaces:   make(map[uuid.UUID]sqlc.Workspace),
		members:      make(map[uuid.UUID]map[uuid.UUID]string),
		transactions: make(map[uuid.UUID][]sqlc.CashTransaction),
		lineItems:    make(map[uuid.UUID][]sqlc.TransactionLineItem),
	}
}

func (m *memoryLedgerStore) seedUserWithDefaultWorkspace(userID, wsID uuid.UUID, planTier string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[userID] = sqlc.User{
		ID:       userID,
		Email:    "owner@ucapkas.id",
		FullName: "Pemilik Kas",
		PlanTier: planTier,
	}
	m.workspaces[wsID] = sqlc.Workspace{
		ID:        wsID,
		OwnerID:   userID,
		Name:      "Usaha Saya (UMKM)",
		ShortName: "Usaha Saya",
		FocusType: "umkm",
		IsDefault: true,
	}
	m.members[wsID] = map[uuid.UUID]string{userID: "owner"}
}

func (m *memoryLedgerStore) setPlan(userID uuid.UUID, planTier string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[userID]
	u.PlanTier = planTier
	m.users[userID] = u
}

func (m *memoryLedgerStore) buildWorkspaceRowsLocked(userID uuid.UUID) []sqlc.ListWorkspacesByUserIDRow {
	var out []sqlc.ListWorkspacesByUserIDRow
	for wsID, ws := range m.workspaces {
		role, ok := m.members[wsID][userID]
		if !ok {
			continue
		}
		var balance, inflow, outflow int64
		for _, tx := range m.transactions[wsID] {
			if tx.DeletedAt.Valid {
				continue
			}
			if tx.Type == "inflow" {
				balance += tx.Amount
				inflow += tx.Amount
			} else {
				balance -= tx.Amount
				outflow += tx.Amount
			}
		}
		out = append(out, sqlc.ListWorkspacesByUserIDRow{
			ID:                ws.ID,
			OwnerID:           ws.OwnerID,
			Name:              ws.Name,
			ShortName:         ws.ShortName,
			FocusType:         ws.FocusType,
			BankName:          ws.BankName,
			BankAccountNumber: ws.BankAccountNumber,
			BankAccountHolder: ws.BankAccountHolder,
			IsDefault:         ws.IsDefault,
			Role:              role,
			Balance:           balance,
			MonthlyInflow:     inflow,
			MonthlyOutflow:    outflow,
		})
	}
	return out
}

func (m *memoryLedgerStore) ListWorkspaces(_ context.Context, userID uuid.UUID) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryLedgerStore) CreateWorkspaceTx(
	_ context.Context,
	userID uuid.UUID,
	input ledger.CreateWorkspaceInput,
	_ string,
) (sqlc.User, sqlc.Workspace, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user := m.users[userID]

	var owned int32
	for _, ws := range m.workspaces {
		if ws.OwnerID == userID {
			owned++
		}
	}
	maxAllowed := int32(1)
	if user.PlanTier == "keluarga_pro" || user.PlanTier == "umkm_pro" {
		maxAllowed = 3
	}
	if owned >= maxAllowed {
		return user, sqlc.Workspace{}, nil, ledger.ErrWorkspaceLimitReached
	}

	wsID, _ := uuid.NewV7()
	ws := sqlc.Workspace{
		ID:                wsID,
		OwnerID:           userID,
		Name:              input.Name,
		ShortName:         input.ShortName,
		FocusType:         input.FocusType,
		BankName:          input.BankName,
		BankAccountNumber: input.BankAccountNumber,
		BankAccountHolder: input.BankAccountHolder,
		IsDefault:         owned == 0,
	}
	m.workspaces[wsID] = ws
	m.members[wsID] = map[uuid.UUID]string{userID: "owner"}
	return user, ws, m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryLedgerStore) UpdateWorkspaceTx(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	input ledger.UpdateWorkspaceInput,
	_ string,
) (sqlc.User, sqlc.Workspace, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[workspaceID][userID] != "owner" {
		return sqlc.User{}, sqlc.Workspace{}, nil, ledger.ErrForbidden
	}
	ws, ok := m.workspaces[workspaceID]
	if !ok {
		return sqlc.User{}, sqlc.Workspace{}, nil, ledger.ErrNotFound
	}
	ws.Name = input.Name
	ws.ShortName = input.ShortName
	ws.FocusType = input.FocusType
	ws.BankName = input.BankName
	ws.BankAccountNumber = input.BankAccountNumber
	ws.BankAccountHolder = input.BankAccountHolder
	m.workspaces[workspaceID] = ws
	return m.users[userID], ws, m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryLedgerStore) ListTransactions(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	_, _ int32,
) ([]sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[workspaceID][userID]; !ok {
		return nil, nil, sqlc.User{}, nil, ledger.ErrForbidden
	}
	var activeTxs []sqlc.CashTransaction
	var activeItems []sqlc.TransactionLineItem
	for _, tx := range m.transactions[workspaceID] {
		if !tx.DeletedAt.Valid {
			activeTxs = append(activeTxs, tx)
			activeItems = append(activeItems, m.lineItems[tx.ID]...)
		}
	}
	return activeTxs, activeItems, m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryLedgerStore) CreateTransactionTx(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	txParams sqlc.CreateCashTransactionParams,
	itemParams []sqlc.CreateTransactionLineItemParams,
	_ string,
) (sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role := m.members[workspaceID][userID]
	if role != "owner" && role != "editor" {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, ledger.ErrForbidden
	}

	createdTx := sqlc.CashTransaction{
		ID:              txParams.ID,
		WorkspaceID:     txParams.WorkspaceID,
		CreatedBy:       txParams.CreatedBy,
		Title:           txParams.Title,
		Type:            txParams.Type,
		Amount:          txParams.Amount,
		Category:        txParams.Category,
		PaymentMethod:   txParams.PaymentMethod,
		CustomerName:    txParams.CustomerName,
		NotaRef:         txParams.NotaRef,
		HasReceiptPhoto: txParams.HasReceiptPhoto,
		Source:          txParams.Source,
		TransactionAt:   txParams.TransactionAt,
	}
	m.transactions[workspaceID] = append([]sqlc.CashTransaction{createdTx}, m.transactions[workspaceID]...)

	var createdItems []sqlc.TransactionLineItem
	for _, ip := range itemParams {
		li := sqlc.TransactionLineItem{
			ID:            ip.ID,
			TransactionID: ip.TransactionID,
			Name:          ip.Name,
			Quantity:      ip.Quantity,
			UnitPrice:     ip.UnitPrice,
			Subtotal:      ip.Subtotal,
			SortOrder:     ip.SortOrder,
		}
		createdItems = append(createdItems, li)
	}
	m.lineItems[createdTx.ID] = createdItems

	return createdTx, createdItems, m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryLedgerStore) SoftDeleteTransactionTx(
	_ context.Context,
	userID, workspaceID, transactionID uuid.UUID,
	_ string,
) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role := m.members[workspaceID][userID]
	if role != "owner" && role != "editor" {
		return sqlc.User{}, nil, ledger.ErrForbidden
	}
	txs := m.transactions[workspaceID]
	found := false
	for i := range txs {
		if txs[i].ID == transactionID && !txs[i].DeletedAt.Valid {
			txs[i].DeletedAt.Valid = true
			txs[i].DeletedAt.Time = time.Now()
			found = true
			break
		}
	}
	if !found {
		return sqlc.User{}, nil, ledger.ErrNotFound
	}
	m.transactions[workspaceID] = txs
	return m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
}

func TestWorkspacesQuotaCustomAndCashTransactions(t *testing.T) {
	jwtMgr, _ := jwtutil.NewManager("0123456789abcdef0123456789abcdef_strong_key!", 15*time.Minute)
	store := newMemoryLedgerStore()
	svc := ledger.NewService(store)
	h := ledger.NewHandler(svc)

	r := chi.NewRouter()
	r.Use(middleware.RequireAuth(jwtMgr))
	r.Get("/api/v1/workspaces", h.ListWorkspaces)
	r.Post("/api/v1/workspaces", h.CreateWorkspace)
	r.Patch("/api/v1/workspaces/{workspaceID}", h.UpdateWorkspace)
	r.Get("/api/v1/workspaces/{workspaceID}/transactions", h.ListTransactions)
	r.Post("/api/v1/workspaces/{workspaceID}/transactions", h.CreateTransaction)
	r.Delete("/api/v1/workspaces/{workspaceID}/transactions/{transactionID}", h.DeleteTransaction)

	ownerID, _ := uuid.NewV7()
	intruderID, _ := uuid.NewV7()
	ws1ID, _ := uuid.NewV7()
	store.seedUserWithDefaultWorkspace(ownerID, ws1ID, "starter")

	ownerToken, _, _ := jwtMgr.GenerateAccessToken(ownerID)
	intruderToken, _, _ := jwtMgr.GenerateAccessToken(intruderID)

	// 1. Starter user CAN customize their 1 default workspace (Name, FocusType, Bank Info)
	patchBody := `{"name":"Bengkel Motor Budi","short_name":"Bengkel Budi","focus_type":"umkm","bank_name":"BCA","bank_account_number":"8720192811","bank_account_holder":"Budi Santoso"}`
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+ws1ID.String(), bytes.NewBufferString(patchBody))
	patchReq.Header.Set("Authorization", "Bearer "+ownerToken)
	patchRec := httptest.NewRecorder()
	r.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on workspace customize, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// 2. Starter user CANNOT create a 2nd workspace -> 403 WORKSPACE_LIMIT_REACHED
	create2Req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", bytes.NewBufferString(`{"name":"Dapur Rumah","focus_type":"rumah_tangga"}`))
	create2Req.Header.Set("Authorization", "Bearer "+ownerToken)
	create2Rec := httptest.NewRecorder()
	r.ServeHTTP(create2Rec, create2Req)
	if create2Rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 WORKSPACE_LIMIT_REACHED for Starter 2nd workspace, got %d: %s", create2Rec.Code, create2Rec.Body.String())
	}

	// 3. Upgrade user to umkm_pro -> can create 2nd and 3rd workspace, 4th rejected with 403
	store.setPlan(ownerID, "umkm_pro")
	for i := 2; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", bytes.NewBufferString(`{"name":"Kas Tambahan","focus_type":"rumah_tangga"}`))
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201 for Pro workspace #%d, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	create4Req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", bytes.NewBufferString(`{"name":"Kas Ke-4","focus_type":"komunitas"}`))
	create4Req.Header.Set("Authorization", "Bearer "+ownerToken)
	create4Rec := httptest.NewRecorder()
	r.ServeHTTP(create4Rec, create4Req)
	if create4Rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for Pro 4th workspace, got %d", create4Rec.Code)
	}

	// 4. Create manual transaction (0 items) + itemized transaction (2 items)
	manualTxBody := `{"title":"Servis Ganti Oli","type":"inflow","amount":125000,"category":"Jasa / Servis","payment_method":"QRIS","source":"manual","items":[]}`
	manualReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws1ID.String()+"/transactions", bytes.NewBufferString(manualTxBody))
	manualReq.Header.Set("Authorization", "Bearer "+ownerToken)
	manualRec := httptest.NewRecorder()
	r.ServeHTTP(manualRec, manualReq)
	if manualRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on manual transaction, got %d: %s", manualRec.Code, manualRec.Body.String())
	}

	itemizedTxBody := `{"title":"Beli Kampas Rem & Busi","type":"outflow","amount":0,"category":"Bahan Baku","payment_method":"Tunai","source":"receipt_ai","has_receipt_photo":true,"items":[{"name":"Kampas Rem","quantity":2,"unit_price":25000},{"name":"Busi Iridium","quantity":1,"unit_price":35000}]}`
	itemizedReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws1ID.String()+"/transactions", bytes.NewBufferString(itemizedTxBody))
	itemizedReq.Header.Set("Authorization", "Bearer "+ownerToken)
	itemizedRec := httptest.NewRecorder()
	r.ServeHTTP(itemizedRec, itemizedReq)
	if itemizedRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on itemized transaction, got %d: %s", itemizedRec.Code, itemizedRec.Body.String())
	}

	// 5. Anti-IDOR: Intruder cannot read transactions of ws1ID
	idorReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws1ID.String()+"/transactions", nil)
	idorReq.Header.Set("Authorization", "Bearer "+intruderToken)
	idorRec := httptest.NewRecorder()
	r.ServeHTTP(idorRec, idorReq)
	if idorRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 FORBIDDEN on IDOR attempt, got %d", idorRec.Code)
	}

	// 6. Owner fetches transactions -> verify 2 transactions, itemized amount = 85000, net balance = 125000 - 85000 = 40000
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws1ID.String()+"/transactions", nil)
	listReq.Header.Set("Authorization", "Bearer "+ownerToken)
	listRec := httptest.NewRecorder()
	r.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on list transactions, got %d", listRec.Code)
	}

	var listResp struct {
		Data struct {
			Transactions []ledger.TransactionDTO `json:"transactions"`
			Workspaces   []struct {
				ID      string `json:"id"`
				Balance int64  `json:"balance"`
			} `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp: %v", err)
	}
	if len(listResp.Data.Transactions) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(listResp.Data.Transactions))
	}
	if listResp.Data.Transactions[0].Amount != 85000 || len(listResp.Data.Transactions[0].Items) != 2 {
		t.Fatalf("expected itemized transaction amount 85000 with 2 items, got %+v", listResp.Data.Transactions[0])
	}

	var ws1Balance int64
	for _, w := range listResp.Data.Workspaces {
		if w.ID == ws1ID.String() {
			ws1Balance = w.Balance
		}
	}
	if ws1Balance != 40000 {
		t.Fatalf("expected ws1 balance 40000 (125000 - 85000), got %d", ws1Balance)
	}

	// 7. Soft-delete the outflow transaction -> balance goes back to 125000
	delTxID := listResp.Data.Transactions[0].ID
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/"+ws1ID.String()+"/transactions/"+delTxID, nil)
	delReq.Header.Set("Authorization", "Bearer "+ownerToken)
	delRec := httptest.NewRecorder()
	r.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on soft-delete transaction, got %d: %s", delRec.Code, delRec.Body.String())
	}
}
