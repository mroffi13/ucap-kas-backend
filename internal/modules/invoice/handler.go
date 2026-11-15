package invoice

import (
	"encoding/json"
	"errors"
	"io"
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

func (h *Handler) ListInvoices(w http.ResponseWriter, r *http.Request) {
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

	statusFilter := r.URL.Query().Get("status")

	invoices, workspaces, err := h.svc.ListInvoices(r.Context(), uid, wsID, statusFilter, limit, offset)
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke Buku Kas ini.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memuat daftar tagihan & nota.")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"invoices":   invoices,
		"workspaces": workspaces,
	})
}

func (h *Handler) CreateInvoice(w http.ResponseWriter, r *http.Request) {
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

	var req CreateInvoiceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan invoice tidak valid.")
		return
	}

	created, workspaces, err := h.svc.CreateInvoice(r.Context(), uid, wsID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin membuat invoice di Buku Kas ini.")
		case errors.Is(err, ErrConflict):
			response.Error(w, http.StatusConflict, "INVOICE_NUMBER_EXISTS", "Nomor invoice sudah digunakan di Buku Kas ini.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal membuat invoice baru.")
		}
		return
	}

	response.JSON(w, http.StatusCreated, map[string]any{
		"invoice":    created,
		"workspaces": workspaces,
	})
}

func (h *Handler) MarkInvoicePaid(w http.ResponseWriter, r *http.Request) {
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

	invID, err := uuid.Parse(chi.URLParam(r, "invoiceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID invoice tidak valid.")
		return
	}

	var req MarkPaidInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan pelunasan tidak valid.")
		return
	}

	inv, tx, workspaces, err := h.svc.MarkInvoicePaid(r.Context(), uid, wsID, invID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin melunasi invoice di Buku Kas ini.")
		case errors.Is(err, ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "Invoice tidak ditemukan.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memproses pelunasan invoice.")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"invoice":     inv,
		"transaction": tx,
		"workspaces":  workspaces,
	})
}

func (h *Handler) UpdateInvoice(w http.ResponseWriter, r *http.Request) {
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

	invID, err := uuid.Parse(chi.URLParam(r, "invoiceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID invoice tidak valid.")
		return
	}

	var req UpdateInvoiceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan pembaruan invoice tidak valid.")
		return
	}

	inv, workspaces, err := h.svc.UpdateInvoice(r.Context(), uid, wsID, invID, req, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin mengubah invoice di Buku Kas ini.")
		case errors.Is(err, ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "Invoice tidak ditemukan.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memperbarui invoice.")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"invoice":    inv,
		"workspaces": workspaces,
	})
}

func (h *Handler) DeleteInvoice(w http.ResponseWriter, r *http.Request) {
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

	invID, err := uuid.Parse(chi.URLParam(r, "invoiceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID invoice tidak valid.")
		return
	}

	workspaces, err := h.svc.DeleteInvoice(r.Context(), uid, wsID, invID, middleware.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin menghapus invoice di Buku Kas ini.")
		case errors.Is(err, ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "Invoice tidak ditemukan.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal menghapus invoice.")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"deleted_id": invID.String(),
		"workspaces": workspaces,
	})
}

func (h *Handler) DownloadPDF(w http.ResponseWriter, r *http.Request) {
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

	invID, err := uuid.Parse(chi.URLParam(r, "invoiceID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_UUID", "ID invoice tidak valid.")
		return
	}

	pdfBytes, filename, err := h.svc.GenerateInvoicePDF(r.Context(), uid, wsID, invID)
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke Buku Kas ini.")
		case errors.Is(err, ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "Invoice tidak ditemukan.")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal membuat dokumen PDF invoice.")
		}
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(pdfBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}
