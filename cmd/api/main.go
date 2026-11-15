package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/artech/ucapkas_backend/internal/config"
	"github.com/artech/ucapkas_backend/internal/db"
	"github.com/artech/ucapkas_backend/internal/modules/ai"
	"github.com/artech/ucapkas_backend/internal/modules/auth"
	"github.com/artech/ucapkas_backend/internal/modules/invoice"
	"github.com/artech/ucapkas_backend/internal/modules/ledger"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/artech/ucapkas_backend/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if envMap, err := godotenv.Read(); err == nil {
		for k, v := range envMap {
			if os.Getenv(k) == "" && v != "" {
				_ = os.Setenv(k, v)
			}
		}
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	if err := db.RunMigrations(cfg.DatabaseURL); err != nil {
		logger.Error("failed to run database migrations", "error", err)
		os.Exit(1)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("invalid DATABASE_URL", "error", err)
		os.Exit(1)
	}
	// Tuned for low-RAM 1GB VPS
	poolCfg.MaxConns = 15
	poolCfg.MinConns = 2
	poolCfg.MaxConnLifetime = 30 * time.Minute

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		logger.Error("failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	jwtMgr, err := jwtutil.NewManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	if err != nil {
		logger.Error("failed to init JWT manager", "error", err)
		os.Exit(1)
	}

	authStore := auth.NewPgStore(pool)
	googleVerifier := auth.NewGoogleVerifier(cfg.GoogleClientIDs, cfg.AllowDevMockAuth)
	authSvc := auth.NewService(authStore, googleVerifier, jwtMgr, cfg.RefreshTokenTTL)
	authHandler := auth.NewHandler(authSvc)

	ledgerStore := ledger.NewPgStore(pool)
	ledgerSvc := ledger.NewService(ledgerStore)
	ledgerHandler := ledger.NewHandler(ledgerSvc)

	aiStore := ai.NewPgStore(pool)
	aiParser := ai.NewHybridParserWithGateway(ai.GatewayConfig{
		GatewayURL:    cfg.AIGatewayURL,
		GatewayAPIKey: cfg.AIGatewayAPIKey,
		GatewayModel:  cfg.AIGatewayModel,
		STTModel:      cfg.AISTTModel,
		GroqAPIKey:    cfg.GroqAPIKey,
		GroqSTTModel:  cfg.GroqSTTModel,
		GroqLLMModel:  cfg.GroqLLMModel,
		GeminiAPIKey:  cfg.GeminiAPIKey,
		GeminiModel:   cfg.GeminiModel,
	})
	aiSvc := ai.NewService(aiStore, aiParser)
	aiHandler := ai.NewHandler(aiSvc)

	invoiceStore := invoice.NewPgStore(pool)
	invoiceSvc := invoice.NewService(invoiceStore)
	invoiceHandler := invoice.NewHandler(invoiceSvc)

	router := server.NewRouter(cfg, pool, jwtMgr, authHandler, ledgerHandler, aiHandler, invoiceHandler)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info(
			"ucapkas_backend started",
			"port", cfg.Port,
			"env", cfg.AppEnv,
			"google_client_ids_count", len(cfg.GoogleClientIDs),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server crashed", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down ucapkas_backend gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
