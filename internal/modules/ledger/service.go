package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
	ErrMemberLimitReached    = errors.New("workspace member limit reached for current plan")
	ErrInviteExpiredOrUsed   = errors.New("invite link is expired, revoked, or already used")
	ErrAlreadyMember         = errors.New("user is already a member of this workspace")
	ErrValidation            = errors.New("invalid request payload")
	ErrNotFound              = errors.New("resource not found")
)

var wibZone = time.FixedZone("WIB", 7*3600)

type CreateWorkspaceInput struct {
	Name              string   `json:"name"`
	ShortName         string   `json:"short_name"`
	FocusType         string   `json:"focus_type"`
	BankName          string   `json:"bank_name"`
	BankAccountNumber string   `json:"bank_account_number"`
	BankAccountHolder string   `json:"bank_account_holder"`
	LogoURL           string   `json:"logo_url"`
	QuickCategories   []string `json:"quick_categories"`
}

type UpdateWorkspaceInput struct {
	Name              string   `json:"name"`
	ShortName         string   `json:"short_name"`
	FocusType         string   `json:"focus_type"`
	BankName          string   `json:"bank_name"`
	BankAccountNumber string   `json:"bank_account_number"`
	BankAccountHolder string   `json:"bank_account_holder"`
	LogoURL           string   `json:"logo_url"`
	QuickCategories   []string `json:"quick_categories"`
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
	ListMembers(ctx context.Context, userID, workspaceID uuid.UUID) ([]sqlc.ListWorkspaceMembersWithProfileRow, sqlc.User, *sqlc.WorkspaceInvite, string, error)
	CreateInviteTx(ctx context.Context, userID, workspaceID uuid.UUID, token string, expiresAt time.Time, ipAddr string) (sqlc.WorkspaceInvite, sqlc.Workspace, error)
	JoinWorkspaceByTokenTx(ctx context.Context, userID uuid.UUID, token, ipAddr string) (sqlc.Workspace, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	RemoveMemberTx(ctx context.Context, ownerID, workspaceID, targetUserID uuid.UUID, ipAddr string) ([]sqlc.ListWorkspaceMembersWithProfileRow, sqlc.User, error)
	ListActivityLogs(ctx context.Context, userID, workspaceID uuid.UUID, limit, offset int32) ([]sqlc.ListWorkspaceAuditLogsWithUserRow, error)
}

type WorkspaceMemberDTO struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	AvatarURL string `json:"avatar_url"`
	Role      string `json:"role"`
	RoleLabel string `json:"role_label"`
	IsOwner   bool   `json:"is_owner"`
	JoinedAt  string `json:"joined_at"`
}

type WorkspaceInviteDTO struct {
	Token       string `json:"token"`
	InviteURL   string `json:"invite_url"`
	DeepLink    string `json:"deep_link"`
	Role        string `json:"role"`
	RoleLabel   string `json:"role_label"`
	ExpiresAt   string `json:"expires_at"`
	WaShareText string `json:"wa_share_text"`
}

type WorkspaceMembersBundleDTO struct {
	Members      []WorkspaceMemberDTO `json:"members"`
	MaxMembers   int32                `json:"max_members"`
	ViewerRole   string               `json:"viewer_role"`
	ActiveInvite *WorkspaceInviteDTO  `json:"active_invite,omitempty"`
}

type WorkspaceActivityLogDTO struct {
	ID             string `json:"id"`
	ActorID        string `json:"actor_id"`
	ActorName      string `json:"actor_name"`
	ActorEmail     string `json:"actor_email"`
	ActorAvatarURL string `json:"actor_avatar_url"`
	Action         string `json:"action"`
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle"`
	TimeLabel      string `json:"time_label"`
	DateGroup      string `json:"date_group"`
	CreatedAt      string `json:"created_at"`
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

func maxMembersForPlan(planTier string) int32 {
	if planTier == "keluarga_pro" || planTier == "umkm_pro" {
		return 5
	}
	return 2 // Starter: 1 Owner + 1 Admin
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

	isPro := user.PlanTier == "keluarga_pro" || user.PlanTier == "umkm_pro"
	logoURL := ""
	if isPro {
		logoURL = strings.TrimSpace(input.LogoURL)
	}
	quickCatsJSON, _ := json.Marshal(auth.NormalizeQuickCategories(input.FocusType, input.QuickCategories))

	ws, err = qtx.UpdateWorkspaceCustom(ctx, sqlc.UpdateWorkspaceCustomParams{
		ID:                ws.ID,
		Name:              input.Name,
		ShortName:         input.ShortName,
		FocusType:         input.FocusType,
		BankName:          input.BankName,
		BankAccountNumber: input.BankAccountNumber,
		BankAccountHolder: input.BankAccountHolder,
		LogoUrl:           logoURL,
		QuickCategories:   quickCatsJSON,
	})
	if err != nil {
		return sqlc.User{}, sqlc.Workspace{}, nil, err
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

	isPro := user.PlanTier == "keluarga_pro" || user.PlanTier == "umkm_pro"
	logoURL := ""
	if isPro {
		logoURL = strings.TrimSpace(input.LogoURL)
	}
	quickCatsJSON, _ := json.Marshal(auth.NormalizeQuickCategories(input.FocusType, input.QuickCategories))

	ws, err := qtx.UpdateWorkspaceCustom(ctx, sqlc.UpdateWorkspaceCustomParams{
		ID:                workspaceID,
		Name:              input.Name,
		ShortName:         input.ShortName,
		FocusType:         input.FocusType,
		BankName:          input.BankName,
		BankAccountNumber: input.BankAccountNumber,
		BankAccountHolder: input.BankAccountHolder,
		LogoUrl:           logoURL,
		QuickCategories:   quickCatsJSON,
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

func (s *PgStore) ListMembers(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
) ([]sqlc.ListWorkspaceMembersWithProfileRow, sqlc.User, *sqlc.WorkspaceInvite, string, error) {
	viewerRole, err := s.queries.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, sqlc.User{}, nil, "", ErrForbidden
		}
		return nil, sqlc.User{}, nil, "", err
	}

	ws, err := s.queries.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, sqlc.User{}, nil, "", ErrNotFound
		}
		return nil, sqlc.User{}, nil, "", err
	}

	owner, err := s.queries.GetUserByID(ctx, ws.OwnerID)
	if err != nil {
		return nil, sqlc.User{}, nil, "", err
	}

	rows, err := s.queries.ListWorkspaceMembersWithProfile(ctx, workspaceID)
	if err != nil {
		return nil, sqlc.User{}, nil, "", err
	}

	var activeInv *sqlc.WorkspaceInvite
	if viewerRole == "owner" {
		inv, err := s.queries.GetActiveWorkspaceInviteByWorkspace(ctx, workspaceID)
		if err == nil {
			activeInv = &inv
		}
	}

	return rows, owner, activeInv, viewerRole, nil
}

func (s *PgStore) CreateInviteTx(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	token string,
	expiresAt time.Time,
	ipAddr string,
) (sqlc.WorkspaceInvite, sqlc.Workspace, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	role, err := qtx.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil || role != "owner" {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, ErrForbidden
	}

	ws, err := qtx.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, ErrNotFound
		}
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}

	owner, err := qtx.GetUserByID(ctx, ws.OwnerID)
	if err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}

	memberCount, err := qtx.CountWorkspaceMembers(ctx, workspaceID)
	if err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}
	if memberCount >= maxMembersForPlan(owner.PlanTier) {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, ErrMemberLimitReached
	}

	if err := qtx.RevokeActiveWorkspaceInvites(ctx, workspaceID); err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}

	invID, err := uuid.NewV7()
	if err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}

	createdInv, err := qtx.CreateWorkspaceInvite(ctx, sqlc.CreateWorkspaceInviteParams{
		ID:          invID,
		WorkspaceID: workspaceID,
		CreatedBy:   userID,
		Token:       token,
		Role:        "editor",
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"invite_id":  createdInv.ID.String(),
		"role":       createdInv.Role,
		"expires_at": createdInv.ExpiresAt.UTC().Format(time.RFC3339),
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "MEMBER_INVITE_CREATED",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	if err := tx.Commit(ctx); err != nil {
		return sqlc.WorkspaceInvite{}, sqlc.Workspace{}, err
	}
	return createdInv, ws, nil
}

func (s *PgStore) JoinWorkspaceByTokenTx(
	ctx context.Context,
	userID uuid.UUID,
	token, ipAddr string,
) (sqlc.Workspace, sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	inv, err := qtx.GetWorkspaceInviteByTokenForUpdate(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Workspace{}, sqlc.User{}, nil, ErrInviteExpiredOrUsed
		}
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}

	if inv.RevokedAt.Valid || inv.UsedBy.Valid || time.Now().UTC().After(inv.ExpiresAt) {
		return sqlc.Workspace{}, sqlc.User{}, nil, ErrInviteExpiredOrUsed
	}

	// Check if user is already a member of this workspace
	if existingRole, err := qtx.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: inv.WorkspaceID,
		UserID:      userID,
	}); err == nil && existingRole != "" {
		return sqlc.Workspace{}, sqlc.User{}, nil, ErrAlreadyMember
	}

	ws, err := qtx.GetWorkspaceByID(ctx, inv.WorkspaceID)
	if err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}

	owner, err := qtx.GetUserByID(ctx, ws.OwnerID)
	if err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}

	memberCount, err := qtx.CountWorkspaceMembers(ctx, inv.WorkspaceID)
	if err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}
	if memberCount >= maxMembersForPlan(owner.PlanTier) {
		return sqlc.Workspace{}, sqlc.User{}, nil, ErrMemberLimitReached
	}

	if err := qtx.AddWorkspaceMember(ctx, sqlc.AddWorkspaceMemberParams{
		WorkspaceID: inv.WorkspaceID,
		UserID:      userID,
		Role:        "editor",
	}); err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}

	if _, err := qtx.MarkWorkspaceInviteUsed(ctx, sqlc.MarkWorkspaceInviteUsedParams{
		ID:     inv.ID,
		UsedBy: uuid.NullUUID{UUID: userID, Valid: true},
	}); err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, ErrInviteExpiredOrUsed
	}

	user, err := qtx.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"invite_id":   inv.ID.String(),
		"member_name": user.FullName,
		"member_role": "editor",
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: inv.WorkspaceID, Valid: true},
		Action:       "MEMBER_JOINED",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	wsRows, err := qtx.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlc.Workspace{}, sqlc.User{}, nil, err
	}
	return ws, user, wsRows, nil
}

func (s *PgStore) RemoveMemberTx(
	ctx context.Context,
	ownerID, workspaceID, targetUserID uuid.UUID,
	ipAddr string,
) ([]sqlc.ListWorkspaceMembersWithProfileRow, sqlc.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, sqlc.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	role, err := qtx.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      ownerID,
	})
	if err != nil || role != "owner" {
		return nil, sqlc.User{}, ErrForbidden
	}
	if ownerID == targetUserID {
		return nil, sqlc.User{}, fmt.Errorf("%w: pemilik Buku Kas tidak dapat menghapus dirinya sendiri", ErrValidation)
	}

	targetUser, _ := qtx.GetUserByID(ctx, targetUserID)

	affected, err := qtx.RemoveWorkspaceMember(ctx, sqlc.RemoveWorkspaceMemberParams{
		WorkspaceID: workspaceID,
		UserID:      targetUserID,
	})
	if err != nil {
		return nil, sqlc.User{}, err
	}
	if affected == 0 {
		return nil, sqlc.User{}, ErrNotFound
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"removed_user_id":   targetUserID.String(),
		"removed_user_name": targetUser.FullName,
	})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: ownerID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "MEMBER_REMOVED",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	owner, err := qtx.GetUserByID(ctx, ownerID)
	if err != nil {
		return nil, sqlc.User{}, err
	}

	rows, err := qtx.ListWorkspaceMembersWithProfile(ctx, workspaceID)
	if err != nil {
		return nil, sqlc.User{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, sqlc.User{}, err
	}
	return rows, owner, nil
}

func (s *PgStore) ListActivityLogs(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	limit, offset int32,
) ([]sqlc.ListWorkspaceAuditLogsWithUserRow, error) {
	role, err := s.queries.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil || role != "owner" {
		return nil, ErrForbidden
	}

	return s.queries.ListWorkspaceAuditLogsWithUser(ctx, sqlc.ListWorkspaceAuditLogsWithUserParams{
		WorkspaceID: uuid.NullUUID{UUID: workspaceID, Valid: true},
		Limit:       limit,
		Offset:      offset,
	})
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

// MapWorkspaces maps sqlc workspace rows into auth.WorkspaceDTO slices.
func MapWorkspaces(rows []sqlc.ListWorkspacesByUserIDRow, planTier string) []auth.WorkspaceDTO {
	return mapWorkspaces(rows, planTier)
}

func mapWorkspaces(rows []sqlc.ListWorkspacesByUserIDRow, planTier string) []auth.WorkspaceDTO {
	isPro := planTier == "umkm_pro" || planTier == "keluarga_pro"
	out := make([]auth.WorkspaceDTO, 0, len(rows))
	for _, r := range rows {
		logoURL := ""
		if isPro {
			logoURL = r.LogoUrl
		}
		out = append(out, auth.WorkspaceDTO{
			ID:                 r.ID.String(),
			Name:               r.Name,
			ShortName:          r.ShortName,
			FocusType:          r.FocusType,
			BankName:           r.BankName,
			BankAccountNumber:  r.BankAccountNumber,
			BankAccountHolder:  r.BankAccountHolder,
			LogoURL:            logoURL,
			QuickCategories:    auth.ParseQuickCategoriesJSON(r.FocusType, r.QuickCategories),
			Role:               r.Role,
			Balance:            r.Balance,
			MonthlyInflow:      r.MonthlyInflow,
			MonthlyOutflow:     r.MonthlyOutflow,
			PrevMonthlyInflow:  r.PrevMonthlyInflow,
			PrevMonthlyOutflow: r.PrevMonthlyOutflow,
			IsPro:              isPro,
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
	cleanFocus := sanitizeFocus(input.FocusType)
	cleanInput := CreateWorkspaceInput{
		Name:              name,
		ShortName:         deriveShortName(name, input.ShortName),
		FocusType:         cleanFocus,
		BankName:          strings.TrimSpace(input.BankName),
		BankAccountNumber: strings.TrimSpace(input.BankAccountNumber),
		BankAccountHolder: strings.TrimSpace(input.BankAccountHolder),
		LogoURL:           strings.TrimSpace(input.LogoURL),
		QuickCategories:   auth.NormalizeQuickCategories(cleanFocus, input.QuickCategories),
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
	cleanFocus := sanitizeFocus(input.FocusType)
	cleanInput := UpdateWorkspaceInput{
		Name:              name,
		ShortName:         deriveShortName(name, input.ShortName),
		FocusType:         cleanFocus,
		BankName:          strings.TrimSpace(input.BankName),
		BankAccountNumber: strings.TrimSpace(input.BankAccountNumber),
		BankAccountHolder: strings.TrimSpace(input.BankAccountHolder),
		LogoURL:           strings.TrimSpace(input.LogoURL),
		QuickCategories:   auth.NormalizeQuickCategories(cleanFocus, input.QuickCategories),
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

// ToTransactionDTO converts a sqlc CashTransaction and its line items into a TransactionDTO.
func ToTransactionDTO(tx sqlc.CashTransaction, items []sqlc.TransactionLineItem, now time.Time) TransactionDTO {
	return toTransactionDTO(tx, items, now)
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

func mapRoleLabel(role string) string {
	switch role {
	case "owner":
		return "Pemilik"
	case "viewer":
		return "Pembaca"
	default:
		return "Admin"
	}
}

func mapMembers(rows []sqlc.ListWorkspaceMembersWithProfileRow) []WorkspaceMemberDTO {
	out := make([]WorkspaceMemberDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, WorkspaceMemberDTO{
			UserID:    r.UserID.String(),
			Email:     r.Email,
			FullName:  r.FullName,
			AvatarURL: r.AvatarUrl,
			Role:      r.Role,
			RoleLabel: mapRoleLabel(r.Role),
			IsOwner:   r.Role == "owner",
			JoinedAt:  r.JoinedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func buildInviteDTO(inv sqlc.WorkspaceInvite, wsName string) WorkspaceInviteDTO {
	inviteURL := fmt.Sprintf("https://ucapkas.id/join/%s", inv.Token)
	deepLink := fmt.Sprintf("ucapkas://join/%s", inv.Token)
	expWIB := inv.ExpiresAt.In(wibZone)
	expStr := fmt.Sprintf("%d %s %d pukul %02d:%02d WIB",
		expWIB.Day(), indonesianMonths[int(expWIB.Month())], expWIB.Year(), expWIB.Hour(), expWIB.Minute())
	waText := fmt.Sprintf(
		"Halo! Anda diundang menjadi *Admin* di Buku Kas *%s* pada aplikasi *UcapKas*.\n\n"+
			"Sebagai Admin, Anda dapat mencatat kas manual, menggunakan Suara AI & Foto Struk, serta mengelola Invoice.\n\n"+
			"Klik tautan undangan (sekali pakai, berlaku 24 jam s/d %s):\n%s\n\n"+
			"Atau buka menu *Akun > Gabung Buku Kas* di aplikasi UcapKas dan tempel kode undangan ini:\n*%s*",
		wsName, expStr, inviteURL, inv.Token,
	)
	return WorkspaceInviteDTO{
		Token:       inv.Token,
		InviteURL:   inviteURL,
		DeepLink:    deepLink,
		Role:        inv.Role,
		RoleLabel:   mapRoleLabel(inv.Role),
		ExpiresAt:   inv.ExpiresAt.UTC().Format(time.RFC3339),
		WaShareText: waText,
	}
}

func generateSecureInviteToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "uk-inv-" + hex.EncodeToString(b[:]), nil
}

var inviteTokenRegex = regexp.MustCompile(`(uk-inv-[a-zA-Z0-9_-]+)`)

func extractInviteToken(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if m := inviteTokenRegex.FindStringSubmatch(trimmed); len(m) > 1 {
		return m[1]
	}
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 && idx+1 < len(trimmed) {
		candidate := strings.TrimSpace(trimmed[idx+1:])
		candidate = strings.Trim(candidate, "*`'\" \t\r\n")
		if candidate != "" {
			return candidate
		}
	}
	return strings.Trim(trimmed, "*`'\" \t\r\n")
}

func (s *Service) ListMembers(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
) (*WorkspaceMembersBundleDTO, error) {
	rows, owner, activeInv, viewerRole, err := s.store.ListMembers(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	bundle := &WorkspaceMembersBundleDTO{
		Members:    mapMembers(rows),
		MaxMembers: maxMembersForPlan(owner.PlanTier),
		ViewerRole: viewerRole,
	}
	if activeInv != nil && viewerRole == "owner" {
		dto := buildInviteDTO(*activeInv, "Buku Kas Bersama")
		bundle.ActiveInvite = &dto
	}
	return bundle, nil
}

func (s *Service) CreateInvite(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	ipAddr string,
) (*WorkspaceInviteDTO, error) {
	token, err := generateSecureInviteToken()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	inv, ws, err := s.store.CreateInviteTx(ctx, userID, workspaceID, token, expiresAt, ipAddr)
	if err != nil {
		return nil, err
	}
	dto := buildInviteDTO(inv, ws.Name)
	return &dto, nil
}

func (s *Service) JoinWorkspaceByToken(
	ctx context.Context,
	userID uuid.UUID,
	rawToken, ipAddr string,
) (*auth.WorkspaceDTO, []auth.WorkspaceDTO, error) {
	token := extractInviteToken(rawToken)
	if token == "" || len(token) > 64 {
		return nil, nil, fmt.Errorf("%w: kode atau link undangan tidak valid", ErrValidation)
	}
	joinedWs, user, wsRows, err := s.store.JoinWorkspaceByTokenTx(ctx, userID, token, ipAddr)
	if err != nil {
		return nil, nil, err
	}
	dtos := mapWorkspaces(wsRows, user.PlanTier)
	var joinedDTO *auth.WorkspaceDTO
	for i := range dtos {
		if dtos[i].ID == joinedWs.ID.String() {
			joinedDTO = &dtos[i]
			break
		}
	}
	return joinedDTO, dtos, nil
}

func (s *Service) RemoveMember(
	ctx context.Context,
	ownerID, workspaceID, targetUserID uuid.UUID,
	ipAddr string,
) (*WorkspaceMembersBundleDTO, error) {
	rows, owner, err := s.store.RemoveMemberTx(ctx, ownerID, workspaceID, targetUserID, ipAddr)
	if err != nil {
		return nil, err
	}
	return &WorkspaceMembersBundleDTO{
		Members:    mapMembers(rows),
		MaxMembers: maxMembersForPlan(owner.PlanTier),
		ViewerRole: "owner",
	}, nil
}

func formatRupiahSimple(amount int64) string {
	if amount < 0 {
		amount = -amount
	}
	str := fmt.Sprintf("%d", amount)
	var b strings.Builder
	for i, ch := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(ch)
	}
	return "Rp " + b.String()
}

func formatActivityLog(row sqlc.ListWorkspaceAuditLogsWithUserRow, now time.Time) WorkspaceActivityLogDTO {
	timeLabel, dateGroup := formatWIBLabels(row.CreatedAt, now)
	var meta map[string]any
	_ = json.Unmarshal(row.MetadataJson, &meta)

	strMeta := func(k string) string {
		if v, ok := meta[k]; ok && v != nil {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	intMeta := func(k string) int64 {
		if v, ok := meta[k]; ok && v != nil {
			switch n := v.(type) {
			case float64:
				return int64(n)
			case int64:
				return n
			}
		}
		return 0
	}

	actor := strings.TrimSpace(row.ActorName)
	if actor == "" {
		actor = "Anggota"
	}

	var title, subtitle string
	switch row.Action {
	case "AI_VOICE_PARSE":
		intent := strMeta("intent")
		transcript := strMeta("transcript")
		amt := intMeta("total_amount")
		wUsed := intMeta("weekly_used")
		wLim := intMeta("weekly_limit")
		mUsed := intMeta("monthly_used")
		mLim := intMeta("monthly_limit")
		if intent == "kirim_invoice" {
			title = fmt.Sprintf("%s menggunakan Suara AI (Invoice)", actor)
		} else {
			title = fmt.Sprintf("%s menggunakan Suara AI (Kas)", actor)
		}
		if transcript != "" {
			if len([]rune(transcript)) > 52 {
				transcript = string([]rune(transcript)[:52]) + "..."
			}
			subtitle = fmt.Sprintf("\"%s\"", transcript)
		} else if amt > 0 {
			subtitle = fmt.Sprintf("Estimasi %s", formatRupiahSimple(amt))
		} else {
			subtitle = "Ekstraksi suara otomatis"
		}
		if mLim > 0 {
			if wLim > 0 && wLim <= 10 {
				subtitle += fmt.Sprintf(" · Kuota %d/%d mgg (%d/%d bln)", wUsed, wLim, mUsed, mLim)
			} else {
				subtitle += fmt.Sprintf(" · Kuota %d/%d bln", mUsed, mLim)
			}
		}
	case "AI_RECEIPT_PARSE":
		storeName := strMeta("store_name")
		amt := intMeta("total_amount")
		title = fmt.Sprintf("%s memindai Foto Struk AI", actor)
		if storeName != "" {
			subtitle = fmt.Sprintf("%s · %s", storeName, formatRupiahSimple(amt))
		} else {
			subtitle = "Ekstraksi foto struk belanja"
		}
	case "TRANSACTION_CREATE":
		txType := strMeta("type")
		amt := intMeta("amount")
		src := strMeta("source")
		if txType == "outflow" {
			title = fmt.Sprintf("%s mencatat Kas Keluar %s", actor, formatRupiahSimple(amt))
		} else {
			title = fmt.Sprintf("%s mencatat Kas Masuk %s", actor, formatRupiahSimple(amt))
		}
		switch src {
		case "voice_ai":
			subtitle = "Disimpan dari Catatan Suara AI"
		case "receipt_ai":
			subtitle = "Disimpan dari Scan Foto Struk"
		default:
			subtitle = "Input pencatatan kas manual"
		}
	case "TRANSACTION_DELETE":
		title = fmt.Sprintf("%s menghapus catatan transaksi kas", actor)
		subtitle = "Ref ID: " + strMeta("transaction_id")
	case "INVOICE_CREATE":
		invNum := strMeta("invoice_number")
		amt := intMeta("amount")
		title = fmt.Sprintf("%s membuat Invoice %s", actor, invNum)
		subtitle = fmt.Sprintf("Nominal tagihan %s", formatRupiahSimple(amt))
	case "INVOICE_MARK_PAID":
		invNum := strMeta("invoice_number")
		amt := intMeta("amount")
		method := strMeta("payment_method")
		title = fmt.Sprintf("%s menandai lunas %s", actor, invNum)
		subtitle = fmt.Sprintf("%s via %s (otomatis masuk kas)", formatRupiahSimple(amt), method)
	case "INVOICE_UPDATE":
		invNum := strMeta("invoice_number")
		title = fmt.Sprintf("%s memperbarui Invoice %s", actor, invNum)
		subtitle = "Status: " + strMeta("status")
	case "INVOICE_DELETE":
		title = fmt.Sprintf("%s menghapus dokumen Invoice", actor)
		subtitle = "Ref ID: " + strMeta("invoice_id")
	case "MEMBER_INVITE_CREATED":
		title = fmt.Sprintf("%s membuat link undangan WA baru", actor)
		subtitle = "Undangan Admin berlaku 24 jam (sekali pakai)"
	case "MEMBER_JOINED":
		memberName := strMeta("member_name")
		if memberName == "" {
			memberName = actor
		}
		title = fmt.Sprintf("%s bergabung sebagai Admin", memberName)
		subtitle = "Masuk melalui tautan undangan WhatsApp"
	case "MEMBER_REMOVED":
		removedName := strMeta("removed_user_name")
		if removedName == "" {
			removedName = "Anggota"
		}
		title = fmt.Sprintf("%s mengeluarkan %s dari Buku Kas", actor, removedName)
		subtitle = "Akses Admin dicabut"
	case "WORKSPACE_CREATE":
		title = fmt.Sprintf("%s membuat Buku Kas baru", actor)
		subtitle = strMeta("name")
	case "WORKSPACE_UPDATE":
		title = fmt.Sprintf("%s mengubah pengaturan Buku Kas", actor)
		subtitle = strMeta("name")
	default:
		title = fmt.Sprintf("%s melakukan aktivitas (%s)", actor, row.Action)
		subtitle = "Tercatat di sistem"
	}

	actorID := ""
	if row.UserID.Valid {
		actorID = row.UserID.UUID.String()
	}

	return WorkspaceActivityLogDTO{
		ID:             row.ID.String(),
		ActorID:        actorID,
		ActorName:      actor,
		ActorEmail:     row.ActorEmail,
		ActorAvatarURL: row.ActorAvatarUrl,
		Action:         row.Action,
		Title:          title,
		Subtitle:       subtitle,
		TimeLabel:      timeLabel,
		DateGroup:      dateGroup,
		CreatedAt:      row.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Service) ListActivityLogs(
	ctx context.Context,
	userID, workspaceID uuid.UUID,
	limit, offset int32,
) ([]WorkspaceActivityLogDTO, bool, error) {
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	if offset < 0 {
		offset = 0
	}
	// Query limit + 1 rows to determine if there is a next page without an extra COUNT query.
	rows, err := s.store.ListActivityLogs(ctx, userID, workspaceID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	hasMore := false
	if int32(len(rows)) > limit {
		hasMore = true
		rows = rows[:limit]
	}
	now := time.Now()
	out := make([]WorkspaceActivityLogDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, formatActivityLog(r, now))
	}
	return out, hasMore, nil
}
