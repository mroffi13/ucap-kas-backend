package ai

import (
	"context"
	"errors"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbiddenWorkspace = errors.New("anda tidak memiliki akses ke buku kas ini")
	ErrReadOnlyRole       = errors.New("peran viewer tidak dapat menggunakan fitur pencatatan AI")
	ErrAIQuotaExceeded    = errors.New("kuota AI bulanan Paket Starter (15x/bulan) telah habis")
)

const StarterMonthlyAIQuota int64 = 15

type QuotaStatus struct {
	Used             int64  `json:"used"`
	Limit            int64  `json:"limit"`
	IsUnlimited      bool   `json:"is_unlimited"`
	SubscriptionTier string `json:"subscription_tier"`
}

type VoiceResponse struct {
	*VoiceParseResult
	Quota QuotaStatus `json:"quota"`
}

type ReceiptResponse struct {
	*ReceiptParseResult
	Quota QuotaStatus `json:"quota"`
}

type Store interface {
	GetWorkspaceRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error)
	GetUserByID(ctx context.Context, userID uuid.UUID) (sqlc.User, error)
	CountMonthlyAIUsageByUser(ctx context.Context, userID uuid.UUID) (int64, error)
	InsertAIUsageLog(ctx context.Context, arg sqlc.InsertAIUsageLogParams) (sqlc.AiUsageLog, error)
}

type PgStore struct {
	q *sqlc.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{
		q: sqlc.New(pool),
	}
}

func (s *PgStore) GetWorkspaceRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error) {
	return s.q.GetWorkspaceMemberRole(ctx, sqlc.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
}

func (s *PgStore) GetUserByID(ctx context.Context, userID uuid.UUID) (sqlc.User, error) {
	return s.q.GetUserByID(ctx, userID)
}

func (s *PgStore) CountMonthlyAIUsageByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.q.CountMonthlyAIUsageByUser(ctx, userID)
}

func (s *PgStore) InsertAIUsageLog(ctx context.Context, arg sqlc.InsertAIUsageLogParams) (sqlc.AiUsageLog, error) {
	return s.q.InsertAIUsageLog(ctx, arg)
}

type Service struct {
	store  Store
	parser Parser
}

func NewService(store Store, parser Parser) *Service {
	return &Service{
		store:  store,
		parser: parser,
	}
}

func (s *Service) GetQuota(ctx context.Context, workspaceID, userID uuid.UUID) (*QuotaStatus, error) {
	if _, err := s.store.GetWorkspaceRole(ctx, workspaceID, userID); err != nil {
		return nil, ErrForbiddenWorkspace
	}
	return s.computeQuota(ctx, userID)
}

func (s *Service) ParseVoice(ctx context.Context, workspaceID, userID uuid.UUID, in ParseVoiceInput) (*VoiceResponse, error) {
	role, err := s.store.GetWorkspaceRole(ctx, workspaceID, userID)
	if err != nil {
		return nil, ErrForbiddenWorkspace
	}
	if role != "owner" && role != "editor" {
		return nil, ErrReadOnlyRole
	}

	quota, err := s.computeQuota(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !quota.IsUnlimited && quota.Used >= quota.Limit {
		return nil, ErrAIQuotaExceeded
	}

	parsed, err := s.parser.ParseVoice(ctx, in)
	if err != nil {
		return nil, err
	}

	featureType := "voice_cash"
	if parsed.Intent == "kirim_invoice" {
		featureType = "voice_invoice"
	}

	logID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	_, _ = s.store.InsertAIUsageLog(ctx, sqlc.InsertAIUsageLogParams{
		ID:          logID,
		WorkspaceID: workspaceID,
		UserID:      userID,
		FeatureType: featureType,
		Provider:    parsed.Provider,
		TokensUsed:  parsed.TokensUsed,
	})

	quota.Used++
	return &VoiceResponse{
		VoiceParseResult: parsed,
		Quota:            *quota,
	}, nil
}

func (s *Service) ParseReceipt(ctx context.Context, workspaceID, userID uuid.UUID, in ParseReceiptInput) (*ReceiptResponse, error) {
	role, err := s.store.GetWorkspaceRole(ctx, workspaceID, userID)
	if err != nil {
		return nil, ErrForbiddenWorkspace
	}
	if role != "owner" && role != "editor" {
		return nil, ErrReadOnlyRole
	}

	quota, err := s.computeQuota(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !quota.IsUnlimited && quota.Used >= quota.Limit {
		return nil, ErrAIQuotaExceeded
	}

	parsed, err := s.parser.ParseReceipt(ctx, in)
	if err != nil {
		return nil, err
	}

	logID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	_, _ = s.store.InsertAIUsageLog(ctx, sqlc.InsertAIUsageLogParams{
		ID:          logID,
		WorkspaceID: workspaceID,
		UserID:      userID,
		FeatureType: "receipt_ocr",
		Provider:    parsed.Provider,
		TokensUsed:  parsed.TokensUsed,
	})

	quota.Used++
	return &ReceiptResponse{
		ReceiptParseResult: parsed,
		Quota:              *quota,
	}, nil
}

func (s *Service) computeQuota(ctx context.Context, userID uuid.UUID) (*QuotaStatus, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	used, err := s.store.CountMonthlyAIUsageByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	isPro := user.PlanTier == "keluarga_pro" || user.PlanTier == "umkm_pro"
	limit := StarterMonthlyAIQuota + int64(user.ExtraAiQuota)
	if isPro {
		limit = 1500 // Soft fair-use cap for Pro
	}

	return &QuotaStatus{
		Used:             used,
		Limit:            limit,
		IsUnlimited:      isPro,
		SubscriptionTier: user.PlanTier,
	}, nil
}
