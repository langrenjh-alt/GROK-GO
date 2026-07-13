package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func (h *Handler) mountOAuth(router chi.Router) {
	router.Get("/oauth/authorize", h.oauthAuthorize)
	router.Post("/oauth/authorize", h.oauthAuthorize)
	router.Post("/oauth/exchange", h.oauthExchange)
	router.Post("/oauth/refresh", h.oauthRefreshBody)
	router.Post("/oauth/refresh/{id}", h.oauthRefresh)

	router.Get("/build-oauth", h.listBuildOAuth)
	router.Get("/build-oauth/authorize", h.oauthAuthorize)
	router.Post("/build-oauth/authorize", h.oauthAuthorize)
	router.Post("/build-oauth/exchange", h.oauthExchange)
	router.Post("/build-oauth/refresh", h.oauthRefreshBody)
	router.Post("/build-oauth/{id}/refresh", h.oauthRefresh)
}

func (h *Handler) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if h.config.OAuth == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "oauth_unavailable", "CLI OAuth is not configured.")
		return
	}
	state := r.URL.Query().Get("state")
	if r.Method == http.MethodPost {
		var request struct {
			State string `json:"state"`
		}
		if err := h.decodeJSON(w, r, &request); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		state = request.State
	}
	session, err := h.config.OAuth.BeginContext(r.Context(), state)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, session)
}

type oauthExchangeRequest struct {
	Code             string `json:"code"`
	State            string `json:"state"`
	Verifier         string `json:"verifier"`
	AccountID        string `json:"account_id"`
	Name             string `json:"name"`
	Email            string `json:"email"`
	Tier             string `json:"tier"`
	Priority         int    `json:"priority"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
}

func (h *Handler) oauthExchange(w http.ResponseWriter, r *http.Request) {
	if h.config.OAuth == nil || !h.requireManagement(w) {
		if h.config.OAuth == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "oauth_unavailable", "CLI OAuth is not configured.")
		}
		return
	}
	var request oauthExchangeRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	var credentials domain.Credentials
	var err error
	if strings.TrimSpace(request.State) != "" {
		credentials, err = h.config.OAuth.ExchangeState(r.Context(), request.Code, request.State)
	} else {
		credentials, err = h.config.OAuth.Exchange(r.Context(), request.Code, request.Verifier)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	var account *domain.Account
	if request.AccountID != "" {
		account, err = h.config.Management.UpdateAccount(r.Context(), request.AccountID, admin.UpdateAccountInput{Credentials: &credentials})
	} else {
		if strings.TrimSpace(request.Name) == "" {
			writeAPIError(w, http.StatusBadRequest, "missing_account_name", "name is required when creating an OAuth account.")
			return
		}
		account, err = h.config.Management.CreateAccount(r.Context(), admin.CreateAccountInput{Name: request.Name, Kind: domain.CredentialCLIOAuth, Tier: request.Tier, Email: request.Email, Credentials: credentials, Priority: request.Priority, ConcurrencyLimit: request.ConcurrencyLimit})
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if err := h.reloadAccounts(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, account)
}

func (h *Handler) oauthRefreshBody(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AccountID string `json:"account_id"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	h.refreshOAuthAccount(w, r, request.AccountID)
}

func (h *Handler) oauthRefresh(w http.ResponseWriter, r *http.Request) {
	h.refreshOAuthAccount(w, r, chi.URLParam(r, "id"))
}

func (h *Handler) refreshOAuthAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if h.config.OAuthRefresh == nil || !h.requireManagement(w) {
		if h.config.OAuthRefresh == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "oauth_unavailable", "CLI OAuth is not configured.")
		}
		return
	}
	_, refreshErr := h.config.OAuthRefresh.RefreshAccount(r.Context(), accountID)
	if reloadErr := h.reloadAccounts(r.Context()); reloadErr != nil {
		writeServiceError(w, errors.Join(refreshErr, reloadErr))
		return
	}
	if refreshErr != nil {
		writeServiceError(w, refreshErr)
		return
	}
	account, err := h.config.Management.GetAccount(r.Context(), accountID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, account)
}

func (h *Handler) listBuildOAuth(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	page := pagination(r)
	items, err := h.config.Management.ListAccountsWithCredentialExpiry(r.Context(), store.AccountFilter{Pagination: page, Kind: domain.CredentialCLIOAuth})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset))
}
