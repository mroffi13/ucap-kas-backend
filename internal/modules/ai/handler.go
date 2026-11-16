package ai

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/pkg/response"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) GetQuota(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Sesi tidak valid.")
		return
	}
	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_WORKSPACE_ID", "ID buku kas tidak valid.")
		return
	}

	quota, err := h.svc.GetQuota(r.Context(), wsID, userID)
	if err != nil {
		if errors.Is(err, ErrForbiddenWorkspace) {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke buku kas ini.")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memuat kuota AI.")
		return
	}

	response.JSON(w, http.StatusOK, quota)
}

func (h *Handler) ParseVoice(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Sesi tidak valid.")
		return
	}
	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_WORKSPACE_ID", "ID buku kas tidak valid.")
		return
	}

	var req ParseVoiceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_JSON", "Format JSON permintaan tidak valid.")
		return
	}

	res, err := h.svc.ParseVoice(r.Context(), wsID, userID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrForbiddenWorkspace):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke buku kas ini.")
		case errors.Is(err, ErrReadOnlyRole):
			response.Error(w, http.StatusForbidden, "READ_ONLY_ROLE", "Peran viewer tidak dapat menggunakan fitur AI.")
		case errors.Is(err, ErrAIWeeklyQuotaExceeded):
			response.Error(
				w,
				http.StatusForbidden,
				"AI_QUOTA_EXCEEDED",
				"Kuota AI mingguan Paket Starter (4x/minggu) telah habis. Upgrade ke Paket Pro untuk akses Suara & Foto Struk Tanpa Batas.",
			)
		case errors.Is(err, ErrAIQuotaExceeded):
			response.Error(
				w,
				http.StatusForbidden,
				"AI_QUOTA_EXCEEDED",
				"Kuota AI bulanan Paket Starter (15x/bulan) telah habis. Upgrade ke Paket Pro untuk akses Suara & Foto Struk Tanpa Batas.",
			)
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memproses catatan suara AI.")
		}
		return
	}

	response.JSON(w, http.StatusOK, res)
}

func (h *Handler) ParseReceipt(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Sesi tidak valid.")
		return
	}
	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_WORKSPACE_ID", "ID buku kas tidak valid.")
		return
	}

	var req ParseReceiptInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_JSON", "Format JSON permintaan tidak valid.")
		return
	}

	res, err := h.svc.ParseReceipt(r.Context(), wsID, userID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrForbiddenWorkspace):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke buku kas ini.")
		case errors.Is(err, ErrReadOnlyRole):
			response.Error(w, http.StatusForbidden, "READ_ONLY_ROLE", "Peran viewer tidak dapat menggunakan fitur AI.")
		case errors.Is(err, ErrAIWeeklyQuotaExceeded):
			response.Error(
				w,
				http.StatusForbidden,
				"AI_QUOTA_EXCEEDED",
				"Kuota AI mingguan Paket Starter (4x/minggu) telah habis. Upgrade ke Paket Pro untuk akses Suara & Foto Struk Tanpa Batas.",
			)
		case errors.Is(err, ErrAIQuotaExceeded):
			response.Error(
				w,
				http.StatusForbidden,
				"AI_QUOTA_EXCEEDED",
				"Kuota AI bulanan Paket Starter (15x/bulan) telah habis. Upgrade ke Paket Pro untuk akses Suara & Foto Struk Tanpa Batas.",
			)
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memproses foto struk AI.")
		}
		return
	}

	response.JSON(w, http.StatusOK, res)
}
