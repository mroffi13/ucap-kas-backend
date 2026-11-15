package invoice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/modules/invoice"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type memoryInvoiceStore struct {
	mu           sync.Mutex
	users        map[uuid.UUID]sqlc.User
	workspaces   map[uuid.UUID]sqlc.Workspace
	members      map[uuid.UUID]map[uuid.UUID]string
	invoices     map[uuid.UUID][]sqlc.Invoice
	lineItems    map[uuid.UUID][]sqlc.InvoiceLineItem
	transactions map[uuid.UUID][]sqlc.CashTransaction
}

func newMemoryInvoiceStore() *memoryInvoiceStore {
	return &memoryInvoiceStore{
		users:        make(map[uuid.UUID]sqlc.User),
		workspaces:   make(map[uuid.UUID]sqlc.Workspace),
		members:      make(map[uuid.UUID]map[uuid.UUID]string),
		invoices:     make(map[uuid.UUID][]sqlc.Invoice),
		lineItems:    make(map[uuid.UUID][]sqlc.InvoiceLineItem),
		transactions: make(map[uuid.UUID][]sqlc.CashTransaction),
	}
}

func (m *memoryInvoiceStore) seedUserWithWorkspace(userID, wsID uuid.UUID, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[userID] = sqlc.User{
		ID:       userID,
		Email:    "owner@ucapkas.id",
		FullName: "Pemilik Kas",
		PlanTier: "umkm_pro",
	}
	if _, exists := m.workspaces[wsID]; !exists {
		m.workspaces[wsID] = sqlc.Workspace{
			ID:        wsID,
			OwnerID:   userID,
			Name:      "Bengkel Jaya",
			ShortName: "Bengkel Jaya",
			FocusType: "umkm",
			IsDefault: true,
		}
		m.members[wsID] = map[uuid.UUID]string{}
	}
	m.members[wsID][userID] = role
}

func (m *memoryInvoiceStore) buildWorkspaceRowsLocked(userID uuid.UUID) []sqlc.ListWorkspacesByUserIDRow {
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
			ID:             ws.ID,
			OwnerID:        ws.OwnerID,
			Name:           ws.Name,
			ShortName:      ws.ShortName,
			FocusType:      ws.FocusType,
			IsDefault:      ws.IsDefault,
			Role:           role,
			Balance:        balance,
			MonthlyInflow:  inflow,
			MonthlyOutflow: outflow,
		})
	}
	return out
}

func (m *memoryInvoiceStore) ListInvoices(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	status string,
	limit, offset int32,
) ([]sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[workspaceID][userID]; !ok {
		return nil, nil, sqlc.User{}, nil, invoice.ErrForbidden
	}
	var invs []sqlc.Invoice
	var items []sqlc.InvoiceLineItem
	for _, inv := range m.invoices[workspaceID] {
		if inv.DeletedAt.Valid {
			continue
		}
		if status != "" && inv.Status != status {
			continue
		}
		invs = append(invs, inv)
		items = append(items, m.lineItems[inv.ID]...)
	}
	return invs, items, m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryInvoiceStore) CreateInvoiceTx(
	_ context.Context,
	userID, workspaceID uuid.UUID,
	params sqlc.CreateInvoiceParams,
	itemParams []sqlc.CreateInvoiceLineItemParams,
	_ string,
) (sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.members[workspaceID][userID]
	if !ok || (role != "owner" && role != "editor") {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, invoice.ErrForbidden
	}

	num := params.InvoiceNumber
	if num == "" {
		num = fmt.Sprintf("NOTA/%d/%d", params.IssuedAt.Year(), 1101+len(m.invoices[workspaceID]))
	}
	for _, existing := range m.invoices[workspaceID] {
		if !existing.DeletedAt.Valid && existing.InvoiceNumber == num {
			return sqlc.Invoice{}, nil, sqlc.User{}, nil, invoice.ErrConflict
		}
	}
	sub := params.Subtitle
	if sub == "" {
		sub = num
	}

	inv := sqlc.Invoice{
		ID:            params.ID,
		WorkspaceID:   workspaceID,
		CreatedBy:     userID,
		InvoiceNumber: num,
		ClientName:    params.ClientName,
		ClientContact: params.ClientContact,
		Subtitle:      sub,
		DueAt:         params.DueAt,
		Amount:        params.Amount,
		Status:        params.Status,
		Notes:         params.Notes,
		IssuedAt:      params.IssuedAt,
		CreatedAt:     params.IssuedAt,
		UpdatedAt:     params.IssuedAt,
	}
	m.invoices[workspaceID] = append([]sqlc.Invoice{inv}, m.invoices[workspaceID]...)

	createdItems := make([]sqlc.InvoiceLineItem, 0, len(itemParams))
	for _, ip := range itemParams {
		li := sqlc.InvoiceLineItem{
			ID:        ip.ID,
			InvoiceID: ip.InvoiceID,
			Name:      ip.Name,
			Quantity:  ip.Quantity,
			UnitPrice: ip.UnitPrice,
			Subtotal:  ip.Subtotal,
			SortOrder: ip.SortOrder,
		}
		createdItems = append(createdItems, li)
	}
	m.lineItems[inv.ID] = createdItems

	return inv, createdItems, m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
}

func (m *memoryInvoiceStore) MarkInvoicePaidTx(
	_ context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	paymentMethod, _ string,
) (sqlc.Invoice, []sqlc.InvoiceLineItem, *sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.members[workspaceID][userID]
	if !ok || (role != "owner" && role != "editor") {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, invoice.ErrForbidden
	}

	list := m.invoices[workspaceID]
	for i := range list {
		if list[i].ID == invoiceID && !list[i].DeletedAt.Valid {
			if list[i].Status == "lunas" {
				return list[i], m.lineItems[invoiceID], nil, nil, m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
			}
			now := time.Now().UTC()
			txID := uuid.New()
			createdTx := sqlc.CashTransaction{
				ID:            txID,
				WorkspaceID:   workspaceID,
				CreatedBy:     userID,
				Title:         "Pelunasan " + list[i].ClientName,
				Type:          "inflow",
				Amount:        list[i].Amount,
				Category:      "Pelunasan Invoice",
				PaymentMethod: paymentMethod,
				CustomerName:  list[i].ClientName,
				NotaRef:       list[i].InvoiceNumber,
				Source:        "nota_sync",
				TransactionAt: now,
			}
			m.transactions[workspaceID] = append([]sqlc.CashTransaction{createdTx}, m.transactions[workspaceID]...)

			list[i].Status = "lunas"
			list[i].Subtitle = fmt.Sprintf("%s • Lunas (%s)", list[i].InvoiceNumber, paymentMethod)
			list[i].PaidAt = pgtype.Timestamptz{Time: now, Valid: true}
			list[i].PaidTransactionID = uuid.NullUUID{UUID: txID, Valid: true}

			var txItems []sqlc.TransactionLineItem
			for _, it := range m.lineItems[invoiceID] {
				txItems = append(txItems, sqlc.TransactionLineItem{
					ID:            uuid.New(),
					TransactionID: txID,
					Name:          it.Name,
					Quantity:      it.Quantity,
					UnitPrice:     it.UnitPrice,
					Subtotal:      it.Subtotal,
					SortOrder:     it.SortOrder,
				})
			}
			return list[i], m.lineItems[invoiceID], &createdTx, txItems, m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
		}
	}
	return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, invoice.ErrNotFound
}

func (m *memoryInvoiceStore) UpdateInvoiceTx(
	_ context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	input invoice.UpdateInvoiceInput,
	_ string,
) (sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.members[workspaceID][userID]
	if !ok || (role != "owner" && role != "editor") {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, invoice.ErrForbidden
	}
	list := m.invoices[workspaceID]
	for i := range list {
		if list[i].ID == invoiceID && !list[i].DeletedAt.Valid {
			if input.ClientName != "" {
				list[i].ClientName = input.ClientName
			}
			if input.ClientContact != "" {
				list[i].ClientContact = input.ClientContact
			}
			if input.Status != "" {
				list[i].Status = input.Status
			}
			if input.Notes != "" {
				list[i].Notes = input.Notes
			}
			return list[i], m.lineItems[invoiceID], m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
		}
	}
	return sqlc.Invoice{}, nil, sqlc.User{}, nil, invoice.ErrNotFound
}

func (m *memoryInvoiceStore) SoftDeleteInvoiceTx(
	_ context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	_ string,
) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.members[workspaceID][userID]
	if !ok || (role != "owner" && role != "editor") {
		return sqlc.User{}, nil, invoice.ErrForbidden
	}
	list := m.invoices[workspaceID]
	for i := range list {
		if list[i].ID == invoiceID && !list[i].DeletedAt.Valid {
			list[i].DeletedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			return m.users[userID], m.buildWorkspaceRowsLocked(userID), nil
		}
	}
	return sqlc.User{}, nil, invoice.ErrNotFound
}

func setupInvoiceRouter(store *memoryInvoiceStore, jwtMgr *jwtutil.Manager) http.Handler {
	svc := invoice.NewService(store)
	h := invoice.NewHandler(svc)
	r := chi.NewRouter()
	r.Route("/api/v1/workspaces/{workspaceID}/invoices", func(ir chi.Router) {
		ir.Use(middleware.RequireAuth(jwtMgr))
		ir.Get("/", h.ListInvoices)
		ir.Post("/", h.CreateInvoice)
		ir.Get("/{invoiceID}/pdf", h.DownloadPDF)
		ir.Patch("/{invoiceID}", h.UpdateInvoice)
		ir.Post("/{invoiceID}/mark-paid", h.MarkInvoicePaid)
		ir.Delete("/{invoiceID}", h.DeleteInvoice)
	})
	return r
}

func TestInvoiceLifecycleAndSync(t *testing.T) {
	store := newMemoryInvoiceStore()
	userID := uuid.New()
	wsID := uuid.New()
	store.seedUserWithWorkspace(userID, wsID, "owner")

	jwtMgr, err := jwtutil.NewManager("super-secret-jwt-key-minimum-32-chars", 15*time.Minute)
	if err != nil {
		t.Fatalf("jwtutil.NewManager: %v", err)
	}
	token, _, err := jwtMgr.GenerateAccessToken(userID)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	router := setupInvoiceRouter(store, jwtMgr)

	// 1. Create Invoice with line items (auto-computed amount & number)
	body := `{
		"client_name": "Pak Reza — 3x Kursi Kerja",
		"client_contact": "0812-8890-3344",
		"due_date_label": "Tempo: 12 November 2026",
		"amount": 1000,
		"items": [
			{"name": "Kursi Kerja", "quantity": 3, "unit_price": 100000},
			{"name": "Meja", "quantity": 3, "unit_price": 200000}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invoices", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var createResp struct {
		Data struct {
			Invoice invoice.InvoiceDTO `json:"invoice"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal createResp: %v", err)
	}
	if createResp.Data.Invoice.Amount != 900000 {
		t.Fatalf("expected computed amount 900000, got %d", createResp.Data.Invoice.Amount)
	}
	if createResp.Data.Invoice.Status != "menunggu" {
		t.Fatalf("expected status menunggu, got %s", createResp.Data.Invoice.Status)
	}
	if len(createResp.Data.Invoice.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(createResp.Data.Invoice.Items))
	}

	// 2. Duplicate invoice number -> 409 Conflict
	dupBody := fmt.Sprintf(`{"invoice_number": %q, "client_name": "Duplikat", "amount": 50000}`, createResp.Data.Invoice.Number)
	dupReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invoices", bytes.NewBufferString(dupBody))
	dupReq.Header.Set("Authorization", "Bearer "+token)
	dupReq.Header.Set("Content-Type", "application/json")
	dupRec := httptest.NewRecorder()
	router.ServeHTTP(dupRec, dupReq)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for duplicate invoice number, got %d: %s", dupRec.Code, dupRec.Body.String())
	}

	// 3. Mark Invoice Paid -> creates nota_sync transaction & updates workspace balance
	paidReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invoices/"+createResp.Data.Invoice.ID+"/mark-paid", bytes.NewBufferString(`{"payment_method":"QRIS"}`))
	paidReq.Header.Set("Authorization", "Bearer "+token)
	paidReq.Header.Set("Content-Type", "application/json")
	paidRec := httptest.NewRecorder()
	router.ServeHTTP(paidRec, paidReq)

	if paidRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on mark-paid, got %d: %s", paidRec.Code, paidRec.Body.String())
	}
	var paidResp struct {
		Data struct {
			Invoice     invoice.InvoiceDTO `json:"invoice"`
			Transaction *struct {
				Amount int64  `json:"amount"`
				Source string `json:"source"`
				Nota   string `json:"nota_ref"`
			} `json:"transaction"`
			Workspaces []struct {
				Balance int64 `json:"balance"`
			} `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(paidRec.Body.Bytes(), &paidResp); err != nil {
		t.Fatalf("unmarshal paidResp: %v", err)
	}
	if paidResp.Data.Invoice.Status != "lunas" {
		t.Fatalf("expected status lunas, got %s", paidResp.Data.Invoice.Status)
	}
	if paidResp.Data.Transaction == nil || paidResp.Data.Transaction.Amount != 900000 || paidResp.Data.Transaction.Source != "nota_sync" {
		t.Fatalf("expected nota_sync transaction of 900000, got %+v", paidResp.Data.Transaction)
	}
	if len(paidResp.Data.Workspaces) == 0 || paidResp.Data.Workspaces[0].Balance != 900000 {
		t.Fatalf("expected workspace balance 900000, got %+v", paidResp.Data.Workspaces)
	}

	// 4. Idempotent second mark-paid -> 200 OK, transaction == nil, balance stays 900000
	paidReq2 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invoices/"+createResp.Data.Invoice.ID+"/mark-paid", nil)
	paidReq2.Header.Set("Authorization", "Bearer "+token)
	paidRec2 := httptest.NewRecorder()
	router.ServeHTTP(paidRec2, paidReq2)
	if paidRec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on second mark-paid, got %d", paidRec2.Code)
	}
	var paidResp2 struct {
		Data struct {
			Transaction any `json:"transaction"`
			Workspaces  []struct {
				Balance int64 `json:"balance"`
			} `json:"workspaces"`
		} `json:"data"`
	}
	_ = json.Unmarshal(paidRec2.Body.Bytes(), &paidResp2)
	if paidResp2.Data.Transaction != nil {
		t.Fatalf("expected nil transaction on idempotent mark-paid, got %+v", paidResp2.Data.Transaction)
	}
	if paidResp2.Data.Workspaces[0].Balance != 900000 {
		t.Fatalf("expected balance to remain 900000, got %d", paidResp2.Data.Workspaces[0].Balance)
	}

	// 5. List with status=lunas
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/invoices?status=lunas", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on list, got %d", listRec.Code)
	}
	var listResp struct {
		Data struct {
			Invoices []invoice.InvoiceDTO `json:"invoices"`
		} `json:"data"`
	}
	_ = json.Unmarshal(listRec.Body.Bytes(), &listResp)
	if len(listResp.Data.Invoices) != 1 {
		t.Fatalf("expected 1 lunas invoice, got %d", len(listResp.Data.Invoices))
	}

	// 6. Viewer cannot create or delete
	viewerID := uuid.New()
	store.seedUserWithWorkspace(viewerID, wsID, "viewer")
	viewerToken, _, _ := jwtMgr.GenerateAccessToken(viewerID)

	vReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invoices", bytes.NewBufferString(`{"client_name":"Test","amount":10000}`))
	vReq.Header.Set("Authorization", "Bearer "+viewerToken)
	vRec := httptest.NewRecorder()
	router.ServeHTTP(vRec, vReq)
	if vRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for viewer, got %d", vRec.Code)
	}

	// 7. Soft delete invoice
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/"+wsID.String()+"/invoices/"+createResp.Data.Invoice.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on delete, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// 8. Dynamic due date countdown & automatic overdue (jatuh_tempo)
	yesterday := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	overdueBody := fmt.Sprintf(`{"client_name": "Pak Telat", "due_date_label": "Tempo", "due_at": %q, "amount": 50000}`, yesterday)
	ovReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/invoices", bytes.NewBufferString(overdueBody))
	ovReq.Header.Set("Authorization", "Bearer "+token)
	ovReq.Header.Set("Content-Type", "application/json")
	ovRec := httptest.NewRecorder()
	router.ServeHTTP(ovRec, ovReq)
	if ovRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", ovRec.Code, ovRec.Body.String())
	}
	var ovResp struct {
		Data struct {
			Invoice invoice.InvoiceDTO `json:"invoice"`
		} `json:"data"`
	}
	_ = json.Unmarshal(ovRec.Body.Bytes(), &ovResp)
	if ovResp.Data.Invoice.Status != "jatuh_tempo" {
		t.Fatalf("expected automatic status jatuh_tempo for past due_at, got %s", ovResp.Data.Invoice.Status)
	}
	if ovResp.Data.Invoice.DueDateLabel == "" || ovResp.Data.Invoice.DueAt == "" {
		t.Fatalf("expected dynamic due_date_label and due_at, got %+v", ovResp.Data.Invoice)
	}

	// 9. Download Invoice PDF -> 200 OK, application/pdf, valid %PDF-1.4 header
	pdfReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/invoices/"+ovResp.Data.Invoice.ID+"/pdf", nil)
	pdfReq.Header.Set("Authorization", "Bearer "+token)
	pdfRec := httptest.NewRecorder()
	router.ServeHTTP(pdfRec, pdfReq)
	if pdfRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on pdf download, got %d: %s", pdfRec.Code, pdfRec.Body.String())
	}
	if ct := pdfRec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("expected Content-Type application/pdf, got %s", ct)
	}
	if !bytes.HasPrefix(pdfRec.Body.Bytes(), []byte("%PDF-1.4")) {
		t.Fatalf("expected valid PDF header, got prefix %q", pdfRec.Body.Bytes()[:16])
	}
}
