package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/artech/ucapkas_backend/internal/modules/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbidden             = errors.New("forbidden: insufficient workspace access")
	ErrWorkspaceLimitReached = errors.New("workspace limit reached for current plan")
	ErrValidation            = errors.New("invalid request payload")
	ErrNotFound              = errors.New("resource not found")
)

var wibZone = time.FixedZone("WIB", 7*3600)

type CreateWorkspaceInput struct {
	Name              string `json:"name"`
	ShortName         string `json:"short_name"`
	FocusType         string `json:"focus_type"`
	BankName          string `json:"bank_name"`
	BankAccountNumber string `json:"bank_account_number"`
	BankAccountHolder string `json:"bank_account_holder"`
}

type UpdateWorkspaceInput struct {
	Name              string `json:"name"`
	ShortName         string `json:"short_name"`
	FocusType         string `json:"focus_type"`
	BankName          string `json:"bank_name"`
	BankAccountNumber string `json:"bank_account_number"`
	BankAccountHolder string `json:"bank_account_holder"`
}

type LineItemInput struct {
	Name      string `json:"name"`
	Quantity  int32  `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
}

type CreateTransactionInput struct {
	Title           string          `json:"title"`
	Type            string          `json:"type"`
	Amount          int64           `json:"amount"`
	Category        string          `json:"category"`
	PaymentMethod   string          `json:"payment_method"`
	CustomerName    string          `json:"customer_name"`
	NotaRef         string          `json:"nota_ref"`
	HasReceiptPhoto bool            `json:"has_receipt_photo"`
	Source          string          `json:"source"`
	Items           []LineItemInput `json:"items"`
}

type LineItemDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Quantity  int32  `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
	Subtotal  int64  `json:"subtotal"`
}

type TransactionDTO struct {
	ID              string        `json:"id"`
	WorkspaceID     string        `json:"workspace_id"`
	Title           string        `json:"title"`
	Type            string        `json:"type"`
	Amount          int64         `json:"amount"`
	Category        string        `json:"category"`
	PaymentMethod   string        `json:"payment_method"`
	CustomerName    string        `json:"customer_name"`
	NotaRef         string        `json:"nota_ref"`
	HasReceiptPhoto bool          `json:"has_receipt_photo"`
	Source          string        `json:"source"`
	TimeLabel       string        `json:"time_label"`
	DateGroup       string        `json:"date_group"`
	TransactionAt   string        `json:"transaction_at"`
	Items           []LineItemDTO `json:"items"`
}

type Store interface {
	ListWorkspaces(ctx context.Context, userID uuid.UUID) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	CreateWorkspaceTx(ctx context.Context, userID uuid.UUID, input CreateWorkspaceInput, ipAddr string) (sqlc.User, sqlc.Workspace, []sqlc.ListWorkspacesByUserIDRow, error)
	UpdateWorkspaceTx(ctx context.Context, userID, workspaceID uuid.UUID, input UpdateWorkspaceInput, ipAddr string) (sqlc.User, sqlc.Workspace, []sqlc.ListWorkspacesByUserIDRow, error)
	ListTransactions(ctx context.Context, userID, workspaceID uuid.UUID, limit, offset int32) ([]sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	CreateTransactionTx(ctx context.Context, userID, workspaceID uuid.UUID, txParams sqlc.CreateCashTransactionParams, itemParams []sqlc.CreateTransactionLineItemParams, ipAddr string) (sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	SoftDeleteTransactionTx(ctx context.Context, userID, workspaceID, transactionID uuid.UUID, ipAddr string) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
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

func (s *PgStore) ListWorkspaces(ctx context.Context, userID uuid.UUID) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	user, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	rows, err := s.queries.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	return user, rows, nil
}

func maxWorkspacesForPlan(planTier string) int32 {
	if planTier == "keluarga_pro" || planTier == "umkm_pro" {
		return 3
	}
	return 1
}

func (s *PgStore) CreateWorkspaceTx(
	ctx context.Context,
	userID uuid.UUID,
	input CreateWorkspaceInput,
	ipAddr string,
) (sqlc.User, sqlc.Workspace, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	ownedCount, err := qtx.CountOwnedWorkspaces(ctx, userID)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	if ownedCount >= maxWorkspacesForPlan(user.PlanTier) {
		return user, sqlc.Workspace{}, nil, ErrWorkspaceLimitReached
	}

	wsID, err := uuid.NewV7()
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	ws, err := qtx.CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
		ID:        wsID,
		OwnerID:   userID,
		Name:      input.Name,
		ShortName: input.ShortName,
		FocusType: input.FocusType,
		IsDefault: ownedCount == 0,
	})
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	if input.BankName != "" || input.BankAccountNumber != "" || input.BankAccountHolder != "" {
		ws, err = qtx.UpdateWorkspaceCustom(ctx, sqlc.UpdateWorkspaceCustomParams{
			ID:                ws.ID,
			Name:              input.Name,
			ShortName:         input.ShortName,
			FocusType:         input.FocusType,
			BankName:          input.BankName,
			BankAccountNumber: input.BankAccountNumber,
			BankAccountHolder: input.BankAccountHolder,
		})
		if err != nil {
			return sqlc.User{}, sqlc.Workspace{}, nil, err
		}
	}

	if err := qtx.AddWorkspaceMember(ctx, sqlc.AddWorkspaceMemberParams{
		WorkspaceID: ws.ID,
		UserID:      userID,
		Role:        "owner",
	}); err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]string{"name": ws.Name, "focus_type": ws.FocusType})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: ws.ID, Valid: true},
		Action:       "WORKSPACE_CREATE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	rows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}
	return user, ws, rows, nil
}

func (s *PgStore) UpdateWorkspaceTx(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	input UpdateWorkspaceInput,
	ipAddr string,
) (sqlc.User, sqlc.Workspace, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	role, err := qtx.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, sqlc.Workspace{}, nil, ErrForbidden
		}
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}
	if role != "owner" {
		return sqlc.User{}, sqlc.Workspace{}, nil, ErrForbidden
	}

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	ws, err := qtx.UpdateWorkspaceCustom(ctx, sqlc.UpdateWorkspaceCustomParams{
		ID:                workspaceID,
		Name:              input.Name,
		ShortName:         input.ShortName,
		FocusType:         input.FocusType,
		BankName:          input.BankName,
		BankAccountNumber: input.BankAccountNumber,
		BankAccountHolder: input.BankAccountHolder,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, sqlc.Workspace{}, nil, ErrNotFound
		}
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]string{"name": ws.Name, "focus_type": ws.FocusType})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: ws.ID, Valid: true},
		Action:       "WORKSPACE_UPDATE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	rows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
	}
	return user, ws, rows, nil
}

func (s *PgStore) ListTransactions(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	limit, offset int32,
) ([]sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	_, err := s.queries.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, sqlc.User{}, nil, ErrForbidden
		}
		return nil, nil, sqlc.User{}, nil, err
	}

	txs, err := s.queries.ListCashTransactionsByWorkspace(ctx, sqlc.ListCashTransactionsByWorkspaceParams{
		WorkspaceID: workspaceID,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		return nil, nil, sqlc.User{}, nil, err
	}

	items, err := s.queries.ListLineItemsByWorkspaceTransactions(ctx, workspaceID)
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

	return txs, items, user, wsRows, nil
}

func (s *PgStore) CreateTransactionTx(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	txParams sqlc.CreateCashTransactionParams,
	itemParams []sqlc.CreateTransactionLineItemParams,
	ipAddr string,
) (sqlc.CashTransaction, []sqlc.TransactionLineItem, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	role, err := qtx.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, ErrForbidden
		}
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
	}
	if role != "owner" && role != "editor" {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, ErrForbidden
	}

	createdTx, err := qtx.CreateCashTransaction(ctx, txParams)
	if err != nil {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
	}

	createdItems := make([]sqlc.TransactionLineItem, 0, len(itemParams))
	for _, ip := range itemParams {
		li, err := qtx.CreateTransactionLineItem(ctx, ip)
		if err != nil {
			return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
		}
		createdItems = append(createdItems, li)
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"transaction_id": createdTx.ID.String(),
		"type":           createdTx.Type,
		"amount":         createdTx.Amount,
		"items_count":    len(createdItems),
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "TRANSACTION_CREATE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
	}
	wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.CashTransaction{}, nil, sqlc.User{}, nil, err
	}
	return createdTx, createdItems, user, wsRows, nil
}

func (s *PgStore) SoftDeleteTransactionTx(
	ctx context.Context,
	userID, workspaceID, transactionID uuid.UUID,
	ipAddr string,
) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	role, err := qtx.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, nil, ErrForbidden
		}
		return sqlc.User{}, nil, err
	}
	if role != "owner" && role != "editor" {
		return sqlc.User{}, nil, ErrForbidden
	}

	_, err = qtx.SoftDeleteCashTransaction(ctx, sqlc.SoftDeleteCashTransactionParams{
		ID:          transactionID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, nil, ErrNotFound
		}
		return sqlc.User{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]string{"transaction_id": transactionID.String()})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "TRANSACTION_DELETE",
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

func sanitizeFocus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "rumah_tangga":
		return "rumah_tangga"
	case "komunitas":
		return "komunitas"
	default:
		return "umkm"
	}
}

func deriveShortName(name, shortName string) string {
	shortName = strings.TrimSpace(shortName)
	if shortName != "" {
		if len([]rune(shortName)) > 20 {
			return string([]rune(shortName)[:20])
		}
		return shortName
	}
	words := strings.Fields(name)
	if len(words) >= 2 {
		candidate := words[0] + " " + words[1]
		if len([]rune(candidate)) <= 20 {
			return candidate
		}
	}
	runes := []rune(name)
	if len(runes) > 16 {
		return string(runes[:16])
	}
	return name
}

func mapWorkspaces(rows []sqlc.ListWorkspacesByUserIDRow, planTier string) []auth.WorkspaceDTO {
	isPro := planTier == "umkm_pro" || planTier == "keluarga_pro"
	out := make([]auth.WorkspaceDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, auth.WorkspaceDTO{
			ID:                r.ID.String(),
			Name:              r.Name,
			ShortName:         r.ShortName,
			FocusType:         r.FocusType,
			BankName:          r.BankName,
			BankAccountNumber: r.BankAccountNumber,
			BankAccountHolder: r.BankAccountHolder,
			Role:              r.Role,
			Balance:           r.Balance,
			MonthlyInflow:     r.MonthlyInflow,
			MonthlyOutflow:    r.MonthlyOutflow,
			IsPro:             isPro,
		})
	}
	return out
}

func (s *Service) ListWorkspaces(ctx context.Context, userID uuid.UUID) ([]auth.WorkspaceDTO, error) {
	user, rows, err := s.store.ListWorkspaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	return mapWorkspaces(rows, user.PlanTier), nil
}

func (s *Service) CreateWorkspace(
	ctx context.Context,
	userID uuid.UUID,
	input CreateWorkspaceInput,
	ipAddr string,
) (*auth.WorkspaceDTO, []auth.WorkspaceDTO, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 80 {
		return nil, nil, fmt.Errorf("%w: nama Buku Kas wajib diisi (maks 80 karakter)", ErrValidation)
	}
	cleanInput := CreateWorkspaceInput{
		Name:              name,
		ShortName:         deriveShortName(name, input.ShortName),
		FocusType:         sanitizeFocus(input.FocusType),
		BankName:          strings.TrimSpace(input.BankName),
		BankAccountNumber: strings.TrimSpace(input.BankAccountNumber),
		BankAccountHolder: strings.TrimSpace(input.BankAccountHolder),
	}

	user, createdWs, rows, err := s.store.CreateWorkspaceTx(ctx, userID, cleanInput, ipAddr)
	if err != nil {
		return nil, nil, err
	}

	dtos := mapWorkspaces(rows, user.PlanTier)
	var createdDTO *auth.WorkspaceDTO
	for i := range dtos {
		if dtos[i].ID == createdWs.ID.String() {
			createdDTO = &dtos[i]
			break
		}
	}
	return createdDTO, dtos, nil
}

func (s *Service) UpdateWorkspace(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	input UpdateWorkspaceInput,
	ipAddr string,
) (*auth.WorkspaceDTO, []auth.WorkspaceDTO, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 80 {
		return nil, nil, fmt.Errorf("%w: nama Buku Kas wajib diisi (maks 80 karakter)", ErrValidation)
	}
	cleanInput := UpdateWorkspaceInput{
		Name:              name,
		ShortName:         deriveShortName(name, input.ShortName),
		FocusType:         sanitizeFocus(input.FocusType),
		BankName:          strings.TrimSpace(input.BankName),
		BankAccountNumber: strings.TrimSpace(input.BankAccountNumber),
		BankAccountHolder: strings.TrimSpace(input.BankAccountHolder),
	}

	user, updatedWs, rows, err := s.store.UpdateWorkspaceTx(ctx, userID, workspaceID, cleanInput, ipAddr)
	if err != nil {
		return nil, nil, err
	}

	dtos := mapWorkspaces(rows, user.PlanTier)
	var updatedDTO *auth.WorkspaceDTO
	for i := range dtos {
		if dtos[i].ID == updatedWs.ID.String() {
			updatedDTO = &dtos[i]
			break
		}
	}
	return updatedDTO, dtos, nil
}

var indonesianMonths = [...]string{
	"", "Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des",
}

func formatWIBLabels(t time.Time, now time.Time) (timeLabel, dateGroup string) {
	wibTime := t.In(wibZone)
	wibNow := now.In(wibZone)
	timeLabel = wibTime.Format("15:04")

	y1, m1, d1 := wibTime.Date()
	yNow, mNow, dNow := wibNow.Date()
	if y1 == yNow && m1 == mNow && d1 == dNow {
		return timeLabel, "Hari Ini"
	}
	yYest, mYest, dYest := wibNow.AddDate(0, 0, -1).Date()
	if y1 == yYest && m1 == mYest && d1 == dYest {
		return timeLabel, "Kemarin"
	}
	return timeLabel, fmt.Sprintf("%d %s %d", d1, indonesianMonths[int(m1)], y1)
}

func toTransactionDTO(tx sqlc.CashTransaction, items []sqlc.TransactionLineItem, now time.Time) TransactionDTO {
	timeLabel, dateGroup := formatWIBLabels(tx.TransactionAt, now)
	itemDTOs := make([]LineItemDTO, 0, len(items))
	for _, it := range items {
		itemDTOs = append(itemDTOs, LineItemDTO{
			ID:        it.ID.String(),
			Name:      it.Name,
			Quantity:  it.Quantity,
			UnitPrice: it.UnitPrice,
			Subtotal:  it.Subtotal,
		})
	}
	return TransactionDTO{
		ID:              tx.ID.String(),
		WorkspaceID:     tx.WorkspaceID.String(),
		Title:           tx.Title,
		Type:            tx.Type,
		Amount:          tx.Amount,
		Category:        tx.Category,
		PaymentMethod:   tx.PaymentMethod,
		CustomerName:    tx.CustomerName,
		NotaRef:         tx.NotaRef,
		HasReceiptPhoto: tx.HasReceiptPhoto,
		Source:          tx.Source,
		TimeLabel:       timeLabel,
		DateGroup:       dateGroup,
		TransactionAt:   tx.TransactionAt.UTC().Format(time.RFC3339),
		Items:           itemDTOs,
	}
}

func (s *Service) ListTransactions(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	limit, offset int32,
) ([]TransactionDTO, []auth.WorkspaceDTO, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	txs, allItems, user, wsRows, err := s.store.ListTransactions(ctx, userID, workspaceID, limit, offset)
	if err != nil {
		return nil, nil, err
	}

	itemsByTx := make(map[uuid.UUID][]sqlc.TransactionLineItem)
	for _, it := range allItems {
		itemsByTx[it.TransactionID] = append(itemsByTx[it.TransactionID], it)
	}

	now := time.Now()
	out := make([]TransactionDTO, 0, len(txs))
	for _, tx := range txs {
		out = append(out, toTransactionDTO(tx, itemsByTx[tx.ID], now))
	}

	return out, mapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) CreateTransaction(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	input CreateTransactionInput,
	ipAddr string,
) (*TransactionDTO, []auth.WorkspaceDTO, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" || len([]rune(title)) > 160 {
		return nil, nil, fmt.Errorf("%w: judul transaksi wajib diisi", ErrValidation)
	}

	txType := strings.ToLower(strings.TrimSpace(input.Type))
	if txType != "inflow" && txType != "outflow" {
		return nil, nil, fmt.Errorf("%w: tipe transaksi harus 'inflow' atau 'outflow'", ErrValidation)
	}

	source := strings.ToLower(strings.TrimSpace(input.Source))
	switch source {
	case "voice_ai", "receipt_ai":
	default:
		source = "manual"
	}

	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = "Lainnya"
	}
	paymentMethod := strings.TrimSpace(input.PaymentMethod)
	if paymentMethod == "" {
		paymentMethod = "Tunai"
	}

	txID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, err
	}

	itemParams := make([]sqlc.CreateTransactionLineItemParams, 0, len(input.Items))
	var computedSum int64
	for idx, it := range input.Items {
		itemName := strings.TrimSpace(it.Name)
		if itemName == "" {
			return nil, nil, fmt.Errorf("%w: nama rincian barang wajib diisi", ErrValidation)
		}
		if it.Quantity <= 0 {
			return nil, nil, fmt.Errorf("%w: jumlah barang harus lebih dari 0", ErrValidation)
		}
		if it.UnitPrice < 0 {
			return nil, nil, fmt.Errorf("%w: harga satuan tidak boleh negatif", ErrValidation)
		}
		itemID, err := uuid.NewV7()
		if err != nil {
			return nil, nil, err
		}
		subtotal := int64(it.Quantity) * it.UnitPrice
		computedSum += subtotal
		itemParams = append(itemParams, sqlc.CreateTransactionLineItemParams{
			ID:            itemID,
			TransactionID: txID,
			Name:          itemName,
			Quantity:      it.Quantity,
			UnitPrice:     it.UnitPrice,
			Subtotal:      subtotal,
			SortOrder:     int32(idx),
		})
	}

	finalAmount := input.Amount
	if len(itemParams) > 0 && computedSum > 0 {
		finalAmount = computedSum
	}
	if finalAmount <= 0 {
		return nil, nil, fmt.Errorf("%w: nominal transaksi harus lebih dari Rp 0", ErrValidation)
	}

	now := time.Now().UTC()
	txParams := sqlc.CreateCashTransactionParams{
		ID:              txID,
		WorkspaceID:     workspaceID,
		CreatedBy:       userID,
		Title:           title,
		Type:            txType,
		Amount:          finalAmount,
		Category:        category,
		PaymentMethod:   paymentMethod,
		CustomerName:    strings.TrimSpace(input.CustomerName),
		NotaRef:         strings.TrimSpace(input.NotaRef),
		HasReceiptPhoto: input.HasReceiptPhoto,
		Source:          source,
		TransactionAt:   now,
	}

	createdTx, createdItems, user, wsRows, err := s.store.CreateTransactionTx(
		ctx,
		userID,
		workspaceID,
		txParams,
		itemParams,
		ipAddr,
	)
	if err != nil {
		return nil, nil, err
	}

	dto := toTransactionDTO(createdTx, createdItems, now)
	return &dto, mapWorkspaces(wsRows, user.PlanTier), nil
}

func (s *Service) DeleteTransaction(
	ctx context.Context,
	userID, workspaceID, transactionID uuid.UUID,
	ipAddr string,
) ([]auth.WorkspaceDTO, error) {
	user, wsRows, err := s.store.SoftDeleteTransactionTx(ctx, userID, workspaceID, transactionID, ipAddr)
	if err != nil {
		return nil, err
	}
	return mapWorkspaces(wsRows, user.PlanTier), nil
}
