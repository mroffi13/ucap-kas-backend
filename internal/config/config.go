package config

import (
	"errors"
	"os"
	"strings"
	"time"
)

type Config struct {
	AppEnv            string
	Port              string
	DatabaseURL       string
	JWTSecret         string
	AccessTokenTTL    time.Duration
	RefreshTokenTTL   time.Duration
	GoogleClientIDs   []string
	AllowDevMockAuth  bool
	AllowedCORSOrigin string
	GeminiAPIKey      string
	GeminiModel       string
	GroqAPIKey        string
	GroqSTTModel      string
	GroqLLMModel      string
	AIGatewayURL      string
	AIGatewayAPIKey   string
	AIGatewayModel    string
	AISTTModel        string
}

func Load() (*Config, error) {
	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	if appEnv == "" {
		appEnv = "production"
	}
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if len(jwtSecret) < 32 {
		return nil, errors.New("JWT_SECRET must be at least 32 characters long")
	}

	var clientIDs []string
	for _, raw := range strings.Split(os.Getenv("GOOGLE_CLIENT_IDS"), ",") {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			clientIDs = append(clientIDs, trimmed)
		}
	}

	// Hard security invariant: dev mock auth can NEVER be active outside development
	allowDevMock := appEnv == "development" && os.Getenv("ALLOW_DEV_MOCK_AUTH") == "true"

	corsOrigin := strings.TrimSpace(os.Getenv("ALLOWED_CORS_ORIGIN"))
	if corsOrigin == "" {
		corsOrigin = "*"
	}

	geminiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	geminiModel := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if geminiModel == "" {
		geminiModel = "gemini-2.5-flash"
	}

	groqKey := strings.TrimSpace(os.Getenv("GROQ_API_KEY"))
	groqSTTModel := strings.TrimSpace(os.Getenv("GROQ_STT_MODEL"))
	if groqSTTModel == "" {
		groqSTTModel = "whisper-large-v3-turbo"
	}
	groqLLMModel := strings.TrimSpace(os.Getenv("GROQ_LLM_MODEL"))
	if groqLLMModel == "" {
		groqLLMModel = "llama-3.3-70b-versatile"
	}

	gatewayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AI_GATEWAY_URL")), "/")
	gatewayKey := strings.TrimSpace(os.Getenv("AI_GATEWAY_API_KEY"))
	gatewayModel := strings.TrimSpace(os.Getenv("AI_GATEWAY_MODEL"))
	if gatewayModel == "" {
		gatewayModel = geminiModel
	}
	sttModel := strings.TrimSpace(os.Getenv("AI_STT_MODEL"))
	if sttModel == "" {
		sttModel = groqSTTModel
	}

	return &Config{
		AppEnv:            appEnv,
		Port:              port,
		DatabaseURL:       dbURL,
		JWTSecret:         jwtSecret,
		AccessTokenTTL:    15 * time.Minute,
		RefreshTokenTTL:   30 * 24 * time.Hour,
		GoogleClientIDs:   clientIDs,
		AllowDevMockAuth:  allowDevMock,
		AllowedCORSOrigin: corsOrigin,
		GeminiAPIKey:      geminiKey,
		GeminiModel:       geminiModel,
		GroqAPIKey:        groqKey,
		GroqSTTModel:      groqSTTModel,
		GroqLLMModel:      groqLLMModel,
		AIGatewayURL:      gatewayURL,
		AIGatewayAPIKey:   gatewayKey,
		AIGatewayModel:    gatewayModel,
		AISTTModel:        sttModel,
	}, nil
}
