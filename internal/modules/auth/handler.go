package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/artech/ucapkas_backend/internal/middleware"
	"github.com/artech/ucapkas_backend/internal/pkg/response"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type googleSignInRequest struct {
	IDToken       string `json:"id_token"`
	SelectedFocus string `json:"selected_focus"`
	DeviceName    string `json:"device_name"`
	AvatarURL     string `json:"avatar_url"`
}

func (h *Handler) GoogleSignIn(w http.ResponseWriter, r *http.Request) {
	var req googleSignInRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan tidak valid.")
		return
	}

	bundle, err := h.svc.LoginWithGoogle(
		r.Context(),
		req.IDToken,
		req.SelectedFocus,
		req.DeviceName,
		middleware.ClientIP(r),
		req.AvatarURL,
	)
	if err != nil {
		slog.Error("google sign-in failed", "error", err)
		if errors.Is(err, ErrInvalidGoogleToken) {
			response.Error(w, http.StatusUnauthorized, "INVALID_GOOGLE_TOKEN", "Verifikasi akun Google gagal. Silakan coba lagi.")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Terjadi gangguan saat memproses login.")
		return
	}

	response.JSON(w, http.StatusOK, bundle)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Format permintaan tidak valid.")
		return
	}

	pair, err := h.svc.RefreshSession(r.Context(), req.RefreshToken, middleware.ClientIP(r))
	if err != nil {
		if errors.Is(err, ErrInvalidSession) {
			response.Error(w, http.StatusUnauthorized, "SESSION_EXPIRED", "Sesi Anda telah berakhir. Silakan masuk kembali.")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memperbarui sesi.")
		return
	}

	response.JSON(w, http.StatusOK, pair)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}
	var req refreshRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.svc.Logout(r.Context(), uid, req.RefreshToken, middleware.ClientIP(r)); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal keluar dari sesi.")
		return
	}

	response.JSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "Tidak terautentikasi.")
		return
	}

	profile, err := h.svc.GetProfile(r.Context(), uid)
	if err != nil {
		if errors.Is(err, ErrInvalidSession) {
			response.Error(w, http.StatusUnauthorized, "USER_NOT_FOUND", "Akun tidak ditemukan.")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Gagal memuat profil.")
		return
	}

	response.JSON(w, http.StatusOK, profile)
}
