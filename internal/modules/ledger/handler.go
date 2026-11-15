package ledger

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

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

func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	workspaces, err := h.svc.ListWorkspaces(r.Context(), uid)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memuat daftar Buku Kas.")
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"workspaces": workspaces,
	})
}

func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	var req CreateWorkspaceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan tidak valid.")
		return
	}

	created, workspaces, err := h.svc.CreateWorkspace(r.Context(), uid, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, ErrWorkspaceLimitReached):
			response.Error(
				w,
				http.StatusForbidden,
				"WORKSPACE_LIMIT_REACHED",
				"Kuota Buku Kas untuk paket Anda telah penuh (Starter maks 1 Buku Kas, Pro maks 3 Buku Kas). Upgrade ke Paket Pro untuk menambah Buku Kas baru.",
			)
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal membuat Buku Kas baru.")
		}
		return
	}

	response.JSON(w, http.StatusCreated, map[string]any{
		"workspace":  created,
		"workspaces": workspaces,
	})
}

func (h *Handler) UpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID Buku Kas tidak valid.")
		return
	}

	var req UpdateWorkspaceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan tidak valid.")
		return
	}

	updated, workspaces, err := h.svc.UpdateWorkspace(r.Context(), uid, wsID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Hanya pemilik Buku Kas yang dapat mengubah pengaturan ini.")
		case errors.Is(err, ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "Buku Kas tidak ditemukan.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal menyimpan perubahan Buku Kas.")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"workspace":  updated,
		"workspaces": workspaces,
	})
}

func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID Buku Kas tidak valid.")
		return
	}

	limit := int32(200)
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 500 {
			limit = int32(parsed)
		}
	}
	offset := int32(0)
	if rawOffset := r.URL.Query().Get("offset"); rawOffset != "" {
		if parsed, err := strconv.Atoi(rawOffset); err == nil && parsed >= 0 {
			offset = int32(parsed)
		}
	}

	txs, workspaces, err := h.svc.ListTransactions(r.Context(), uid, wsID, limit, offset)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke Buku Kas ini.")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memuat riwayat transaksi.")
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"transactions": txs,
		"workspaces":   workspaces,
	})
}

func (h *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID Buku Kas tidak valid.")
		return
	}

	var req CreateTransactionInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan transaksi tidak valid.")
		return
	}

	createdTx, workspaces, err := h.svc.CreateTransaction(r.Context(), uid, wsID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin mencatat transaksi di Buku Kas ini.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal menyimpan catatan kas.")
		}
		return
	}

	response.JSON(w, http.StatusCreated, map[string]any{
		"transaction": createdTx,
		"workspaces":  workspaces,
	})
}

func (h *Handler) DeleteTransaction(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID Buku Kas tidak valid.")
		return
	}

	txID, err := uuid.Parse(chi.URLParam(r, "transactionID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID transaksi tidak valid.")
		return
	}

	workspaces, err := h.svc.DeleteTransaction(r.Context(), uid, wsID, txID, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin menghapus transaksi di Buku Kas ini.")
		case errors.Is(err, ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "Catatan transaksi tidak ditemukan.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal menghapus catatan transaksi.")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"deleted_id": txID.String(),
		"workspaces": workspaces,
	})
}
