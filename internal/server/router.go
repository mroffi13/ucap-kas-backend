package server

import (
	"context"
	"net/http"
	"time"

	"github.com/artech/ucapkas_backend/internal/config"
	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/modules/ai"
	"github.com/artech/ucapkas_backend/internal/modules/auth"
	"github.com/artech/ucapkas_backend/internal/modules/invoice"
	"github.com/artech/ucapkas_backend/internal/modules/ledger"
	"github.com/artech/ucapkas_backend/internal/pkg/jwtutil"
	"github.com/artech/ucapkas_backend/internal/pkg/response"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type DBPinger interface {
	Ping(ctx context.Context) error
}

func NewRouter(
	cfg *config.Config,
	pinger DBPinger,
	jwtMgr *jwtutil.Manager,
	authHandler *auth.Handler,
	ledgerHandler *ledger.Handler,
	aiHandler *ai.Handler,
	invoiceHandler *invoice.Handler,
) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(25 * time.Second))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.LimitBodyBytes(8 << 20)) // 8 MB max JSON payload (for base64 receipt/audio)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{cfg.AllowedCORSOrigin},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if pinger != nil {
			if err := pinger.Ping(ctx); err != nil {
				response.Error(w, http.StatusServiceUnavailable, "DB_UNAVAILABLE", "Database sedang tidak terjangkau.")
				return
			}
		}
		response.JSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"module": "modul_3_ai_voice_and_receipt",
		})
	})

	authRateLimiter := middleware.NewIPRateLimiter(20)
	aiRateLimiter := middleware.NewIPRateLimiter(30)

	r.Route("/api/v1", func(v1 chi.Router) {
		v1.Route("/auth", func(ar chi.Router) {
			ar.With(authRateLimiter).Post("/google", authHandler.GoogleSignIn)
			ar.With(authRateLimiter).Post("/refresh", authHandler.Refresh)

			ar.Group(func(pr chi.Router) {
				pr.Use(middleware.RequireAuth(jwtMgr))
				pr.Get("/me", authHandler.Me)
				pr.Post("/logout", authHandler.Logout)
			})
		})

		if ledgerHandler != nil {
			v1.Route("/workspaces", func(wr chi.Router) {
				wr.Use(middleware.RequireAuth(jwtMgr))
				wr.Get("/", ledgerHandler.ListWorkspaces)
				wr.Post("/", ledgerHandler.CreateWorkspace)
				wr.Post("/join", ledgerHandler.JoinWorkspace)
				wr.Patch("/{workspaceID}", ledgerHandler.UpdateWorkspace)
				wr.Get("/{workspaceID}/members", ledgerHandler.ListMembers)
				wr.Post("/{workspaceID}/invites", ledgerHandler.CreateInvite)
				wr.Delete("/{workspaceID}/members/{memberUserID}", ledgerHandler.RemoveMember)
				wr.Get("/{workspaceID}/activity-logs", ledgerHandler.ListActivityLogs)
				wr.Get("/{workspaceID}/transactions", ledgerHandler.ListTransactions)
				wr.Post("/{workspaceID}/transactions", ledgerHandler.CreateTransaction)
				wr.Delete("/{workspaceID}/transactions/{transactionID}", ledgerHandler.DeleteTransaction)

				if aiHandler != nil {
					wr.Route("/{workspaceID}/ai", func(air chi.Router) {
						air.Use(aiRateLimiter)
						air.Get("/quota", aiHandler.GetQuota)
						air.Post("/parse-voice", aiHandler.ParseVoice)
						air.Post("/parse-receipt", aiHandler.ParseReceipt)
					})
				}

				if invoiceHandler != nil {
					wr.Route("/{workspaceID}/invoices", func(ir chi.Router) {
						ir.Get("/", invoiceHandler.ListInvoices)
						ir.Post("/", invoiceHandler.CreateInvoice)
						ir.Get("/{invoiceID}/pdf", invoiceHandler.DownloadPDF)
						ir.Patch("/{invoiceID}", invoiceHandler.UpdateInvoice)
						ir.Post("/{invoiceID}/mark-paid", invoiceHandler.MarkInvoicePaid)
						ir.Delete("/{invoiceID}", invoiceHandler.DeleteInvoice)
					})
				}
			})
		}
	})

	return r
}
