package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/artech/ucapkas_backend/internal/db/sqlc"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidGoogleToken = errors.New("invalid google id_token")
	ErrInvalidSession     = errors.New("invalid or expired session")
)

type LoginTxParams struct {
	NewUserID            uuid.UUID
	GoogleSub            string
	Email                string
	FullName             string
	AvatarURL            string
	NewWorkspaceID       uuid.UUID
	DefaultWorkspaceName string
	DefaultShortName     string
	FocusType            string
	SessionID            uuid.UUID
	RefreshTokenHash     string
	DeviceName           string
	IPAddress            string
	ExpiresAt            time.Time
}

type Store interface {
	UpsertUserAndDefaultWorkspaceTx(ctx context.Context, params LoginTxParams) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
	GetSessionByHash(ctx context.Context, hash string) (sqlc.AuthSession, error)
	RotateSessionTx(ctx context.Context, oldSessionID uuid.UUID, next sqlc.CreateAuthSessionParams) error
	HandleTokenReuseAttack(ctx context.Context, userID uuid.UUID, ip string) error
	RevokeSessionByHash(ctx context.Context, userID uuid.UUID, hash string, ip string) error
	GetUserAndWorkspaces(ctx context.Context, userID uuid.UUID) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error)
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

func (s *PgStore) UpsertUserAndDefaultWorkspaceTx(
	ctx context.Context,
	params LoginTxParams,
) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	user, err := qtx.UpsertUserByGoogleSub(ctx, sqlc.UpsertUserByGoogleSubParams{
		ID:        params.NewUserID,
		GoogleSub: params.GoogleSub,
		Email:     params.Email,
		FullName:  params.FullName,
		AvatarUrl: params.AvatarURL,
	})
	if err != nil {
		return sqlc.User{}, nil, err
	}

	wsList, err := qtx.ListWorkspacesByUserID(ctx, user.ID)
	if err != nil {
		return sqlc.User{}, nil, err
	}

	if len(wsList) == 0 {
		createdWs, err := qtx.CreateWorkspace(ctx, sqlc.CreateWorkspaceParams{
			ID:        params.NewWorkspaceID,
			OwnerID:   user.ID,
			Name:      params.DefaultWorkspaceName,
			ShortName: params.DefaultShortName,
			FocusType: params.FocusType,
			IsDefault: true,
		})
		if err != nil {
			return sqlc.User{}, nil, err
		}
		if err := qtx.AddWorkspaceMember(ctx, sqlc.AddWorkspaceMemberParams{
			WorkspaceID: createdWs.ID,
			UserID:      user.ID,
			Role:        "owner",
		}); err != nil {
			return sqlc.User{}, nil, err
		}
		wsList, err = qtx.ListWorkspacesByUserID(ctx, user.ID)
		if err != nil {
			return sqlc.User{}, nil, err
		}
	}

	if _, err := qtx.CreateAuthSession(ctx, sqlc.CreateAuthSessionParams{
		ID:               params.SessionID,
		UserID:           user.ID,
		RefreshTokenHash: params.RefreshTokenHash,
		DeviceName:       params.DeviceName,
		IpAddress:        params.IPAddress,
		ExpiresAt:        params.ExpiresAt,
	}); err != nil {
		return sqlc.User{}, nil, err
	}

	auditID, _ := uuid.NewV7()
	metaBytes, _ := json.Marshal(map[string]string{"device": params.DeviceName})
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: user.ID, Valid: true},
		WorkspaceID:  uuid.NullUUID{},
		Action:       "LOGIN_GOOGLE",
		IpAddress:    params.IPAddress,
		MetadataJson: metaBytes,
	})

	if err := tx.Commit(ctx); err != nil {
		return sqlc.User{}, nil, err
	}
	return user, wsList, nil
}

func (s *PgStore) GetSessionByHash(ctx context.Context, hash string) (sqlc.AuthSession, error) {
	return s.queries.GetAuthSessionByHash(ctx, hash)
}

func (s *PgStore) RotateSessionTx(
	ctx context.Context,
	oldSessionID uuid.UUID,
	next sqlc.CreateAuthSessionParams,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)
	if err := qtx.RevokeAuthSessionByID(ctx, oldSessionID); err != nil {
		return err
	}
	if _, err := qtx.CreateAuthSession(ctx, next); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) HandleTokenReuseAttack(ctx context.Context, userID uuid.UUID, ip string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)
	if err := qtx.RevokeAllUserSessions(ctx, userID); err != nil {
		return err
	}
	auditID, _ := uuid.NewV7()
	_ = qtx.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		Action:       "REFRESH_TOKEN_REUSE_DETECTED",
		IpAddress:    ip,
		MetadataJson: []byte(`{"severity":"high","action":"revoked_all_sessions"}`),
	})
	return tx.Commit(ctx)
}

func (s *PgStore) RevokeSessionByHash(ctx context.Context, userID uuid.UUID, hash string, ip string) error {
	sess, err := s.queries.GetAuthSessionByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if sess.UserID != userID {
		return nil
	}
	if err := s.queries.RevokeAuthSessionByID(ctx, sess.ID); err != nil {
		return err
	}
	auditID, _ := uuid.NewV7()
	_ = s.queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID:           auditID,
		UserID:       uuid.NullUUID{UUID: userID, Valid: true},
		Action:       "LOGOUT",
		IpAddress:    ip,
		MetadataJson: []byte(`{}`),
	})
	return nil
}

func (s *PgStore) GetUserAndWorkspaces(ctx context.Context, userID uuid.UUID) (sqlc.User, []sqlc.ListWorkspacesByUserIDRow, error) {
	user, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	wsList, err := s.queries.ListWorkspacesByUserID(ctx, userID)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	return user, wsList, nil
}

// DTOs & Service Layer

type UserDTO struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	AvatarURL string `json:"avatar_url"`
	PlanTier  string `json:"plan_tier"`
}

type WorkspaceDTO struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	ShortName         string `json:"short_name"`
	FocusType         string `json:"focus_type"`
	BankName          string `json:"bank_name"`
	BankAccountNumber string `json:"bank_account_number"`
	BankAccountHolder string `json:"bank_account_holder"`
	Role              string `json:"role"`
	Balance           int64  `json:"balance"`
	MonthlyInflow     int64  `json:"monthly_inflow"`
	MonthlyOutflow    int64  `json:"monthly_outflow"`
	IsPro             bool   `json:"is_pro"`
}

type AuthBundleDTO struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	ExpiresIn    int64          `json:"expires_in"`
	User         UserDTO        `json:"user"`
	Workspaces   []WorkspaceDTO `json:"workspaces"`
}

type TokenPairDTO struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type MeDTO struct {
	User       UserDTO        `json:"user"`
	Workspaces []WorkspaceDTO `json:"workspaces"`
}

type Service struct {
	store           Store
	googleVerifier  GoogleTokenVerifier
	jwtMgr          *jwtutil.Manager
	refreshTokenTTL time.Duration
}

func NewService(
	store Store,
	googleVerifier GoogleTokenVerifier,
	jwtMgr *jwtutil.Manager,
	refreshTokenTTL time.Duration,
) *Service {
	return &Service{
		store:           store,
		googleVerifier:  googleVerifier,
		jwtMgr:          jwtMgr,
		refreshTokenTTL: refreshTokenTTL,
	}
}

func normalizeFocus(raw string) (focus, wsName, shortName string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "rumah_tangga":
		return "rumah_tangga", "Dapur Rumah Tangga", "Dapur Rumah"
	case "komunitas":
		return "komunitas", "Kas Komunitas", "Komunitas"
	default:
		return "umkm", "Usaha Saya (UMKM)", "Usaha Saya"
	}
}

func (s *Service) LoginWithGoogle(
	ctx context.Context,
	rawIDToken, selectedFocus, deviceName, ipAddr string,
) (*AuthBundleDTO, error) {
	claims, err := s.googleVerifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, ErrInvalidGoogleToken
	}

	newUserID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	newWsID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	sessionID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	rawRefresh, refreshHash, err := jwtutil.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	focus, wsName, shortName := normalizeFocus(selectedFocus)
	if len(deviceName) > 140 {
		deviceName = deviceName[:140]
	}

	user, wsRows, err := s.store.UpsertUserAndDefaultWorkspaceTx(ctx, LoginTxParams{
		NewUserID:            newUserID,
		GoogleSub:            claims.Subject,
		Email:                claims.Email,
		FullName:             claims.FullName,
		AvatarURL:            claims.Picture,
		NewWorkspaceID:       newWsID,
		DefaultWorkspaceName: wsName,
		DefaultShortName:     shortName,
		FocusType:            focus,
		SessionID:            sessionID,
		RefreshTokenHash:     refreshHash,
		DeviceName:           strings.TrimSpace(deviceName),
		IPAddress:            ipAddr,
		ExpiresAt:            time.Now().UTC().Add(s.refreshTokenTTL),
	})
	if err != nil {
		return nil, err
	}

	accessToken, expiresIn, err := s.jwtMgr.GenerateAccessToken(user.ID)
	if err != nil {
		return nil, err
	}

	return &AuthBundleDTO{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		ExpiresIn:    expiresIn,
		User:         toUserDTO(user),
		Workspaces:   toWorkspaceDTOs(wsRows, user.PlanTier),
	}, nil
}

func (s *Service) RefreshSession(ctx context.Context, rawRefreshToken, ipAddr string) (*TokenPairDTO, error) {
	rawRefreshToken = strings.TrimSpace(rawRefreshToken)
	if rawRefreshToken == "" {
		return nil, ErrInvalidSession
	}
	hash := jwtutil.HashRefreshToken(rawRefreshToken)
	sess, err := s.store.GetSessionByHash(ctx, hash)
	if err != nil {
		return nil, ErrInvalidSession
	}

	// Token Reuse Detection: if a previously rotated/revoked token is replayed, revoke ALL sessions!
	if sess.RevokedAt.Valid {
		_ = s.store.HandleTokenReuseAttack(ctx, sess.UserID, ipAddr)
		return nil, ErrInvalidSession
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		return nil, ErrInvalidSession
	}

	nextSessionID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	nextRawRefresh, nextHash, err := jwtutil.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	if err := s.store.RotateSessionTx(ctx, sess.ID, sqlc.CreateAuthSessionParams{
		ID:               nextSessionID,
		UserID:           sess.UserID,
		RefreshTokenHash: nextHash,
		DeviceName:       sess.DeviceName,
		IpAddress:        ipAddr,
		ExpiresAt:        time.Now().UTC().Add(s.refreshTokenTTL),
	}); err != nil {
		return nil, err
	}

	accessToken, expiresIn, err := s.jwtMgr.GenerateAccessToken(sess.UserID)
	if err != nil {
		return nil, err
	}

	return &TokenPairDTO{
		AccessToken:  accessToken,
		RefreshToken: nextRawRefresh,
		ExpiresIn:    expiresIn,
	}, nil
}

func (s *Service) Logout(ctx context.Context, userID uuid.UUID, rawRefreshToken, ipAddr string) error {
	rawRefreshToken = strings.TrimSpace(rawRefreshToken)
	if rawRefreshToken == "" {
		return nil
	}
	hash := jwtutil.HashRefreshToken(rawRefreshToken)
	return s.store.RevokeSessionByHash(ctx, userID, hash, ipAddr)
}

func (s *Service) GetProfile(ctx context.Context, userID uuid.UUID) (*MeDTO, error) {
	user, wsRows, err := s.store.GetUserAndWorkspaces(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidSession
		}
		return nil, err
	}
	return &MeDTO{
		User:       toUserDTO(user),
		Workspaces: toWorkspaceDTOs(wsRows, user.PlanTier),
	}, nil
}

func toUserDTO(u sqlc.User) UserDTO {
	return UserDTO{
		ID:        u.ID.String(),
		Email:     u.Email,
		FullName:  u.FullName,
		AvatarURL: u.AvatarUrl,
		PlanTier:  u.PlanTier,
	}
}

func toWorkspaceDTOs(rows []sqlc.ListWorkspacesByUserIDRow, planTier string) []WorkspaceDTO {
	isPro := planTier == "umkm_pro" || planTier == "keluarga_pro"
	out := make([]WorkspaceDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, WorkspaceDTO{
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
