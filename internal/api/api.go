package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/accountprobe"
	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	mediastore "github.com/langrenjh-alt/GROK-GO/internal/media"
	"github.com/langrenjh-alt/GROK-GO/internal/runtimecfg"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type SettingsStore interface {
	LoadSettings(context.Context) (map[string]any, error)
	SaveSettings(context.Context, map[string]any) error
}

type MediaAdmin interface {
	ListMedia(context.Context, store.Pagination) ([]domain.MediaObject, int64, error)
	DeleteMedia(context.Context, string) error
	MediaSummary(context.Context, time.Duration) (mediastore.Summary, error)
	DeleteMediaBatch(context.Context, []string) (mediastore.DeletionResult, error)
	CleanupMedia(context.Context, bool) (mediastore.DeletionResult, error)
}

type ProxyChecker interface {
	CheckProxy(context.Context, string) error
}

type ProxyCheckerFunc func(context.Context, string) error

func (f ProxyCheckerFunc) CheckProxy(ctx context.Context, proxyURL string) error {
	return f(ctx, proxyURL)
}

type Config struct {
	Auth              *admin.AuthService
	Audit             store.AuditLogRepository
	Management        *admin.ManagementService
	AdminRepository   store.AdminRepository
	Accounts          *accounts.Pool
	AccountProbe      *accountprobe.Service
	OAuth             *upstream.OAuthService
	OAuthRefresh      *upstream.RefreshService
	Redis             *database.Redis
	Gateway           http.Handler
	Settings          SettingsStore
	Media             MediaAdmin
	ProxyChecker      ProxyChecker
	BootstrapToken    string
	SessionCookie     string
	CSRFCookie        string
	CSRFHeader        string
	CookieDomain      string
	CookieSecure      bool
	TrustedOrigin     string
	TrustProxyHeaders bool
	ServiceName       string
	MaxRequestBytes   int64
	RuntimeSettings   *runtimecfg.Runtime
	ConfigChanges     configevent.Notifier
}

type Handler struct {
	config Config
	router chi.Router
}

type contextKey string

const principalContextKey contextKey = "admin-principal"

func NewAdminHandler(config Config) http.Handler {
	if config.SessionCookie == "" {
		config.SessionCookie = "grok_go_admin"
	}
	if config.CSRFCookie == "" {
		config.CSRFCookie = "grok_go_csrf"
	}
	if config.CSRFHeader == "" {
		config.CSRFHeader = "X-CSRF-Token"
	}
	if config.ServiceName == "" {
		config.ServiceName = "GROK-GO"
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = 16 << 20
	}
	if config.RuntimeSettings == nil {
		defaults := runtimecfg.Defaults()
		config.RuntimeSettings = runtimecfg.MustRuntime(defaults, defaults)
	}
	h := &Handler{config: config}
	router := chi.NewRouter()
	router.Get("/status", h.status)
	router.Post("/setup", h.setup)
	router.Post("/auth/login", h.login)

	router.Group(func(protected chi.Router) {
		protected.Use(h.authenticate)
		protected.Use(h.auditMutations)
		protected.Use(h.verifyCSRF)
		protected.Get("/auth/me", h.me)
		protected.Post("/auth/logout", h.logout)
		protected.Post("/auth/totp/begin", h.beginTOTP)
		protected.Post("/auth/totp/confirm", h.confirmTOTP)
		protected.Post("/auth/totp/disable", h.disableTOTP)
		protected.Delete("/auth/totp", h.disableTOTP)
		protected.Patch("/auth/email", h.changeEmail)
		protected.Post("/auth/password", h.changePassword)

		protected.Get("/dashboard", h.dashboard)
		h.mountAccounts(protected)
		h.mountOAuth(protected)
		h.mountProxies(protected)
		h.mountKeys(protected)
		h.mountModels(protected)
		h.mountLogs(protected)
		h.mountAuditLogs(protected)
		h.mountExtras(protected)
	})
	h.router = router
	return h
}

func (h *Handler) notifyConfiguration(ctx context.Context, scope configevent.Scope) {
	if h.config.ConfigChanges == nil {
		return
	}
	if err := h.config.ConfigChanges.Notify(ctx, scope); err != nil {
		slog.Warn("configuration notification failed", "scope", scope, "error", err)
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.config.Auth == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Administrator authentication is not configured.")
			return
		}
		cookie, err := r.Cookie(h.config.SessionCookie)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Administrator authentication is required.")
			return
		}
		principal, err := h.config.Auth.AuthenticateSession(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, admin.ErrAuthStateUnavailable) {
				writeAPIError(w, http.StatusServiceUnavailable, "auth_state_unavailable", "Administrator authentication state is temporarily unavailable.")
				return
			}
			h.clearCookies(w)
			writeAPIError(w, http.StatusUnauthorized, "invalid_session", "The administrator session is invalid or expired.")
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) verifyCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if !h.originAllowed(r) {
			writeAPIError(w, http.StatusForbidden, "invalid_origin", "The request origin is not trusted.")
			return
		}
		session, sessionErr := r.Cookie(h.config.SessionCookie)
		csrfToken := strings.TrimSpace(r.Header.Get(h.config.CSRFHeader))
		if sessionErr != nil || csrfToken == "" {
			writeAPIError(w, http.StatusForbidden, "invalid_csrf", "A valid CSRF token is required.")
			return
		}
		if err := h.config.Auth.VerifyCSRF(r.Context(), session.Value, csrfToken); err != nil {
			if errors.Is(err, admin.ErrAuthStateUnavailable) {
				writeAPIError(w, http.StatusServiceUnavailable, "auth_state_unavailable", "Administrator authentication state is temporarily unavailable.")
				return
			}
			writeAPIError(w, http.StatusForbidden, "invalid_csrf", "A valid CSRF token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) originAllowed(request *http.Request) bool {
	expected := strings.TrimRight(strings.TrimSpace(h.config.TrustedOrigin), "/")
	if expected == "" {
		return true
	}
	origin := strings.TrimRight(strings.TrimSpace(request.Header.Get("Origin")), "/")
	if origin == "" {
		referer := strings.TrimSpace(request.Referer())
		if referer == "" {
			return true
		}
		parsed, err := url.Parse(referer)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return false
		}
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return subtle.ConstantTimeCompare([]byte(origin), []byte(expected)) == 1
}

func principalFromContext(ctx context.Context) *admin.Principal {
	principal, _ := ctx.Value(principalContextKey).(*admin.Principal)
	return principal
}

func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	maximum := h.config.MaxRequestBytes
	if h.config.RuntimeSettings != nil {
		maximum = h.config.RuntimeSettings.Active().MaxRequestBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request body must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func writeServiceError(w http.ResponseWriter, err error) {
	var rateLimit *admin.LoginRateLimitError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "The requested record was not found.")
	case errors.Is(err, mediastore.ErrMediaNotFound):
		writeAPIError(w, http.StatusNotFound, "media_not_found", "The requested media object was not found.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, admin.ErrAlreadyBootstrapped):
		writeAPIError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, admin.ErrEmailInUse):
		writeAPIError(w, http.StatusConflict, "email_in_use", err.Error())
	case errors.Is(err, admin.ErrInvalidEmail):
		writeAPIError(w, http.StatusBadRequest, "invalid_email", err.Error())
	case errors.Is(err, admin.ErrEmailUnchanged):
		writeAPIError(w, http.StatusBadRequest, "email_unchanged", err.Error())
	case errors.Is(err, admin.ErrPasswordConfirmation):
		writeAPIError(w, http.StatusBadRequest, "password_confirmation_mismatch", err.Error())
	case errors.Is(err, admin.ErrPasswordUnchanged):
		writeAPIError(w, http.StatusBadRequest, "password_unchanged", err.Error())
	case errors.Is(err, admin.ErrPasswordTooLong):
		writeAPIError(w, http.StatusBadRequest, "password_too_long", err.Error())
	case errors.Is(err, admin.ErrInvalidCredentials), errors.Is(err, admin.ErrInvalidTOTP):
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "The supplied credentials are invalid.")
	case errors.As(err, &rateLimit):
		retryAfter := max(1, int((rateLimit.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		writeAPIError(w, http.StatusTooManyRequests, "login_rate_limited", "Too many administrator login attempts.")
	case errors.Is(err, admin.ErrAuthStateUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "auth_state_unavailable", "Administrator authentication state is temporarily unavailable.")
	case errors.Is(err, admin.ErrTOTPRequired):
		writeAPIError(w, http.StatusUnauthorized, "totp_required", "A TOTP code is required.")
	case errors.Is(err, admin.ErrInvalidSession):
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "The administrator session is invalid or expired.")
	case errors.Is(err, admin.ErrTOTPNotPending):
		writeAPIError(w, http.StatusConflict, "totp_not_pending", err.Error())
	default:
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = "The request could not be completed."
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_request", message)
	}
}

func (h *Handler) setSessionCookies(w http.ResponseWriter, result *admin.LoginResult, remember bool) {
	session := &http.Cookie{Name: h.config.SessionCookie, Value: result.SessionToken, Path: "/", Domain: h.config.CookieDomain, HttpOnly: true, Secure: h.config.CookieSecure, SameSite: http.SameSiteStrictMode}
	csrf := &http.Cookie{Name: h.config.CSRFCookie, Value: result.CSRFToken, Path: "/", Domain: h.config.CookieDomain, HttpOnly: false, Secure: h.config.CookieSecure, SameSite: http.SameSiteStrictMode}
	if remember {
		session.Expires, csrf.Expires = result.ExpiresAt, result.ExpiresAt
		session.MaxAge, csrf.MaxAge = max(1, int(time.Until(result.ExpiresAt).Seconds())), max(1, int(time.Until(result.ExpiresAt).Seconds()))
	}
	http.SetCookie(w, session)
	http.SetCookie(w, csrf)
	w.Header().Set(h.config.CSRFHeader, result.CSRFToken)
}

func (h *Handler) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{h.config.SessionCookie, h.config.CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", Domain: h.config.CookieDomain, HttpOnly: name == h.config.SessionCookie, Secure: h.config.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

func bootstrapToken(request *http.Request, bodyValue string) string {
	if value := strings.TrimSpace(request.Header.Get("X-Bootstrap-Token")); value != "" {
		return value
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return strings.TrimSpace(authorization[7:])
	}
	return strings.TrimSpace(bodyValue)
}

func validBootstrapToken(expected, supplied string) bool {
	if expected == "" || len(expected) != len(supplied) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func adminIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.Contains(value, "@") && value != "" {
		value += "@grok-go.local"
	}
	return value
}
