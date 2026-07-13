package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
)

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	setupRequired := true
	if h.config.AdminRepository != nil {
		count, err := h.config.AdminRepository.CountAdmins(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "status_unavailable", "Administrator status is unavailable.")
			return
		}
		setupRequired = count == 0
	}
	redisHealthy := true
	if h.config.Redis != nil {
		redisHealthy = h.config.Redis.Health(r.Context()) == nil
	}
	writeData(w, http.StatusOK, map[string]any{"service": h.config.ServiceName, "bootstrapped": !setupRequired, "setup_required": setupRequired, "gateway_healthy": h.config.Gateway != nil, "redis_healthy": redisHealthy})
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	if h.config.Auth == nil || h.config.AdminRepository == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "setup_unavailable", "Administrator setup is not configured.")
		return
	}
	var request struct {
		Username       string         `json:"username"`
		Email          string         `json:"email"`
		Password       string         `json:"password"`
		BootstrapToken string         `json:"bootstrap_token"`
		ServiceName    string         `json:"service_name"`
		PublicBaseURL  string         `json:"public_base_url"`
		Timezone       string         `json:"timezone"`
		Settings       map[string]any `json:"settings"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !validBootstrapToken(h.config.BootstrapToken, bootstrapToken(r, request.BootstrapToken)) {
		writeAPIError(w, http.StatusForbidden, "invalid_bootstrap_token", "The bootstrap token is invalid.")
		return
	}
	identity := request.Email
	if identity == "" {
		identity = request.Username
	}
	principal, err := h.config.Auth.Bootstrap(r.Context(), adminIdentity(identity), request.Password)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if h.config.Settings != nil {
		settings := request.Settings
		if settings == nil {
			settings = make(map[string]any)
		}
		if request.ServiceName != "" {
			settings["service_name"] = request.ServiceName
		}
		if request.PublicBaseURL != "" {
			settings["public_base_url"] = request.PublicBaseURL
		}
		if request.Timezone != "" {
			settings["timezone"] = request.Timezone
		}
		if err := h.config.Settings.SaveSettings(r.Context(), settings); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "settings_save_failed", "Administrator was created, but service settings could not be saved.")
			return
		}
	}
	writeData(w, http.StatusCreated, principal)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if h.config.Auth == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Administrator authentication is not configured.")
		return
	}
	var request struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
		Remember bool   `json:"remember"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	identity := request.Email
	if identity == "" {
		identity = request.Username
	}
	result, err := h.config.Auth.Login(r.Context(), admin.LoginInput{Email: adminIdentity(identity), Password: request.Password, TOTPCode: request.TOTPCode, IPAddress: h.clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.setSessionCookies(w, result, request.Remember)
	writeData(w, http.StatusOK, map[string]any{"principal": result.Principal, "csrf_token": result.CSRFToken, "expires_at": result.ExpiresAt})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, principalFromContext(r.Context()))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.config.SessionCookie); err == nil {
		if err := h.config.Auth.Logout(r.Context(), cookie.Value); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	h.clearCookies(w)
	writeData(w, http.StatusOK, map[string]any{"logged_out": true})
}

func (h *Handler) beginTOTP(w http.ResponseWriter, r *http.Request) {
	principal := principalFromContext(r.Context())
	enrollment, err := h.config.Auth.BeginTOTP(r.Context(), principal.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, enrollment)
}

func (h *Handler) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := h.config.Auth.ConfirmTOTP(r.Context(), principalFromContext(r.Context()).ID, request.Code); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"totp_enabled": true})
}

func (h *Handler) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var request struct{ Password, Code string }
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := h.config.Auth.DisableTOTP(r.Context(), principalFromContext(r.Context()).ID, request.Password, request.Code); err != nil {
		writeServiceError(w, err)
		return
	}
	h.clearCookies(w)
	writeData(w, http.StatusOK, map[string]any{"totp_enabled": false, "reauthenticate": true})
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		Confirmation    string `json:"confirm_password"`
		TOTPCode        string `json:"totp_code"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := h.config.Auth.ChangePassword(r.Context(), principalFromContext(r.Context()).ID, request.CurrentPassword, request.NewPassword, request.Confirmation, request.TOTPCode); err != nil {
		writeServiceError(w, err)
		return
	}
	h.clearCookies(w)
	writeData(w, http.StatusOK, map[string]any{"password_changed": true, "reauthenticate": true})
}

func (h *Handler) changeEmail(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email           string `json:"email"`
		CurrentPassword string `json:"current_password"`
		TOTPCode        string `json:"totp_code"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	principal, err := h.config.Auth.ChangeEmail(r.Context(), principalFromContext(r.Context()).ID, request.Email, request.CurrentPassword, request.TOTPCode)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.clearCookies(w)
	writeData(w, http.StatusOK, map[string]any{"principal": principal, "email_changed": true, "reauthenticate": true})
}

func (h *Handler) clientIP(r *http.Request) string {
	trustProxyHeaders := h.config.TrustProxyHeaders
	if h.config.RuntimeSettings != nil {
		trustProxyHeaders = h.config.RuntimeSettings.Active().TrustProxyHeaders
	}
	if trustProxyHeaders {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return forwarded
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
