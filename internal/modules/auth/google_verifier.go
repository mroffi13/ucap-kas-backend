package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/api/idtoken"
)

type VerifiedGoogleClaims struct {
	Subject  string
	Email    string
	FullName string
	Picture  string
}

type GoogleTokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (*VerifiedGoogleClaims, error)
}

type CryptographicGoogleVerifier struct {
	clientIDs    []string
	allowDevMock bool
}

func NewGoogleVerifier(clientIDs []string, allowDevMock bool) *CryptographicGoogleVerifier {
	return &CryptographicGoogleVerifier{
		clientIDs:    clientIDs,
		allowDevMock: allowDevMock,
	}
}

func (v *CryptographicGoogleVerifier) Verify(ctx context.Context, rawToken string) (*VerifiedGoogleClaims, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, errors.New("missing google id_token")
	}

	// Strictly gated local simulator testing helper (NEVER active in production; enforced by config.Load)
	if v.allowDevMock && strings.HasPrefix(rawToken, "dev_mock:") {
		parts := strings.SplitN(strings.TrimPrefix(rawToken, "dev_mock:"), ":", 2)
		email := strings.TrimSpace(parts[0])
		if email == "" || !strings.Contains(email, "@") {
			return nil, errors.New("invalid dev_mock email")
		}
		name := "Pengguna UcapKas"
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			name = strings.TrimSpace(parts[1])
		}
		subSum := sha256.Sum256([]byte("dev_google_sub:" + strings.ToLower(email)))
		return &VerifiedGoogleClaims{
			Subject:  "dev_" + hex.EncodeToString(subSum[:16]),
			Email:    strings.ToLower(email),
			FullName: name,
			Picture:  "",
		}, nil
	}

	if len(v.clientIDs) == 0 {
		return nil, errors.New("GOOGLE_CLIENT_IDS is not configured on server")
	}

	var lastErr error
	for _, clientID := range v.clientIDs {
		payload, err := idtoken.Validate(ctx, rawToken, clientID)
		if err != nil {
			lastErr = err
			continue
		}
		emailVerified, _ := payload.Claims["email_verified"].(bool)
		if !emailVerified {
			return nil, errors.New("google account email is not verified")
		}
		email, _ := payload.Claims["email"].(string)
		if email == "" {
			return nil, errors.New("google id_token missing email claim")
		}
		name, _ := payload.Claims["name"].(string)
		if strings.TrimSpace(name) == "" {
			name = strings.Split(email, "@")[0]
		}
		picture, _ := payload.Claims["picture"].(string)
		return &VerifiedGoogleClaims{
			Subject:  payload.Subject,
			Email:    strings.ToLower(strings.TrimSpace(email)),
			FullName: strings.TrimSpace(name),
			Picture:  strings.TrimSpace(picture),
		}, nil
	}

	return nil, fmt.Errorf("invalid google id_token: %w", lastErr)
}
