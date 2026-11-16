package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbiddenWorkspace    = errors.New("anda tidak memiliki akses ke buku kas ini")
	ErrReadOnlyRole          = errors.New("peran viewer tidak dapat menggunakan fitur pencatatan AI")
	ErrAIQuotaExceeded       = errors.New("kuota AI Paket Starter telah habis")
	ErrAIWeeklyQuotaExceeded = fmt.Errorf("%w: kuota AI mingguan Paket Starter (4x/minggu) telah habis", ErrAIQuotaExceeded)
)

const (
	StarterWeeklyAIQuota  int64 = 4
	StarterMonthlyAIQuota int64 = 15
)

type QuotaStatus struct {
	Used             int64  `json:"used"`
	Limit            int64  `json:"limit"`
	WeeklyUsed       int64  `json:"weekly_used"`
	WeeklyLimit      int64  `json:"weekly_limit"`
	MonthlyUsed      int64  `json:"monthly_used"`
	MonthlyLimit     int64  `json:"monthly_limit"`
	IsUnlimited      bool   `json:"is_unlimited"`
	SubscriptionTier string `json:"subscription_tier"`
	OwnerID          string `json:"owner_id,omitempty"`
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
	GetWorkspaceOwnerUser(ctx context.Context, workspaceID uuid.UUID) (sqlc.User, error)
	CountMonthlyAIUsageByUser(ctx context.Context, userID uuid.UUID) (int64, error)
	CountMonthlyAIUsageByOwner(ctx context.Context, ownerID uuid.UUID) (int64, error)
	CountWeeklyAIUsageByOwner(ctx context.Context, ownerID uuid.UUID) (int64, error)
	InsertAIUsageLog(ctx context.Context, arg sqlc.InsertAIUsageLogParams) (sqlc.AiUsageLog, error)
	InsertAuditLog(ctx context.Context, arg sqlc.InsertAuditLogParams) error
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

func (s *PgStore) GetWorkspaceOwnerUser(ctx context.Context, workspaceID uuid.UUID) (sqlc.User, error) {
	return s.q.GetWorkspaceOwnerUser(ctx, workspaceID)
}

func (s *PgStore) CountMonthlyAIUsageByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.q.CountMonthlyAIUsageByUser(ctx, userID)
}

func (s *PgStore) CountMonthlyAIUsageByOwner(ctx context.Context, ownerID uuid.UUID) (int64, error) {
	return s.q.CountMonthlyAIUsageByOwner(ctx, ownerID)
}

func (s *PgStore) CountWeeklyAIUsageByOwner(ctx context.Context, ownerID uuid.UUID) (int64, error) {
	return s.q.CountWeeklyAIUsageByOwner(ctx, ownerID)
}

func (s *PgStore) InsertAIUsageLog(ctx context.Context, arg sqlc.InsertAIUsageLogParams) (sqlc.AiUsageLog, error) {
	return s.q.InsertAIUsageLog(ctx, arg)
}

func (s *PgStore) InsertAuditLog(ctx context.Context, arg sqlc.InsertAuditLogParams) error {
	return s.q.InsertAuditLog(ctx, arg)
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
	return s.computeQuota(ctx, workspaceID, userID)
}

func sanitizeProvider(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		p = "slang_engine"
	}
	if len(p) > 20 {
		p = p[:20]
	}
	return p
}

func checkQuotaLimit(quota *QuotaStatus) error {
	if quota.IsUnlimited {
		return nil
	}
	if quota.MonthlyUsed >= quota.MonthlyLimit {
		return fmt.Errorf("%w: kuota AI bulanan Paket Starter (%dx/bulan) telah habis", ErrAIQuotaExceeded, quota.MonthlyLimit)
	}
	if quota.WeeklyUsed >= quota.WeeklyLimit {
		return ErrAIWeeklyQuotaExceeded
	}
	return nil
}

func (s *Service) ParseVoice(ctx context.Context, workspaceID, userID uuid.UUID, in ParseVoiceInput, ipArgs ...string) (*VoiceResponse, error) {
	ipAddr := ""
	if len(ipArgs) > 0 {
		ipAddr = ipArgs[0]
	}

	role, err := s.store.GetWorkspaceRole(ctx, workspaceID, userID)
	if err != nil {
		return nil, ErrForbiddenWorkspace
	}
	if role != "owner" && role != "editor" {
		return nil, ErrReadOnlyRole
	}

	quota, err := s.computeQuota(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if err := checkQuotaLimit(quota); err != nil {
		slog.Warn("ai voice parse rejected: quota exceeded",
			"user_id", userID.String(),
			"owner_id", quota.OwnerID,
			"workspace_id", workspaceID.String(),
			"weekly_used", quota.WeeklyUsed,
			"weekly_limit", quota.WeeklyLimit,
			"monthly_used", quota.MonthlyUsed,
			"monthly_limit", quota.MonthlyLimit,
			"tier", quota.SubscriptionTier,
		)
		return nil, err
	}

	parsed, err := s.parser.ParseVoice(ctx, in)
	if err != nil {
		slog.Error("ai voice parse failed",
			"user_id", userID.String(),
			"workspace_id", workspaceID.String(),
			"error", err,
		)
		return nil, err
	}

	featureType := "voice_cash"
	if parsed.Intent == "kirim_invoice" {
		featureType = "voice_invoice"
	}
	provider := sanitizeProvider(parsed.Provider)

	logID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if _, err := s.store.InsertAIUsageLog(ctx, sqlc.InsertAIUsageLogParams{
		ID:          logID,
		WorkspaceID: workspaceID,
		UserID:      userID,
		FeatureType: featureType,
		Provider:    provider,
		TokensUsed:  parsed.TokensUsed,
	}); err != nil {
		slog.Error("failed to insert ai_usage_logs for voice parse",
			"user_id", userID.String(),
			"workspace_id", workspaceID.String(),
			"feature_type", featureType,
			"provider", provider,
			"error", err,
		)
		return nil, err
	}

	quota.Used++
	quota.WeeklyUsed++
	quota.MonthlyUsed++

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"usage_log_id":  logID.String(),
		"feature_type":  featureType,
		"provider":      provider,
		"intent":        parsed.Intent,
		"transcript":    parsed.Transcript,
		"items_count":   len(parsed.Items),
		"total_amount":  parsed.TotalAmount,
		"tokens_used":   parsed.TokensUsed,
		"weekly_used":   quota.WeeklyUsed,
		"weekly_limit":  quota.WeeklyLimit,
		"monthly_used":  quota.MonthlyUsed,
		"monthly_limit": quota.MonthlyLimit,
		"quota_used":    quota.Used,
		"quota_limit":   quota.Limit,
	})
	_ = s.store.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "AI_VOICE_PARSE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	slog.Info("ai voice parse logged and quota deducted",
		"usage_log_id", logID.String(),
		"user_id", userID.String(),
		"owner_id", quota.OwnerID,
		"workspace_id", workspaceID.String(),
		"feature_type", featureType,
		"provider", provider,
		"intent", parsed.Intent,
		"items_count", len(parsed.Items),
		"total_amount", parsed.TotalAmount,
		"tokens_used", parsed.TokensUsed,
		"weekly_used", quota.WeeklyUsed,
		"weekly_limit", quota.WeeklyLimit,
		"monthly_used", quota.MonthlyUsed,
		"monthly_limit", quota.MonthlyLimit,
		"is_unlimited", quota.IsUnlimited,
	)

	return &VoiceResponse{
		VoiceParseResult: parsed,
		Quota:            *quota,
	}, nil
}

func (s *Service) ParseReceipt(ctx context.Context, workspaceID, userID uuid.UUID, in ParseReceiptInput, ipArgs ...string) (*ReceiptResponse, error) {
	ipAddr := ""
	if len(ipArgs) > 0 {
		ipAddr = ipArgs[0]
	}

	role, err := s.store.GetWorkspaceRole(ctx, workspaceID, userID)
	if err != nil {
		return nil, ErrForbiddenWorkspace
	}
	if role != "owner" && role != "editor" {
		return nil, ErrReadOnlyRole
	}

	quota, err := s.computeQuota(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if err := checkQuotaLimit(quota); err != nil {
		slog.Warn("ai receipt parse rejected: quota exceeded",
			"user_id", userID.String(),
			"owner_id", quota.OwnerID,
			"workspace_id", workspaceID.String(),
			"weekly_used", quota.WeeklyUsed,
			"weekly_limit", quota.WeeklyLimit,
			"monthly_used", quota.MonthlyUsed,
			"monthly_limit", quota.MonthlyLimit,
			"tier", quota.SubscriptionTier,
		)
		return nil, err
	}

	parsed, err := s.parser.ParseReceipt(ctx, in)
	if err != nil {
		slog.Error("ai receipt parse failed",
			"user_id", userID.String(),
			"workspace_id", workspaceID.String(),
			"error", err,
		)
		return nil, err
	}

	provider := sanitizeProvider(parsed.Provider)
	logID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if _, err := s.store.InsertAIUsageLog(ctx, sqlc.InsertAIUsageLogParams{
		ID:          logID,
		WorkspaceID: workspaceID,
		UserID:      userID,
		FeatureType: "receipt_ocr",
		Provider:    provider,
		TokensUsed:  parsed.TokensUsed,
	}); err != nil {
		slog.Error("failed to insert ai_usage_logs for receipt parse",
			"user_id", userID.String(),
			"workspace_id", workspaceID.String(),
			"provider", provider,
			"error", err,
		)
		return nil, err
	}

	quota.Used++
	quota.WeeklyUsed++
	quota.MonthlyUsed++

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]any{
		"usage_log_id":  logID.String(),
		"feature_type":  "receipt_ocr",
		"provider":      provider,
		"store_name":    parsed.StoreName,
		"items_count":   len(parsed.Items),
		"total_amount":  parsed.TotalAmount,
		"tokens_used":   parsed.TokensUsed,
		"weekly_used":   quota.WeeklyUsed,
		"weekly_limit":  quota.WeeklyLimit,
		"monthly_used":  quota.MonthlyUsed,
		"monthly_limit": quota.MonthlyLimit,
		"quota_used":    quota.Used,
		"quota_limit":   quota.Limit,
	})
	_ = s.store.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		WorkspaceID:  uuid.NullUUID{UUID: workspaceID, Valid: true},
		Action:       "AI_RECEIPT_PARSE",
		IpAddress:    ipAddr,
		MetadataJson: metaBytes,
	})

	slog.Info("ai receipt parse logged and quota deducted",
		"usage_log_id", logID.String(),
		"user_id", userID.String(),
		"owner_id", quota.OwnerID,
		"workspace_id", workspaceID.String(),
		"provider", provider,
		"store_name", parsed.StoreName,
		"items_count", len(parsed.Items),
		"total_amount", parsed.TotalAmount,
		"tokens_used", parsed.TokensUsed,
		"weekly_used", quota.WeeklyUsed,
		"weekly_limit", quota.WeeklyLimit,
		"monthly_used", quota.MonthlyUsed,
		"monthly_limit", quota.MonthlyLimit,
		"is_unlimited", quota.IsUnlimited,
	)

	return &ReceiptResponse{
		ReceiptParseResult: parsed,
		Quota:              *quota,
	}, nil
}

func (s *Service) computeQuota(ctx context.Context, workspaceID, fallbackUserID uuid.UUID) (*QuotaStatus, error) {
	owner, err := s.store.GetWorkspaceOwnerUser(ctx, workspaceID)
	if err != nil {
		owner, err = s.store.GetUserByID(ctx, fallbackUserID)
		if err != nil {
			return nil, err
		}
	}

	monthlyUsed, err := s.store.CountMonthlyAIUsageByOwner(ctx, owner.ID)
	if err != nil {
		return nil, err
	}
	weeklyUsed, err := s.store.CountWeeklyAIUsageByOwner(ctx, owner.ID)
	if err != nil {
		return nil, err
	}

	isPro := owner.PlanTier == "keluarga_pro" || owner.PlanTier == "umkm_pro"
	weeklyLimit := StarterWeeklyAIQuota + int64(owner.ExtraAiQuota)
	monthlyLimit := StarterMonthlyAIQuota + int64(owner.ExtraAiQuota)
	if isPro {
		weeklyLimit = 500
		monthlyLimit = 1500 // Soft fair-use cap for Pro
	}

	return &QuotaStatus{
		Used:             monthlyUsed,
		Limit:            monthlyLimit,
		WeeklyUsed:       weeklyUsed,
		WeeklyLimit:      weeklyLimit,
		MonthlyUsed:      monthlyUsed,
		MonthlyLimit:     monthlyLimit,
		IsUnlimited:      isPro,
		SubscriptionTier: owner.PlanTier,
		OwnerID:          owner.ID.String(),
	}, nil
}
