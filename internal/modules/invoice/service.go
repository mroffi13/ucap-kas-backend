package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/artech/ucapkas_backend/internal/modules/auth"
	"github.com/artech/ucapkas_backend/internal/modules/ledger"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbidden  = errors.New("forbidden: insufficient workspace access")
	ErrValidation = errors.New("invalid invoice payload")
	ErrNotFound   = errors.New("invoice not found")
	ErrConflict   = errors.New("invoice number already exists")
)

var wibZone = time.FixedZone("WIB", 7*3600)

var indonesianMonths = [...]string{
	"", "Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des",
}

type CreateInvoiceInput struct {
	InvoiceNumber string                 `json:"invoice_number"`
	ClientName    string                 `json:"client_name"`
	ClientContact string                 `json:"client_contact"`
	Subtitle      string                 `json:"subtitle"`
	DueDateLabel  string                 `json:"due_date_label"`
	DueAt         string                 `json:"due_at"`
	Amount        int64                  `json:"amount"`
	Status        string                 `json:"status"`
	Notes         string                 `json:"notes"`
	Items         []ledger.LineItemInput `json:"items"`
}

type UpdateInvoiceInput struct {
	ClientName    string `json:"client_name"`
	ClientContact string `json:"client_contact"`
	Subtitle      string `json:"subtitle"`
	DueDateLabel  string `json:"due_date_label"`
	DueAt         string `json:"due_at"`
	Status        string `json:"status"`
	Notes         string `json:"notes"`
}

type MarkPaidInput struct {
	PaymentMethod string `json:"payment_method"`
}

type InvoiceDTO struct {
	ID            string               `json:"id"`
	WorkspaceID   string               `json:"workspace_id"`
	Number        string               `json:"number"`
	ClientName    string               `json:"client_name"`
	ClientContact string               `json:"client_contact"`
	Subtitle      string               `json:"subtitle"`
	DateIssued    string               `json:"date_issued"`
	DueDateLabel  string               `json:"due_date_label"`
	DueAt         string               `json:"due_at,omitempty"`
	Amount        int64                `json:"amount"`
	Status        string               `json:"status"`
	Notes         string               `json:"notes"`
	IssuedAt      string               `json:"issued_at"`
	PaidAt        string               `json:"paid_at,omitempty"`
	Items         []ledger.LineItemDTO `json:"items"`
}

type Store interface {
	ListInvoices(ctx context.Context, userID, workspaceID uuid.UUID, status string, limit, offset int32) ([]sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	CreateInvoiceTx(ctx context.Context, userID, workspaceID uuid.UUID, params sqlc.CreateInvoiceParams, itemParams []sqlc.CreateInvoiceLineItemParams, ipAddr string) (sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	MarkInvoicePaidTx(ctx context.Context, userID, workspaceID, invoiceID uuid.UUID, paymentMethod, ipAddr string) (sqlc.Invoice, []sqlc.InvoiceLineItem, *sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	UpdateInvoiceTx(ctx context.Context, userID, workspaceID, invoiceID uuid.UUID, input UpdateInvoiceInput, ipAddr string) (sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	SoftDeleteInvoiceTx(ctx context.Context, userID, workspaceID, invoiceID uuid.UUID, ipAddr string) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
}

type PgStore struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{
		pool:    pool,
		queries: sqlc.New(pool),
	}
}

func checkMemberRole(ctx context.Context, q *sqlc.Queries, workspaceID, userID uuid.UUID, requireWrite bool) error {
	role, err := q.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	if requireWrite && role != "owner" && role != "editor" {
		return ErrForbidden
	}
	return nil
}

func (s *PgStore) ListInvoices(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	status string,
	limit, offset int32,
) ([]sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	if err := checkMemberRole(ctx, s.queries, workspaceID, userID, false); err != nil {
		return nil, nil, sqlc.User{}, nil, err
	}

	invs, err := s.queries.ListInvoicesByWorkspace(ctx, sqlc.ListInvoicesByWorkspaceParams{
		WorkspaceID: workspaceID,
		Status:      status,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		return nil, nil, sqlc.User{}, nil, err
	}

	items, err := s.queries.ListLineItemsByWorkspaceInvoices(ctx, workspaceID)
	if err != nil {
		return nil, nil, sqlc.User{}, nil, err
	}

	user, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, nil, sqlc.User{}, nil, err
	}
	wsRows, err := s.queries.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return nil, nil, sqlc.User{}, nil, err
	}

	return invs, items, user, wsRows, nil
}

func (s *PgStore) CreateInvoiceTx(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	params sqlc.CreateInvoiceParams,
	itemParams []sqlc.CreateInvoiceLineItemParams,
	ipAddr string,
) (sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	if err := checkMemberRole(ctx, qtx, workspaceID, userID, true); err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	if params.InvoiceNumber == "" {
		count, err := qtx.CountInvoicesByWorkspace(ctx, workspaceID)
		if err != nil {
			return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
		}
		year := params.IssuedAt.In(wibZone).Year()
		params.InvoiceNumber = fmt.Sprintf("NOTA/%d/%d", year, 1101+count)
	}
	if params.Subtitle == "" {
		dynDue, _ := formatDynamicDueLabel(params.DueAt, params.IssuedAt)
		params.Subtitle = fmt.Sprintf("%s • %s", params.InvoiceNumber, dynDue)
	}

	createdInv, err := qtx.CreateInvoice(ctx, params)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return sqlc.Invoice{}, nil, sqlc.User{}, nil, ErrConflict
		}
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	createdItems := make([]sqlc.InvoiceLineItem, 0, len(itemParams))
	for _, ip := range itemParams {
		li, err := qtx.CreateInvoiceLineItem(ctx, ip)
		if err != nil {
			return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
		}
		createdItems = append(createdItems, li)
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"invoice_id":     createdInv.ID.String(),
		"invoice_number": createdInv.InvoiceNumber,
		"amount":         createdInv.Amount,
		"items_count":    len(createdItems),
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "INVOICE_CREATE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}
	wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}
	return createdInv, createdItems, user, wsRows, nil
}

func (s *PgStore) MarkInvoicePaidTx(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	paymentMethod, ipAddr string,
) (sqlc.Invoice, []sqlc.InvoiceLineItem, *sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	if err := checkMemberRole(ctx, qtx, workspaceID, userID, true); err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	inv, err := qtx.GetInvoiceByIDForUpdate(ctx, sqlc.GetInvoiceByIDForUpdateParams{
		ID:          invoiceID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, ErrNotFound
		}
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	invItems, err := qtx.ListLineItemsByInvoiceID(ctx, inv.ID)
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	// Idempotent if already paid
	if inv.Status == "lunas" {
		user, err := qtx.GetUserByID(ctx, userID)
		if err != nil {
			return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
		}
		wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
		if err != nil {
			return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
		}
		return inv, invItems, nil, nil, user, wsRows, nil
	}

	txID, err := uuid.NewV7()
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	now := time.Now().UTC()
	createdTx, err := qtx.CreateCashTransaction(ctx, sqlc.CreateCashTransactionParams{
		ID:              txID,
		WorkspaceID:     workspaceID,
		CreatedBy:       userID,
		Title:           fmt.Sprintf("Pelunasan %s", inv.ClientName),
		Type:            "inflow",
		Amount:          inv.Amount,
		Category:        "Pelunasan Invoice",
		PaymentMethod:   paymentMethod,
		CustomerName:    inv.ClientName,
		NotaRef:         inv.InvoiceNumber,
		HasReceiptPhoto: false,
		Source:          "nota_sync",
		TransactionAt:   now,
	})
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	txItems := make([]sqlc.TransactionLineItem, 0, len(invItems))
	for _, item := range invItems {
		liID, err := uuid.NewV7()
		if err != nil {
			return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
		}
		tli, err := qtx.CreateTransactionLineItem(ctx, sqlc.CreateTransactionLineItemParams{
			ID:            liID,
			TransactionID: txID,
			Name:          item.Name,
			Quantity:      item.Quantity,
			UnitPrice:     item.UnitPrice,
			Subtotal:      item.Subtotal,
			SortOrder:     item.SortOrder,
		})
		if err != nil {
			return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
		}
		txItems = append(txItems, tli)
	}

	paidSubtitle := fmt.Sprintf("%s • Lunas (%s)", inv.InvoiceNumber, paymentMethod)
	updatedInv, err := qtx.MarkInvoicePaid(ctx, sqlc.MarkInvoicePaidParams{
		ID:                inv.ID,
		WorkspaceID:       workspaceID,
		Subtitle:          paidSubtitle,
		PaidTransactionID: uuid.NullUUID{UUID: txID, Valid: true},
	})
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"invoice_id":          updatedInv.ID.String(),
		"invoice_number":      updatedInv.InvoiceNumber,
		"paid_transaction_id": txID.String(),
		"payment_method":      paymentMethod,
		"amount":              updatedInv.Amount,
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "INVOICE_MARK_PAID",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}
	wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.Invoice{}, nil, nil, nil, sqlc.User{}, nil, err
	}
	return updatedInv, invItems, &createdTx, txItems, user, wsRows, nil
}

func (s *PgStore) UpdateInvoiceTx(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	input UpdateInvoiceInput,
	ipAddr string,
) (sqlc.Invoice, []sqlc.InvoiceLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	if err := checkMemberRole(ctx, qtx, workspaceID, userID, true); err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	existing, err := qtx.GetInvoiceByIDForUpdate(ctx, sqlc.GetInvoiceByIDForUpdateParams{
		ID:          invoiceID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Invoice{}, nil, sqlc.User{}, nil, ErrNotFound
		}
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	clientName := strings.TrimSpace(input.ClientName)
	if clientName == "" {
		clientName = existing.ClientName
	}
	clientContact := strings.TrimSpace(input.ClientContact)
	if input.ClientContact == "" {
		clientContact = existing.ClientContact
	}
	dueDateLabel := strings.TrimSpace(input.DueDateLabel)
	subtitle := strings.TrimSpace(input.Subtitle)
	if subtitle == "" {
		subtitle = existing.Subtitle
	}
	status := existing.Status
	if input.Status != "" {
		status = input.Status
	}
	notes := strings.TrimSpace(input.Notes)
	if input.Notes == "" {
		notes = existing.Notes
	}
	dueAt := existing.DueAt
	if strings.TrimSpace(input.DueAt) != "" || dueDateLabel != "" {
		dueAt = resolveDueAt(input.DueAt, dueDateLabel, time.Now())
	}

	updated, err := qtx.UpdateInvoice(ctx, sqlc.UpdateInvoiceParams{
		ID:            invoiceID,
		WorkspaceID:   workspaceID,
		ClientName:    clientName,
		ClientContact: clientContact,
		Subtitle:      subtitle,
		DueAt:         dueAt,
		Status:        status,
		Notes:         notes,
	})
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	items, err := qtx.ListLineItemsByInvoiceID(ctx, invoiceID)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]string{
		"invoice_id":     updated.ID.String(),
		"invoice_number": updated.InvoiceNumber,
		"status":         updated.Status,
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "INVOICE_UPDATE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}
	wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.Invoice{}, nil, sqlc.User{}, nil, err
	}
	return updated, items, user, wsRows, nil
}

func (s *PgStore) SoftDeleteInvoiceTx(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	ipAddr string,
) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	if err := checkMemberRole(ctx, qtx, workspaceID, userID, true); err != nil {
		return sqlc.User{}, nil, err
	}

	_, err = qtx.SoftDeleteInvoice(ctx, sqlc.SoftDeleteInvoiceParams{
		ID:          invoiceID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, nil, ErrNotFound
		}
		return sqlc.User{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]string{"invoice_id": invoiceID.String()})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "INVOICE_DELETE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.User{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.User{}, nil, err
	}
	return user, wsRows, nil
}

// Service Layer

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func formatIssuedDateWIB(t time.Time) string {
	wib := t.In(wibZone)
	return fmt.Sprintf("%d %s %d", wib.Day(), indonesianMonths[int(wib.Month())], wib.Year())
}

func sanitizeStatus(raw string, allowEmpty bool) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" && allowEmpty {
		return "", nil
	}
	switch s {
	case "", "menunggu":
		return "menunggu", nil
	case "jatuh_tempo":
		return "jatuh_tempo", nil
	case "lunas":
		return "lunas", nil
	default:
		return "", fmt.Errorf("%w: status invoice harus 'menunggu', 'jatuh_tempo', atau 'lunas'", ErrValidation)
	}
}

var relativeDaysRe = regexp.MustCompile(`(?i)(\d+)\s*hari`)

func endOfDayWIB(t time.Time) time.Time {
	w := t.In(wibZone)
	return time.Date(w.Year(), w.Month(), w.Day(), 23, 59, 59, 0, wibZone).UTC()
}

func resolveDueAt(rawDueAt, dueDateLabel string, now time.Time) time.Time {
	s := strings.TrimSpace(rawDueAt)
	if s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return endOfDayWIB(t)
		}
		if t, err := time.ParseInLocation("2006-01-02", s, wibZone); err == nil {
			return endOfDayWIB(t)
		}
	}

	nowWIB := now.In(wibZone)
	label := strings.ToLower(strings.TrimSpace(dueDateLabel))
	if strings.Contains(label, "besok") {
		return endOfDayWIB(nowWIB.AddDate(0, 0, 1))
	}
	if strings.Contains(label, "hari ini") {
		return endOfDayWIB(nowWIB)
	}
	if m := relativeDaysRe.FindStringSubmatch(label); len(m) == 2 {
		if days, err := strconv.Atoi(m[1]); err == nil && days >= 0 && days <= 3650 {
			return endOfDayWIB(nowWIB.AddDate(0, 0, days))
		}
	}
	return endOfDayWIB(nowWIB.AddDate(0, 0, 7))
}

func formatDynamicDueLabel(dueAt time.Time, now time.Time) (string, bool) {
	dueWIB := dueAt.In(wibZone)
	nowWIB := now.In(wibZone)

	dueDay := time.Date(dueWIB.Year(), dueWIB.Month(), dueWIB.Day(), 0, 0, 0, 0, wibZone)
	today := time.Date(nowWIB.Year(), nowWIB.Month(), nowWIB.Day(), 0, 0, 0, 0, wibZone)
	diffDays := int(dueDay.Sub(today).Hours() / 24)
	dateStr := formatIssuedDateWIB(dueAt)

	switch {
	case diffDays > 1:
		return fmt.Sprintf("Tempo: %s (%d hari lagi)", dateStr, diffDays), false
	case diffDays == 1:
		return fmt.Sprintf("Tempo: Besok (%s)", dateStr), false
	case diffDays == 0:
		return "Tempo: Hari Ini", false
	default:
		return fmt.Sprintf("Lewat %d Hari (%s)", -diffDays, dateStr), true
	}
}

func toInvoiceDTO(inv sqlc.Invoice, items []sqlc.InvoiceLineItem, now time.Time) InvoiceDTO {
	itemDTOs := make([]ledger.LineItemDTO, 0, len(items))
	for _, it := range items {
		itemDTOs = append(itemDTOs, ledger.LineItemDTO{
			ID:        it.ID.String(),
			Name:      it.Name,
			Quantity:  it.Quantity,
			UnitPrice: it.UnitPrice,
			Subtotal:  it.Subtotal,
		})
	}
	paidAt := ""
	if inv.PaidAt.Valid {
		paidAt = inv.PaidAt.Time.UTC().Format(time.RFC3339)
	}
	dueAt := inv.DueAt.UTC().Format(time.RFC3339)
	var dueDateLabel string
	subtitle := inv.Subtitle
	status := inv.Status
	if status != "lunas" {
		dynLabel, isOverdue := formatDynamicDueLabel(inv.DueAt, now)
		dueDateLabel = dynLabel
		subtitle = fmt.Sprintf("%s • %s", inv.InvoiceNumber, dynLabel)
		if isOverdue {
			status = "jatuh_tempo"
		}
	} else {
		dueDateLabel = fmt.Sprintf("Tempo: %s", formatIssuedDateWIB(inv.DueAt))
	}
	return InvoiceDTO{
		ID:            inv.ID.String(),
		WorkspaceID:   inv.WorkspaceID.String(),
		Number:        inv.InvoiceNumber,
		ClientName:    inv.ClientName,
		ClientContact: inv.ClientContact,
		Subtitle:      subtitle,
		DateIssued:    formatIssuedDateWIB(inv.IssuedAt),
		DueDateLabel:  dueDateLabel,
		DueAt:         dueAt,
		Amount:        inv.Amount,
		Status:        status,
		Notes:         inv.Notes,
		IssuedAt:      inv.IssuedAt.UTC().Format(time.RFC3339),
		PaidAt:        paidAt,
		Items:         itemDTOs,
	}
}

func (s *Service) ListInvoices(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	rawStatus string,
	limit, offset int32,
) ([]InvoiceDTO, []auth.WorkspaceDTO, error) {
	status, err := sanitizeStatus(rawStatus, true)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	sqlStatus := status
	if status == "menunggu" || status == "jatuh_tempo" {
		sqlStatus = ""
	}

	invs, allItems, user, wsRows, err := s.store.ListInvoices(ctx, userID, workspaceID, sqlStatus, limit, offset)
	if err != nil {
		return nil, nil, err
	}

	itemsByInv := make(map[uuid.UUID][]sqlc.InvoiceLineItem)
	for _, it := range allItems {
		itemsByInv[it.InvoiceID] = append(itemsByInv[it.InvoiceID], it)
	}

	now := time.Now()
	out := make([]InvoiceDTO, 0, len(invs))
	for _, inv := range invs {
		dto := toInvoiceDTO(inv, itemsByInv[inv.ID], now)
		if status != "" && dto.Status != status {
			continue
		}
		out = append(out, dto)
	}
	return out, ledger.MapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) CreateInvoice(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	input CreateInvoiceInput,
	ipAddr string,
) (*InvoiceDTO, []auth.WorkspaceDTO, error) {
	clientName := strings.TrimSpace(input.ClientName)
	if clientName == "" || len([]rune(clientName)) > 160 {
		return nil, nil, fmt.Errorf("%w: nama pelanggan wajib diisi (maks 160 karakter)", ErrValidation)
	}

	status, err := sanitizeStatus(input.Status, false)
	if err != nil {
		return nil, nil, err
	}
	if status == "lunas" {
		status = "menunggu"
	}

	invID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, err
	}

	itemParams := make([]sqlc.CreateInvoiceLineItemParams, 0, len(input.Items))
	var computedSum int64
	for idx, it := range input.Items {
		itemName := strings.TrimSpace(it.Name)
		if itemName == "" {
			return nil, nil, fmt.Errorf("%w: nama item invoice wajib diisi", ErrValidation)
		}
		if it.Quantity <= 0 {
			return nil, nil, fmt.Errorf("%w: jumlah item harus lebih dari 0", ErrValidation)
		}
		if it.UnitPrice < 0 {
			return nil, nil, fmt.Errorf("%w: harga satuan item tidak boleh negatif", ErrValidation)
		}
		itemID, err := uuid.NewV7()
		if err != nil {
			return nil, nil, err
		}
		subtotal := int64(it.Quantity) * it.UnitPrice
		computedSum += subtotal
		itemParams = append(itemParams, sqlc.CreateInvoiceLineItemParams{
			ID:        itemID,
			InvoiceID: invID,
			Name:      itemName,
			Quantity:  it.Quantity,
			UnitPrice: it.UnitPrice,
			Subtotal:  subtotal,
			SortOrder: int32(idx),
		})
	}

	finalAmount := input.Amount
	if len(itemParams) > 0 && computedSum > 0 {
		finalAmount = computedSum
	}
	if finalAmount <= 0 {
		return nil, nil, fmt.Errorf("%w: nominal tagihan harus lebih dari Rp 0", ErrValidation)
	}

	dueDateLabel := strings.TrimSpace(input.DueDateLabel)
	clientContact := strings.TrimSpace(input.ClientContact)
	if clientContact == "" {
		clientContact = "WA Pelanggan"
	}

	now := time.Now().UTC()
	dueAt := resolveDueAt(input.DueAt, dueDateLabel, now)
	params := sqlc.CreateInvoiceParams{
		ID:            invID,
		WorkspaceID:   workspaceID,
		CreatedBy:     userID,
		InvoiceNumber: strings.TrimSpace(input.InvoiceNumber),
		ClientName:    clientName,
		ClientContact: clientContact,
		Subtitle:      strings.TrimSpace(input.Subtitle),
		DueAt:         dueAt,
		Amount:        finalAmount,
		Status:        status,
		Notes:         strings.TrimSpace(input.Notes),
		IssuedAt:      now,
	}

	createdInv, createdItems, user, wsRows, err := s.store.CreateInvoiceTx(
		ctx,
		userID,
		workspaceID,
		params,
		itemParams,
		ipAddr,
	)
	if err != nil {
		return nil, nil, err
	}

	dto := toInvoiceDTO(createdInv, createdItems, now)
	return &dto, ledger.MapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) MarkInvoicePaid(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	input MarkPaidInput,
	ipAddr string,
) (*InvoiceDTO, *ledger.TransactionDTO, []auth.WorkspaceDTO, error) {
	paymentMethod := strings.TrimSpace(input.PaymentMethod)
	if paymentMethod == "" {
		paymentMethod = "Transfer"
	}

	updatedInv, invItems, createdTx, txItems, user, wsRows, err := s.store.MarkInvoicePaidTx(
		ctx,
		userID,
		workspaceID,
		invoiceID,
		paymentMethod,
		ipAddr,
	)
	if err != nil {
		return nil, nil, nil, err
	}

	invDTO := toInvoiceDTO(updatedInv, invItems, time.Now())
	var txDTO *ledger.TransactionDTO
	if createdTx != nil {
		mapped := ledger.ToTransactionDTO(*createdTx, txItems, time.Now())
		txDTO = &mapped
	}

	return &invDTO, txDTO, ledger.MapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) UpdateInvoice(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	input UpdateInvoiceInput,
	ipAddr string,
) (*InvoiceDTO, []auth.WorkspaceDTO, error) {
	if input.Status != "" {
		status, err := sanitizeStatus(input.Status, false)
		if err != nil {
			return nil, nil, err
		}
		if status == "lunas" {
			return nil, nil, fmt.Errorf("%w: gunakan endpoint mark-paid untuk melunasi invoice", ErrValidation)
		}
		input.Status = status
	}

	updatedInv, items, user, wsRows, err := s.store.UpdateInvoiceTx(ctx, userID, workspaceID, invoiceID, input, ipAddr)
	if err != nil {
		return nil, nil, err
	}

	dto := toInvoiceDTO(updatedInv, items, time.Now())
	return &dto, ledger.MapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) DeleteInvoice(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
	ipAddr string,
) ([]auth.WorkspaceDTO, error) {
	user, wsRows, err := s.store.SoftDeleteInvoiceTx(ctx, userID, workspaceID, invoiceID, ipAddr)
	if err != nil {
		return nil, err
	}
	return ledger.MapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) GenerateInvoicePDF(
	ctx context.Context,
	userID, workspaceID, invoiceID uuid.UUID,
) ([]byte, string, error) {
	invoices, workspaces, err := s.ListInvoices(ctx, userID, workspaceID, "", 500, 0)
	if err != nil {
		return nil, "", err
	}

	var targetInv *InvoiceDTO
	targetID := invoiceID.String()
	for i := range invoices {
		if invoices[i].ID == targetID {
			targetInv = &invoices[i]
			break
		}
	}
	if targetInv == nil {
		return nil, "", ErrNotFound
	}

	var targetWs auth.WorkspaceDTO
	wsIDStr := workspaceID.String()
	for _, ws := range workspaces {
		if ws.ID == wsIDStr {
			targetWs = ws
			break
		}
	}

	pdfBytes, filename := RenderInvoicePDF(*targetInv, targetWs)
	return pdfBytes, filename, nil
}
