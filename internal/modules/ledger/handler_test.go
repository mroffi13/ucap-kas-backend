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
	"github.com/jackc/pgx/v5/pgtype"
)

type memoryLedgerStore struct {
	mu           sync.Mutex
	users        map[uuid.UUID]sqlc.User
	workspaces   map[uuid.UUID]sqlc.Workspace
	members      map[uuid.UUID]map[uuid.UUID]string // wsID -> userID -> role
	invites      map[string]sqlc.WorkspaceInvite    // token -> invite
	auditLogs    []sqlc.ListWorkspaceAuditLogsWithUserRow
	transactions map[uuid.UUID][]sqlc.CashTransaction
	lineItems    map[uuid.UUID][]sqlc.TransactionLineItem
}

func newMemoryLedgerStore() *memoryLedgerStore {
	return &memoryLedgerStore{
		users:        make(map[uuid.UUID]sqlc.User),
		workspaces:   make(map[uuid.UUID]sqlc.Workspace),
		members:      make(map[uuid.UUID]map[uuid.UUID]string),
		invites:      make(map[string]sqlc.WorkspaceInvite),
		transactions: make(map[uuid.UUID][]sqlc.CashTransaction),
		lineItems:    make(map[uuid.UUID][]sqlc.TransactionLineItem),
	}
}

func (m *memoryLedgerStore) seedUser(userID uuid.UUID, email, fullName, planTier string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[userID] = sqlc.User{
		ID:       userID,
		Email:    email,
		FullName: fullName,
		PlanTier: planTier,
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
			LogoUrl:           ws.LogoUrl,
			QuickCategories:   ws.QuickCategories,
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
	qcBytes, _ := json.Marshal(input.QuickCategories)
	ws := sqlc.Workspace{
		ID:                wsID,
		OwnerID:           userID,
		Name:              input.Name,
		ShortName:         input.ShortName,
		FocusType:         input.FocusType,
		BankName:          input.BankName,
		BankAccountNumber: input.BankAccountNumber,
		BankAccountHolder: input.BankAccountHolder,
		QuickCategories:   qcBytes,
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
	qcBytes, _ := json.Marshal(input.QuickCategories)
	ws.QuickCategories = qcBytes
	if m.users[userID].PlanTier == "umkm_pro" || m.users[userID].PlanTier == "keluarga_pro" {
		ws.LogoUrl = input.LogoURL
	} else {
		ws.LogoUrl = ""
	}
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

func (m *memoryLedgerStore) buildMemberRowsLocked(workspaceID uuid.UUID) []sqlc.ListWorkspaceMembersWithProfileRow {
	var rows []sqlc.ListWorkspaceMembersWithProfileRow
	for uid, role := range m.members[workspaceID] {
		u := m.users[uid]
		rows = append(rows, sqlc.ListWorkspaceMembersWithProfileRow{
			WorkspaceID: workspaceID,
			UserID:      uid,
			Role:        role,
			JoinedAt:    time.Now().UTC(),
			Email:       u.Email,
			FullName:    u.FullName,
			AvatarUrl:   u.AvatarUrl,
		})
	}
	return rows
}

func (m *memoryLedgerStore) ListMembers(
	_ context.Context,
	userID, workspaceID uuid.UUID,
) ([]sqlc.ListWorkspaceMembersWithProfileRow, sqlc.User, *sqlc.WorkspaceInvite, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.members[workspaceID][userID]
	if !ok {
		return nil, sqlc.User{}, nil, "", ledger.ErrForbidden
	}
	ws, ok := m.workspaces[workspaceID]
	if !ok {
		return nil, sqlc.User{}, nil, "", ledger.ErrNotFound
	}
	owner := m.users[ws.OwnerID]
	var activeInv *sqlc.WorkspaceInvite
	if role == "owner" {
		for _, inv := range m.invites {
			if inv.WorkspaceID == workspaceID && !inv.UsedBy.Valid && !inv.RevokedAt.Valid && time.Now().UTC().Before(inv.ExpiresAt) {
				cp := inv
				activeInv = &cp
				break
			}
		}
	}
	return m.buildMemberRowsLocked(workspaceID), owner, activeInv, role, nil
}

func (m *memoryLedgerStore) CreateInviteTx(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	token string,
	expiresAt time.Time,
	ipAddr string,
) (sqlc.WorkspaceInvite, sqlc.Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[workspaceID][userID] != "owner" {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, ledger.ErrForbidden
	}
	ws, ok := m.workspaces[workspaceID]
	if !ok {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, ledger.ErrNotFound
	}
	owner := m.users[ws.OwnerID]
	maxMembers := 2
	if owner.PlanTier == "umkm_pro" || owner.PlanTier == "keluarga_pro" {
		maxMembers = 5
	}
	if len(m.members[workspaceID]) >= maxMembers {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, ledger.ErrMemberLimitReached
	}
	for k, inv := range m.invites {
		if inv.WorkspaceID == workspaceID && !inv.UsedBy.Valid && !inv.RevokedAt.Valid {
			inv.RevokedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			m.invites[k] = inv
		}
	}
	invID, _ := uuid.NewV7()
	created := sqlc.WorkspaceInvite{
		ID:          invID,
		WorkspaceID: workspaceID,
		CreatedBy:   userID,
		Token:       token,
		Role:        "editor",
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now().UTC(),
	}
	m.invites[token] = created
	logID, _ := uuid.NewV7()
	m.auditLogs = append([]sqlc.ListWorkspaceAuditLogsWithUserRow{{
		ID:          logID,
		UserID:      uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID: uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:      "MEMBER_INVITE_CREATED",
		IpAddress:   ipAddr,
		CreatedAt:   time.Now().UTC(),
		ActorName:   owner.FullName,
		ActorEmail:  owner.Email,
	}}, m.auditLogs...)
	return created, ws, nil
}

func (m *memoryLedgerStore) JoinWorkspaceByTokenTx(
	_ context.Context,
	userID uuid.UUID,
	token, ipAddr string,
) (sqlc.Workspace, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[token]
	if !ok || inv.RevokedAt.Valid || inv.UsedBy.Valid || time.Now().UTC().After(inv.ExpiresAt) {
		return sqlc.Workspace{}, sqlc.User{}, nil, ledger.ErrInviteExpiredOrUsed
	}
	if _, exists := m.members[inv.WorkspaceID][userID]; exists {
		return sqlc.Workspace{}, sqlc.User{}, nil, ledger.ErrAlreadyMember
	}
	ws := m.workspaces[inv.WorkspaceID]
	owner := m.users[ws.OwnerID]
	maxMembers := 2
	if owner.PlanTier == "umkm_pro" || owner.PlanTier == "keluarga_pro" {
		maxMembers = 5
	}
	if len(m.members[inv.WorkspaceID]) >= maxMembers {
		return sqlc.Workspace{}, sqlc.User{}, nil, ledger.ErrMemberLimitReached
	}
	m.members[inv.WorkspaceID][userID] = "editor"
	inv.UsedBy = uuid.NullUUID{UUID: userID, Valid: true}
	inv.UsedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	m.invites[token] = inv

	u := m.users[userID]
	logID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{"member_name": u.FullName})
	m.auditLogs = append([]sqlc.ListWorkspaceAuditLogsWithUserRow{{
		ID:           logID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: inv.WorkspaceID, Valid: true},
		Action:       "MEMBER_JOINED",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
		CreatedAt:    time.Now().UTC(),
		ActorName:    u.FullName,
		ActorEmail:   u.Email,
	}}, m.auditLogs...)

	return ws, u, m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryLedgerStore) RemoveMemberTx(
	_ context.Context,
	ownerID, workspaceID, targetUserID uuid.UUID,
	ipAddr string,
) ([]sqlc.ListWorkspaceMembersWithProfileRow, sqlc.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[workspaceID][ownerID] != "owner" {
		return nil, sqlc.User{}, ledger.ErrForbidden
	}
	role, exists := m.members[workspaceID][targetUserID]
	if !exists || role == "owner" {
		return nil, sqlc.User{}, ledger.ErrNotFound
	}
	target := m.users[targetUserID]
	delete(m.members[workspaceID], targetUserID)

	owner := m.users[ownerID]
	logID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{"removed_user_name": target.FullName})
	m.auditLogs = append([]sqlc.ListWorkspaceAuditLogsWithUserRow{{
		ID:           logID,
		UserID:       uuid.NullUUID{UUID: ownerID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "MEMBER_REMOVED",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
		CreatedAt:    time.Now().UTC(),
		ActorName:    owner.FullName,
		ActorEmail:   owner.Email,
	}}, m.auditLogs...)

	return m.buildMemberRowsLocked(workspaceID), owner, nil
}

func (m *memoryLedgerStore) ListActivityLogs(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	limit, offset int32,
) ([]sqlc.ListWorkspaceAuditLogsWithUserRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[workspaceID][userID] != "owner" {
		return nil, ledger.ErrForbidden
	}
	var matching []sqlc.ListWorkspaceAuditLogsWithUserRow
	for _, l := range m.auditLogs {
		if l.WorkspaceID.Valid && l.WorkspaceID.UUID == workspaceID {
			matching = append(matching, l)
		}
	}
	if int(offset) >= len(matching) {
		return nil, nil
	}
	sliced := matching[offset:]
	if int32(len(sliced)) > limit {
		sliced = sliced[:limit]
	}
	return sliced, nil
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

func TestSingleUseInviteLinkMemberLimitAndOwnerActivityLogs(t *testing.T) {
	jwtMgr, _ := jwtutil.NewManager("0123456789abcdef0123456789abcdef_strong_key!", 15*time.Minute)
	store := newMemoryLedgerStore()
	svc := ledger.NewService(store)
	h := ledger.NewHandler(svc)

	r := chi.NewRouter()
	r.Use(middleware.RequireAuth(jwtMgr))
	r.Post("/api/v1/workspaces/join", h.JoinWorkspace)
	r.Get("/api/v1/workspaces/{workspaceID}/members", h.ListMembers)
	r.Post("/api/v1/workspaces/{workspaceID}/invites", h.CreateInvite)
	r.Delete("/api/v1/workspaces/{workspaceID}/members/{memberUserID}", h.RemoveMember)
	r.Get("/api/v1/workspaces/{workspaceID}/activity-logs", h.ListActivityLogs)

	ownerID, _ := uuid.NewV7()
	admin1ID, _ := uuid.NewV7()
	admin2ID, _ := uuid.NewV7()
	wsID, _ := uuid.NewV7()

	store.seedUserWithDefaultWorkspace(ownerID, wsID, "starter")
	store.seedUser(admin1ID, "rian@ucapkas.id", "Rian Pratama", "starter")
	store.seedUser(admin2ID, "siti@ucapkas.id", "Siti Aminah", "starter")

	ownerToken, _, _ := jwtMgr.GenerateAccessToken(ownerID)
	admin1Token, _, _ := jwtMgr.GenerateAccessToken(admin1ID)
	admin2Token, _, _ := jwtMgr.GenerateAccessToken(admin2ID)

	// 1. Owner generates a 24h single-use WA invite link
	invReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invites", nil)
	invReq.Header.Set("Authorization", "Bearer "+ownerToken)
	invRec := httptest.NewRecorder()
	r.ServeHTTP(invRec, invReq)
	if invRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on CreateInvite, got %d: %s", invRec.Code, invRec.Body.String())
	}

	var invResp struct {
		Data struct {
			Invite ledger.WorkspaceInviteDTO `json:"invite"`
		} `json:"data"`
	}
	if err := json.Unmarshal(invRec.Body.Bytes(), &invResp); err != nil {
		t.Fatalf("unmarshal invResp: %v", err)
	}
	if invResp.Data.Invite.Token == "" || invResp.Data.Invite.InviteURL == "" {
		t.Fatalf("expected non-empty invite token and URL, got %+v", invResp.Data.Invite)
	}

	// 2. Admin1 joins using the full WA invite URL -> 200 OK
	joinBody, _ := json.Marshal(map[string]string{"token": invResp.Data.Invite.InviteURL})
	joinReq1 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/join", bytes.NewReader(joinBody))
	joinReq1.Header.Set("Authorization", "Bearer "+admin1Token)
	joinRec1 := httptest.NewRecorder()
	r.ServeHTTP(joinRec1, joinReq1)
	if joinRec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on first join, got %d: %s", joinRec1.Code, joinRec1.Body.String())
	}

	// 3. Admin2 tries to join using the EXACT SAME invite link -> MUST fail with 410 Gone (single-use lock!)
	joinReq2 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/join", bytes.NewReader(joinBody))
	joinReq2.Header.Set("Authorization", "Bearer "+admin2Token)
	joinRec2 := httptest.NewRecorder()
	r.ServeHTTP(joinRec2, joinReq2)
	if joinRec2.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone when reusing single-use invite link, got %d: %s", joinRec2.Code, joinRec2.Body.String())
	}

	// 4. Starter plan member cap is 2 (1 Owner + 1 Admin): Owner tries to create another invite -> 403 MEMBER_LIMIT_REACHED
	invReq2 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invites", nil)
	invReq2.Header.Set("Authorization", "Bearer "+ownerToken)
	invRec2 := httptest.NewRecorder()
	r.ServeHTTP(invRec2, invReq2)
	if invRec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when Starter workspace has 2/2 members, got %d: %s", invRec2.Code, invRec2.Body.String())
	}

	// 5. Activity Logs: Admin1 (editor) CANNOT view Activity Logs -> 403 Forbidden
	adminLogReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/activity-logs", nil)
	adminLogReq.Header.Set("Authorization", "Bearer "+admin1Token)
	adminLogRec := httptest.NewRecorder()
	r.ServeHTTP(adminLogRec, adminLogReq)
	if adminLogRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for Admin viewing activity logs, got %d", adminLogRec.Code)
	}

	// 6. Activity Logs: Owner CAN view Activity Logs -> 200 OK with invite & join events
	ownerLogReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/activity-logs", nil)
	ownerLogReq.Header.Set("Authorization", "Bearer "+ownerToken)
	ownerLogRec := httptest.NewRecorder()
	r.ServeHTTP(ownerLogRec, ownerLogReq)
	if ownerLogRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for Owner viewing activity logs, got %d: %s", ownerLogRec.Code, ownerLogRec.Body.String())
	}
	var logResp struct {
		Data struct {
			Logs []ledger.WorkspaceActivityLogDTO `json:"logs"`
		} `json:"data"`
	}
	_ = json.Unmarshal(ownerLogRec.Body.Bytes(), &logResp)
	if len(logResp.Data.Logs) < 2 {
		t.Fatalf("expected at least 2 activity log entries, got %d", len(logResp.Data.Logs))
	}

	// 7. Owner removes Admin1 -> 200 OK, member count drops to 1
	remReq := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/"+wsID.String()+"/members/"+admin1ID.String(), nil)
	remReq.Header.Set("Authorization", "Bearer "+ownerToken)
	remRec := httptest.NewRecorder()
	r.ServeHTTP(remRec, remReq)
	if remRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on RemoveMember, got %d: %s", remRec.Code, remRec.Body.String())
	}
}

func TestWorkspaceQuickCategoriesLimit3(t *testing.T) {
	jwtMgr, _ := jwtutil.NewManager("0123456789abcdef0123456789abcdef_strong_key!", 15*time.Minute)
	store := newMemoryLedgerStore()
	svc := ledger.NewService(store)
	h := ledger.NewHandler(svc)

	r := chi.NewRouter()
	r.Use(middleware.RequireAuth(jwtMgr))
	r.Patch("/api/v1/workspaces/{workspaceID}", h.UpdateWorkspace)

	userID := uuid.MustParse("01932c00-0000-7000-8000-000000000090")
	wsID := uuid.MustParse("01932c00-0000-7000-8000-000000000099")
	store.seedUserWithDefaultWorkspace(userID, wsID, "umkm_pro")

	token, _, _ := jwtMgr.GenerateAccessToken(userID)

	patchPayload, _ := json.Marshal(map[string]any{
		"name":             "Bengkel Maju",
		"short_name":       "Bengkel Maju",
		"focus_type":       "umkm",
		"quick_categories": []string{"Ganti Oli", "Tune Up", "Suku Cadang", "Kategori Keempat Diabaikan"},
	})
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID.String(), bytes.NewReader(patchPayload))
	patchReq.Header.Set("Authorization", "Bearer "+token)
	patchRec := httptest.NewRecorder()
	r.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on PATCH workspace, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	var resp struct {
		Data struct {
			Workspace struct {
				QuickCategories []string `json:"quick_categories"`
			} `json:"workspace"`
		} `json:"data"`
	}
	if err := json.Unmarshal(patchRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	got := resp.Data.Workspace.QuickCategories
	if len(got) != 3 || got[0] != "Ganti Oli" || got[1] != "Tune Up" || got[2] != "Suku Cadang" {
		t.Fatalf("expected 3 quick categories [Ganti Oli, Tune Up, Suku Cadang], got %v", got)
	}
}
