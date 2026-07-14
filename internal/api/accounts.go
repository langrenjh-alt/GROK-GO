package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/accountprobe"
	accountpool "github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

const (
	accountDeleteSyncTimeout          = 5 * time.Second
	accountImportActionTimeout        = 60 * time.Second
	accountImportCleanupTimeout       = 15 * time.Second
	accountImportActionMaxConcurrency = 5
)

type accountRequest struct {
	Name             string                `json:"name"`
	Kind             domain.CredentialKind `json:"kind"`
	Tier             string                `json:"tier"`
	Pool             string                `json:"pool"`
	Email            string                `json:"email"`
	Credentials      *domain.Credentials   `json:"credentials"`
	AccessToken      string                `json:"access_token"`
	RefreshToken     string                `json:"refresh_token"`
	IDToken          string                `json:"id_token"`
	SSO              string                `json:"sso"`
	SSORW            string                `json:"sso_rw"`
	UserID           string                `json:"user_id"`
	CFClearance      string                `json:"cf_clearance"`
	UserAgent        string                `json:"user_agent"`
	BaseURL          string                `json:"base_url"`
	Disabled         bool                  `json:"disabled"`
	Status           domain.AccountStatus  `json:"status"`
	ProxyID          string                `json:"proxy_id"`
	Models           []string              `json:"models"`
	Tags             []string              `json:"tags"`
	Priority         int                   `json:"priority"`
	ConcurrencyLimit int                   `json:"concurrency_limit"`
	Quota            domain.QuotaSnapshot  `json:"quota"`
	CooldownUntil    *time.Time            `json:"cooldown_until"`
	LastError        string                `json:"last_error"`
}

type accountPatchRequest struct {
	Name             *string                `json:"name"`
	Kind             *domain.CredentialKind `json:"kind"`
	Tier             *string                `json:"tier"`
	Pool             *string                `json:"pool"`
	Status           *domain.AccountStatus  `json:"status"`
	Email            *string                `json:"email"`
	Credentials      *domain.Credentials    `json:"credentials"`
	AccessToken      *string                `json:"access_token"`
	RefreshToken     *string                `json:"refresh_token"`
	SSO              *string                `json:"sso"`
	SSORW            *string                `json:"sso_rw"`
	UserID           *string                `json:"user_id"`
	CFClearance      *string                `json:"cf_clearance"`
	UserAgent        *string                `json:"user_agent"`
	BaseURL          *string                `json:"base_url"`
	ProxyID          *string                `json:"proxy_id"`
	Models           *[]string              `json:"models"`
	Tags             *[]string              `json:"tags"`
	Priority         *int                   `json:"priority"`
	ConcurrencyLimit *int                   `json:"concurrency_limit"`
	Quota            *domain.QuotaSnapshot  `json:"quota"`
	CooldownUntil    **time.Time            `json:"cooldown_until"`
	LastError        *string                `json:"last_error"`
}

type accountBatchPatchRequest struct {
	IDs              []string              `json:"ids"`
	Tier             *string               `json:"tier"`
	Status           *domain.AccountStatus `json:"status"`
	ProxyID          *string               `json:"proxy_id"`
	Models           *[]string             `json:"models"`
	Tags             *[]string             `json:"tags"`
	Priority         *int                  `json:"priority"`
	ConcurrencyLimit *int                  `json:"concurrency_limit"`
}

func (h *Handler) mountAccounts(router chi.Router) {
	router.Get("/accounts", h.listAccounts)
	router.Post("/accounts", h.createAccount)
	router.Post("/accounts/import", h.importAccounts)
	router.Post("/accounts/export", h.exportAccounts)
	router.Get("/accounts/quota-summary", h.accountQuotaSummary)
	router.Get("/accounts/policy", h.getAccountPolicy)
	router.Put("/accounts/policy", h.updateAccountPolicy)
	router.Patch("/accounts/policy", h.updateAccountPolicy)
	router.Patch("/accounts/batch", h.batchUpdateAccounts)
	router.Post("/accounts/batch-delete", h.batchDeleteAccounts)
	router.Post("/accounts/probe", h.batchProbeAccounts)
	router.Post("/accounts/test", h.batchProbeAccounts)
	router.Post("/accounts/{id}/probe", h.probeAccount)
	router.Post("/accounts/{id}/test", h.probeAccount)
	router.Get("/accounts/{id}", h.getAccount)
	router.Patch("/accounts/{id}", h.updateAccount)
	router.Put("/accounts/{id}", h.updateAccount)
	router.Delete("/accounts/{id}", h.deleteAccount)

	// grok2api-compatible management aliases. Tokens are never returned in plaintext.
	router.Get("/tokens", h.listTokens)
	router.Post("/tokens", h.importAccounts)
	router.Post("/tokens/add", h.importAccounts)
	router.Get("/tokens/page", h.listTokens)
	router.Get("/tokens/summary", h.tokenSummary)
	router.Delete("/tokens", h.deleteTokens)
}

func (h *Handler) batchUpdateAccounts(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request accountBatchPatchRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.IDs = uniqueStrings(request.IDs)
	if len(request.IDs) == 0 || len(request.IDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_account_batch", "Select between 1 and 500 accounts.")
		return
	}
	if request.Status == nil && request.Tier == nil && request.ProxyID == nil && request.Models == nil && request.Tags == nil && request.Priority == nil && request.ConcurrencyLimit == nil {
		writeAPIError(w, http.StatusBadRequest, "empty_account_patch", "At least one account field is required.")
		return
	}
	if request.Status != nil && !apiAccountStatusValid(*request.Status) {
		writeAPIError(w, http.StatusBadRequest, "invalid_account_status", "Account status is invalid.")
		return
	}
	if request.ConcurrencyLimit != nil && *request.ConcurrencyLimit <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_concurrency", "Concurrency limit must be positive.")
		return
	}
	items, err := h.config.Management.BatchUpdateAccounts(r.Context(), admin.BatchUpdateAccountsInput{
		IDs: request.IDs, Tier: request.Tier, Status: request.Status, ProxyID: request.ProxyID,
		Models: request.Models, Tags: request.Tags, Priority: request.Priority,
		ConcurrencyLimit: request.ConcurrencyLimit,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if request.Status != nil && *request.Status == domain.AccountActive && h.config.Accounts != nil {
		for _, accountID := range request.IDs {
			if err := h.config.Accounts.ClearCooldown(r.Context(), accountID); err != nil {
				writeServiceError(w, err)
				return
			}
		}
	}
	if err := h.reloadAccounts(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"updated": len(items), "items": items})
}

func (h *Handler) batchDeleteAccounts(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request struct {
		IDs []string `json:"ids"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.IDs = uniqueStrings(request.IDs)
	if len(request.IDs) == 0 || len(request.IDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_account_batch", "Select between 1 and 500 accounts.")
		return
	}
	deleted, err := h.config.Management.DeleteAccounts(r.Context(), request.IDs)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.syncDeletedAccounts(r.Context(), request.IDs)
	writeData(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (h *Handler) getAccountPolicy(w http.ResponseWriter, _ *http.Request) {
	strategy := accountpool.StrategyAffinity
	if h.config.Accounts != nil {
		strategy = h.config.Accounts.Strategy()
	}
	writeData(w, http.StatusOK, map[string]any{"strategy": strategy})
}

func (h *Handler) updateAccountPolicy(w http.ResponseWriter, r *http.Request) {
	if h.config.Accounts == nil || h.config.Settings == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "account_policy_unavailable", "Account scheduling policy is not configured.")
		return
	}
	var request struct {
		Strategy string `json:"strategy"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	strategy, err := accountpool.ParseStrategy(request.Strategy)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_account_strategy", err.Error())
		return
	}
	if err := h.config.Settings.SaveSettings(r.Context(), map[string]any{"account_scheduling_strategy": strategy}); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := h.config.Accounts.SetStrategy(strategy); err != nil {
		writeServiceError(w, err)
		return
	}
	h.notifyConfiguration(r.Context(), configevent.ScopeAccountStrategy)
	writeData(w, http.StatusOK, map[string]any{"strategy": strategy})
}

type accountQuotaMetricSummary struct {
	State             string     `json:"state"`
	Limit             *int64     `json:"limit"`
	Used              *int64     `json:"used"`
	Remaining         *int64     `json:"remaining"`
	UsagePercent      *float64   `json:"usage_percent"`
	ResetAt           *time.Time `json:"reset_at,omitempty"`
	KnownAccounts     int        `json:"known_accounts"`
	UnknownAccounts   int        `json:"unknown_accounts"`
	UnlimitedAccounts int        `json:"unlimited_accounts"`
	WindowCount       int        `json:"window_count"`
}

func (h *Handler) accountQuotaSummary(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	items, err := h.allAccounts(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	now := time.Now().UTC()
	available := make([]domain.Account, 0, len(items))
	for _, item := range items {
		statusAvailable := item.Status == domain.AccountActive && (item.CooldownUntil == nil || !item.CooldownUntil.After(now)) || item.Status == domain.AccountCooldown && item.CooldownUntil != nil && !item.CooldownUntil.After(now)
		if statusAvailable && !accountpool.QuotaUnavailableUntil(item.Quota, item.Kind, now).After(now) {
			available = append(available, item)
		}
	}
	writeData(w, http.StatusOK, map[string]any{
		"total_accounts":     len(items),
		"available_accounts": len(available),
		"requests":           summarizeQuotaMetric(available, false, now),
		"tokens":             summarizeQuotaMetric(available, true, now),
	})
}

func (h *Handler) allAccounts(ctx context.Context) ([]domain.Account, error) {
	const pageSize = 500
	var result []domain.Account
	for offset := 0; ; offset += pageSize {
		items, err := h.config.Management.ListAccounts(ctx, store.AccountFilter{Pagination: store.Pagination{Limit: pageSize, Offset: offset}})
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if len(items) < pageSize {
			return result, nil
		}
	}
}

func summarizeQuotaMetric(items []domain.Account, tokens bool, now time.Time) accountQuotaMetricSummary {
	result := accountQuotaMetricSummary{State: "unknown"}
	var limit, remaining int64
	windows := make(map[string]*time.Time)
	for _, item := range items {
		quota := item.Quota
		metricLimit, metricRemaining, unlimited := quota.RequestsLimit, quota.RequestsRemaining, quota.RequestsUnlimited
		if tokens {
			metricLimit, metricRemaining, unlimited = quota.TokensLimit, quota.TokensRemaining, quota.TokensUnlimited
		}
		if quota.ResetAt != nil && !quota.ResetAt.After(now) {
			result.UnknownAccounts++
			continue
		}
		if unlimited {
			result.UnlimitedAccounts++
			continue
		}
		if metricLimit <= 0 {
			result.UnknownAccounts++
			continue
		}
		result.KnownAccounts++
		limit += metricLimit
		remaining += min(max(metricRemaining, int64(0)), metricLimit)
		key := "none"
		var reset *time.Time
		if quota.ResetAt != nil {
			value := quota.ResetAt.UTC().Truncate(time.Second)
			key, reset = value.Format(time.RFC3339), &value
		}
		windows[key] = reset
	}
	result.WindowCount = len(windows)
	if result.UnlimitedAccounts > 0 {
		result.State = "unlimited"
		return result
	}
	if result.KnownAccounts == 0 {
		return result
	}
	if result.WindowCount > 1 {
		result.State = "mixed"
		return result
	}
	used := limit - remaining
	percent := float64(0)
	if limit > 0 {
		percent = float64(used) / float64(limit) * 100
	}
	result.Limit, result.Used, result.Remaining, result.UsagePercent = &limit, &used, &remaining, &percent
	for _, reset := range windows {
		result.ResetAt = reset
	}
	if result.UnknownAccounts > 0 {
		result.State = "partial"
	} else {
		result.State = "known"
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func apiAccountStatusValid(status domain.AccountStatus) bool {
	switch status {
	case domain.AccountActive, domain.AccountCooldown, domain.AccountExpired, domain.AccountDisabled, domain.AccountError:
		return true
	default:
		return false
	}
}

func (h *Handler) requireManagement(w http.ResponseWriter) bool {
	if h.config.Management == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "management_unavailable", "Management service is not configured.")
		return false
	}
	return true
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	query := r.URL.Query()
	var proxyID *string
	if query.Has("proxy_id") {
		value := strings.TrimSpace(query.Get("proxy_id"))
		switch strings.ToLower(value) {
		case "direct", "none", "unbound":
			value = ""
		}
		proxyID = &value
	}
	filter := store.AccountFilter{
		Pagination: pagination(r),
		Query:      query.Get("q"),
		Kind:       domain.CredentialKind(query.Get("kind")),
		Status:     domain.AccountStatus(query.Get("status")),
		Tier:       query.Get("tier"),
		ProxyID:    proxyID,
		Model:      query.Get("model"),
	}
	items, err := h.config.Management.ListAccountsWithCredentialExpiry(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	total, err := h.config.Management.CountAccounts(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, filter.Limit, filter.Offset, total))
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	h.listAccounts(w, r)
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	item, err := h.config.Management.GetAccount(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request accountRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	item, err := h.config.Management.CreateAccount(r.Context(), request.createInput())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if err := h.reloadAccounts(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, item)
}

func (h *Handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request accountPatchRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	input, err := h.accountUpdateInput(r.Context(), chi.URLParam(r, "id"), request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	item, err := h.config.Management.UpdateAccount(r.Context(), chi.URLParam(r, "id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if request.Status != nil && *request.Status == domain.AccountActive && h.config.Accounts != nil {
		if err := h.config.Accounts.ClearCooldown(r.Context(), chi.URLParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	if err := h.reloadAccounts(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	accountID := chi.URLParam(r, "id")
	if err := h.config.Management.DeleteAccount(r.Context(), accountID); err != nil {
		writeServiceError(w, err)
		return
	}
	h.syncDeletedAccounts(r.Context(), []string{accountID})
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) == nil {
		*s = splitCredentials(value)
		return nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil {
		return err
	}
	var values []string
	for _, raw := range rawItems {
		if json.Unmarshal(raw, &value) == nil {
			values = append(values, splitCredentials(value)...)
			continue
		}
		var item struct {
			Token string `json:"token"`
			SSO   string `json:"sso"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if token := firstNonEmpty(item.Token, item.SSO); token != "" {
			values = append(values, token)
		}
	}
	*s = values
	return nil
}

func splitCredentials(value string) []string {
	var values []string
	for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if line = strings.TrimSpace(line); line != "" {
			values = append(values, line)
		}
	}
	return values
}

type importCredential struct {
	Token    string
	Tags     []string
	Note     string
	Tier     string
	Status   string
	Disabled bool
}

type importCredentialList []importCredential

func (s *importCredentialList) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) == nil {
		*s = importCredentialsFromText(value)
		return nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil {
		return err
	}
	values := make([]importCredential, 0, len(rawItems))
	for _, raw := range rawItems {
		if json.Unmarshal(raw, &value) == nil {
			values = append(values, importCredentialsFromText(value)...)
			continue
		}
		var item struct {
			Token    string   `json:"token"`
			SSO      string   `json:"sso"`
			Tags     []string `json:"tags"`
			Note     string   `json:"note"`
			Tier     string   `json:"tier"`
			Pool     string   `json:"pool"`
			Status   string   `json:"status"`
			Disabled bool     `json:"disabled"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		token := normalizeImportedCredential(firstNonEmpty(item.Token, item.SSO))
		if token != "" {
			values = append(values, importCredential{
				Token: token, Tags: uniqueStrings(item.Tags), Note: strings.TrimSpace(item.Note),
				Tier: firstNonEmpty(item.Tier, item.Pool), Status: strings.TrimSpace(item.Status), Disabled: item.Disabled,
			})
		}
	}
	*s = values
	return nil
}

func importCredentialsFromText(value string) []importCredential {
	tokens := splitCredentials(value)
	result := make([]importCredential, 0, len(tokens))
	for _, token := range tokens {
		if token = normalizeImportedCredential(token); token != "" {
			result = append(result, importCredential{Token: token})
		}
	}
	return result
}

func normalizeImportedCredential(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\ufeff', '\u200b', '\u200c', '\u200d', '\u2060':
			return -1
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2212':
			return '-'
		default:
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}
	}, value)
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sso=")
	return strings.TrimSpace(value)
}

func normalizeImportTier(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "auto", "basic", "ssobasic":
		return "basic"
	case "super", "ssosuper":
		return "super"
	case "heavy", "ssoheavy":
		return "heavy"
	default:
		return trimmed
	}
}

type importRequest struct {
	Accounts         []accountRequest      `json:"accounts"`
	Tokens           importCredentialList  `json:"tokens"`
	Token            string                `json:"token"`
	Basic            importCredentialList  `json:"basic"`
	SSOBasic         importCredentialList  `json:"ssoBasic"`
	Auto             importCredentialList  `json:"auto"`
	Super            importCredentialList  `json:"super"`
	SSOSuper         importCredentialList  `json:"ssoSuper"`
	Heavy            importCredentialList  `json:"heavy"`
	SSOHeavy         importCredentialList  `json:"ssoHeavy"`
	Kind             domain.CredentialKind `json:"kind"`
	Tier             string                `json:"tier"`
	Pool             string                `json:"pool"`
	Tags             []string              `json:"tags"`
	Priority         int                   `json:"priority"`
	ConcurrencyLimit int                   `json:"concurrency_limit"`
	InitialStatus    domain.AccountStatus  `json:"initial_status"`
	Status           domain.AccountStatus  `json:"status"`
	PostImportAction string                `json:"post_import_action"`
	Total            int                   `json:"total,omitempty"`
	Page             int                   `json:"page,omitempty"`
	PageSize         int                   `json:"page_size,omitempty"`
	TotalPages       int                   `json:"total_pages,omitempty"`
}

type importAccountActionResult struct {
	AccountID string               `json:"account_id"`
	Action    string               `json:"action"`
	Refreshed bool                 `json:"refreshed"`
	Probed    bool                 `json:"probed"`
	Success   bool                 `json:"success"`
	Status    domain.AccountStatus `json:"status,omitempty"`
	Message   string               `json:"message,omitempty"`
	Probe     *accountprobe.Result `json:"probe,omitempty"`
}

type importPostActionResult struct {
	Action    string                      `json:"action"`
	Total     int                         `json:"total"`
	Succeeded int                         `json:"succeeded"`
	Failed    int                         `json:"failed"`
	Items     []importAccountActionResult `json:"items"`
}

type accountImportResult struct {
	Status     string                  `json:"status"`
	Count      int                     `json:"count"`
	Imported   int                     `json:"imported"`
	Skipped    int                     `json:"skipped"`
	Failed     int                     `json:"failed"`
	Items      []domain.Account        `json:"items"`
	Errors     []string                `json:"errors,omitempty"`
	PostAction *importPostActionResult `json:"post_action,omitempty"`
}

type buildOAuthImportRecord struct {
	Type          string          `json:"type"`
	AuthKind      string          `json:"auth_kind"`
	Email         string          `json:"email"`
	Subject       string          `json:"sub"`
	AccessToken   string          `json:"access_token"`
	RefreshToken  string          `json:"refresh_token"`
	IDToken       string          `json:"id_token"`
	TokenType     string          `json:"token_type"`
	ExpiresIn     *int64          `json:"expires_in"`
	ExpiresAt     json.RawMessage `json:"expires_at"`
	Expired       string          `json:"expired"`
	LastRefresh   string          `json:"last_refresh"`
	RedirectURI   string          `json:"redirect_uri"`
	TokenEndpoint string          `json:"token_endpoint"`
	BaseURL       string          `json:"base_url"`
	Disabled      bool            `json:"disabled"`
	CooldownUntil *int64          `json:"cooldown_until"`
	Headers       json.RawMessage `json:"headers"`
}

const (
	xAICLIBaseURL      = "https://cli-chat-proxy.grok.com/v1"
	xAIOfficialBaseURL = "https://api.x.ai/v1"
)

func (h *Handler) importAccounts(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request importRequest
	if err := h.decodeImportRequest(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	applyImportQueryOptions(r, &request)
	initialStatus, err := normalizeImportInitialStatus(firstNonEmpty(string(request.InitialStatus), string(request.Status)))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_initial_status", err.Error())
		return
	}
	postImportAction, err := normalizePostImportAction(request.PostImportAction)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_post_import_action", err.Error())
		return
	}
	if request.Token != "" {
		request.Tokens = append(request.Tokens, importCredentialsFromText(request.Token)...)
	}
	for _, pair := range []struct {
		tier   string
		values importCredentialList
	}{{"basic", request.Basic}, {"basic", request.SSOBasic}, {"basic", request.Auto}, {"super", request.Super}, {"super", request.SSOSuper}, {"heavy", request.Heavy}, {"heavy", request.SSOHeavy}} {
		for _, credential := range pair.values {
			request.Accounts = append(request.Accounts, importedCredentialAccount(credential, request.Kind, pair.tier, request))
		}
	}
	tier := normalizeImportTier(firstNonEmpty(request.Tier, request.Pool, "basic"))
	for _, credential := range request.Tokens {
		request.Accounts = append(request.Accounts, importedCredentialAccount(credential, request.Kind, firstNonEmpty(credential.Tier, tier), request))
	}
	applyImportDefaults(&request, tier)
	if initialStatus != "" {
		applyImportStatusOverride(request.Accounts, initialStatus, time.Now().UTC())
	}
	if len(request.Accounts) == 0 {
		writeAPIError(w, http.StatusBadRequest, "empty_import", "No accounts or tokens were supplied.")
		return
	}
	existing, err := h.allAccounts(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	seenCredentials := make(map[string]struct{}, len(existing)+len(request.Accounts))
	for _, account := range existing {
		if len(account.CredentialFingerprint) > 0 {
			seenCredentials[string(account.CredentialFingerprint)] = struct{}{}
			continue
		}
		credentials, credentialErr := h.config.Management.GetAccountCredentials(r.Context(), account.ID)
		if credentialErr != nil {
			writeServiceError(w, credentialErr)
			return
		}
		if fingerprint := h.config.Management.AccountCredentialFingerprint(account.Kind, credentials); len(fingerprint) > 0 {
			seenCredentials[string(fingerprint)] = struct{}{}
		}
	}
	result := accountImportResult{Status: "success"}
	var importedCLIAccountIDs []string
	for _, item := range request.Accounts {
		input := item.createInput()
		if postImportAction == "refresh_probe" && input.Kind == domain.CredentialCLIOAuth && input.Status != domain.AccountDisabled {
			// Keep unverified credentials outside the scheduler until the probe
			// persists its success feedback.
			input.Status = domain.AccountError
			input.CooldownUntil = nil
			input.LastError = "post-import credential verification is pending"
		}
		fingerprint := h.config.Management.AccountCredentialFingerprint(input.Kind, input.Credentials)
		if len(fingerprint) > 0 {
			if _, duplicate := seenCredentials[string(fingerprint)]; duplicate {
				result.Skipped++
				continue
			}
		}
		created, err := h.config.Management.CreateAccount(r.Context(), input)
		if err != nil {
			if len(fingerprint) > 0 && errors.Is(err, store.ErrConflict) && strings.Contains(err.Error(), "accounts_credential_fingerprint_unique") {
				result.Skipped++
				seenCredentials[string(fingerprint)] = struct{}{}
				continue
			}
			result.Failed++
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		result.Imported++
		result.Count++
		result.Items = append(result.Items, *created)
		if created.Kind == domain.CredentialCLIOAuth && created.Status != domain.AccountDisabled {
			importedCLIAccountIDs = append(importedCLIAccountIDs, created.ID)
		}
		if len(fingerprint) > 0 {
			seenCredentials[string(fingerprint)] = struct{}{}
		}
	}
	if result.Imported > 0 {
		if err := h.reloadAccounts(r.Context()); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	if postImportAction != "none" {
		result.PostAction = h.runImportPostAction(r.Context(), postImportAction, importedCLIAccountIDs)
		readContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), accountImportCleanupTimeout)
		defer cancel()
		actionResults := make(map[string]importAccountActionResult, len(result.PostAction.Items))
		for _, actionResult := range result.PostAction.Items {
			actionResults[actionResult.AccountID] = actionResult
		}
		for index := range result.Items {
			if refreshed, getErr := h.config.Management.GetAccount(readContext, result.Items[index].ID); getErr == nil {
				if actionResult, ok := actionResults[refreshed.ID]; ok && !actionResult.Success && actionResult.Message != "" {
					refreshed.LastError = actionResult.Message
				}
				result.Items[index] = *refreshed
			}
		}
	}
	writeData(w, http.StatusOK, result)
}

func (h *Handler) decodeImportRequest(w http.ResponseWriter, r *http.Request, destination *importRequest) error {
	maximum := h.config.MaxRequestBytes
	if h.config.RuntimeSettings != nil {
		maximum = h.config.RuntimeSettings.Active().MaxRequestBytes
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maximum)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		parsed, err := parseImportData(data)
		if err != nil {
			return err
		}
		*destination = parsed
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	if err := r.ParseMultipartForm(maximum); err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	destination.Kind = domain.CredentialKind(r.FormValue("kind"))
	destination.Tier = r.FormValue("tier")
	destination.Pool = r.FormValue("pool")
	destination.Priority, _ = strconv.Atoi(r.FormValue("priority"))
	destination.ConcurrencyLimit, _ = strconv.Atoi(r.FormValue("concurrency_limit"))
	destination.InitialStatus = domain.AccountStatus(strings.TrimSpace(r.FormValue("initial_status")))
	destination.Status = domain.AccountStatus(strings.TrimSpace(r.FormValue("status")))
	destination.PostImportAction = strings.TrimSpace(r.FormValue("post_import_action"))
	destination.Tags = splitCredentials(r.FormValue("tags"))
	destination.Tokens = append(destination.Tokens, importCredentialsFromText(r.FormValue("tokens"))...)
	if token := strings.TrimSpace(r.FormValue("token")); token != "" {
		destination.Tokens = append(destination.Tokens, importCredentialsFromText(token)...)
	}
	for _, files := range r.MultipartForm.File {
		for _, header := range files {
			file, err := header.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(file, maximum+1))
			_ = file.Close()
			if err != nil {
				return err
			}
			if int64(len(data)) > maximum {
				return errors.New("import file exceeds configured limit")
			}
			part, err := parseImportData(data)
			if err != nil {
				return err
			}
			mergeImportRequest(destination, part)
		}
	}
	return nil
}

func parseImportData(data []byte) (importRequest, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return importRequest{}, nil
	}
	if data[0] == '{' {
		return parseImportObject(data)
	}
	if data[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return importRequest{}, err
		}
		result := importRequest{}
		for _, raw := range items {
			var token string
			if json.Unmarshal(raw, &token) == nil {
				result.Tokens = append(result.Tokens, importCredentialsFromText(token)...)
				continue
			}
			account, err := parseImportAccount(raw)
			if err != nil {
				return importRequest{}, err
			}
			result.Accounts = append(result.Accounts, account)
		}
		return result, nil
	}
	return importRequest{Tokens: importCredentialsFromText(string(data))}, nil
}

func parseImportObject(data []byte) (importRequest, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return importRequest{}, err
	}
	if result, matched, err := parseInteroperableAccountEnvelope(data, object); matched || err != nil {
		return result, err
	}
	if isBuildOAuthImport(object) {
		account, err := parseBuildOAuthAccount(data)
		if err != nil {
			return importRequest{}, err
		}
		var controls struct {
			InitialStatus    domain.AccountStatus `json:"initial_status"`
			Status           domain.AccountStatus `json:"status"`
			PostImportAction string               `json:"post_import_action"`
		}
		if err := json.Unmarshal(data, &controls); err != nil {
			return importRequest{}, err
		}
		return importRequest{
			Accounts:         []accountRequest{account},
			InitialStatus:    controls.InitialStatus,
			Status:           controls.Status,
			PostImportAction: controls.PostImportAction,
		}, nil
	}

	accountsData, hasAccounts := object["accounts"]
	delete(object, "accounts")
	metadata, err := json.Marshal(object)
	if err != nil {
		return importRequest{}, err
	}
	var result importRequest
	if err := strictJSON(metadata, &result); err != nil {
		return importRequest{}, err
	}
	if !hasAccounts {
		return result, nil
	}
	var rawAccounts []json.RawMessage
	if err := json.Unmarshal(accountsData, &rawAccounts); err != nil {
		return importRequest{}, err
	}
	for _, raw := range rawAccounts {
		account, err := parseImportAccount(raw)
		if err != nil {
			return importRequest{}, err
		}
		result.Accounts = append(result.Accounts, account)
	}
	return result, nil
}

func parseImportAccount(data []byte) (accountRequest, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return accountRequest{}, err
	}
	if isBuildOAuthImport(object) {
		return parseBuildOAuthAccount(data)
	}
	if _, compatibleToken := object["token"]; compatibleToken {
		var compat struct {
			Token    string                `json:"token"`
			Pool     string                `json:"pool"`
			Tier     string                `json:"tier"`
			Kind     domain.CredentialKind `json:"kind"`
			Tags     []string              `json:"tags"`
			Note     string                `json:"note"`
			Status   string                `json:"status"`
			Disabled bool                  `json:"disabled"`
		}
		if err := json.Unmarshal(data, &compat); err != nil {
			return accountRequest{}, err
		}
		return importedCredentialAccount(
			importCredential{
				Token: compat.Token, Tags: compat.Tags, Note: compat.Note,
				Status: compat.Status, Disabled: compat.Disabled,
			},
			compat.Kind,
			normalizeImportTier(firstNonEmpty(compat.Tier, compat.Pool, "basic")),
			importRequest{},
		), nil
	}
	var account accountRequest
	if err := strictJSON(data, &account); err != nil {
		return accountRequest{}, err
	}
	return account, nil
}

func isBuildOAuthImport(object map[string]json.RawMessage) bool {
	_, hasAccess := object["access_token"]
	_, hasRefresh := object["refresh_token"]
	_, hasAuthKind := object["auth_kind"]
	_, hasType := object["type"]
	if (hasAccess || hasRefresh) && (hasAuthKind || hasType) {
		return true
	}
	if !hasAccess || !hasRefresh {
		return false
	}
	for _, field := range []string{"kind", "name", "credentials", "tier", "pool"} {
		if _, isGenericAccount := object[field]; isGenericAccount {
			return false
		}
	}
	return true
}

func parseBuildOAuthAccount(data []byte) (accountRequest, error) {
	var record buildOAuthImportRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return accountRequest{}, errors.New("OAuth import record has an invalid JSON schema")
	}
	if value := strings.TrimSpace(record.Type); value != "" && !strings.EqualFold(value, "xai") {
		return accountRequest{}, errors.New("OAuth import record type must be xai")
	}
	if value := strings.TrimSpace(record.AuthKind); value != "" && !strings.EqualFold(value, "oauth") {
		return accountRequest{}, errors.New("OAuth import record authentication kind must be oauth")
	}
	accessToken := strings.TrimSpace(record.AccessToken)
	refreshToken := strings.TrimSpace(record.RefreshToken)
	if accessToken == "" || refreshToken == "" {
		return accountRequest{}, errors.New("OAuth import record requires both access and refresh tokens")
	}
	tokenType := strings.TrimSpace(record.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	} else if !strings.EqualFold(tokenType, "Bearer") {
		return accountRequest{}, errors.New("OAuth import record token type must be Bearer")
	}
	tokenType = "Bearer"
	baseURL, err := normalizeBuildOAuthBaseURL(record.BaseURL)
	if err != nil {
		return accountRequest{}, err
	}
	expiresAt, err := buildOAuthExpiry(record)
	if err != nil {
		return accountRequest{}, err
	}
	email := strings.TrimSpace(record.Email)
	name := email
	if name == "" && strings.TrimSpace(record.Subject) != "" {
		name = "xAI OAuth " + maskedToken(strings.TrimSpace(record.Subject))
	}
	if name == "" {
		name = "Imported xAI OAuth"
	}
	credentials := domain.Credentials{
		AccessToken: accessToken, RefreshToken: refreshToken,
		IDToken: strings.TrimSpace(record.IDToken), TokenType: tokenType,
		ExpiresAt: expiresAt, UserID: strings.TrimSpace(record.Subject), BaseURL: baseURL,
		Email: email,
	}
	status := domain.AccountActive
	var cooldownUntil *time.Time
	if record.CooldownUntil != nil && *record.CooldownUntil > time.Now().Unix() {
		value := time.Unix(*record.CooldownUntil, 0).UTC()
		status, cooldownUntil = domain.AccountCooldown, &value
	}
	if record.Disabled {
		status, cooldownUntil = domain.AccountDisabled, nil
	}
	return accountRequest{Name: name, Kind: domain.CredentialCLIOAuth, Email: email, Credentials: &credentials, Status: status, CooldownUntil: cooldownUntil}, nil
}

func normalizeBuildOAuthBaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return xAICLIBaseURL, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") {
		return "", errors.New("OAuth import record base URL must use an official xAI endpoint")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || !strings.EqualFold(parsed.Host, parsed.Hostname()) {
		return "", errors.New("OAuth import record base URL must use an official xAI endpoint")
	}
	if parsed.Path != "/v1" && parsed.Path != "/v1/" {
		return "", errors.New("OAuth import record base URL must use an official xAI endpoint")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "cli-chat-proxy.grok.com" && host != "api.x.ai" {
		return "", errors.New("OAuth import record base URL must use an official xAI endpoint")
	}
	return xAICLIBaseURL, nil
}

func buildOAuthExpiry(record buildOAuthImportRecord) (time.Time, error) {
	if value := strings.TrimSpace(record.Expired); value != "" {
		expiresAt, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, errors.New("OAuth import record has an invalid expiration time")
		}
		return expiresAt, nil
	}
	if value := bytes.TrimSpace(record.ExpiresAt); len(value) > 0 && !bytes.Equal(value, []byte("null")) {
		var unixSeconds int64
		if err := json.Unmarshal(value, &unixSeconds); err == nil {
			if unixSeconds <= 0 {
				return time.Time{}, errors.New("OAuth import record has an invalid expiration time")
			}
			return time.Unix(unixSeconds, 0).UTC(), nil
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return time.Time{}, errors.New("OAuth import record has an invalid expiration time")
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(text))
		if err != nil {
			return time.Time{}, errors.New("OAuth import record has an invalid expiration time")
		}
		return expiresAt, nil
	}
	if record.ExpiresIn != nil && *record.ExpiresIn > 0 && strings.TrimSpace(record.LastRefresh) != "" {
		if *record.ExpiresIn > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Time{}, errors.New("OAuth import record expiration interval is invalid")
		}
		refreshedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(record.LastRefresh))
		if err != nil {
			return time.Time{}, errors.New("OAuth import record has an invalid refresh time")
		}
		return refreshedAt.Add(time.Duration(*record.ExpiresIn) * time.Second), nil
	}
	return time.Time{}, nil
}

func strictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("import data must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func applyImportDefaults(request *importRequest, tier string) {
	for index := range request.Accounts {
		account := &request.Accounts[index]
		if account.Kind == "" {
			account.Kind = request.Kind
		}
		account.Tier = normalizeImportTier(firstNonEmpty(account.Tier, account.Pool, tier))
		account.Pool = ""
		account.Tags = uniqueStrings(append(account.Tags, request.Tags...))
		if account.Priority == 0 {
			account.Priority = request.Priority
		}
		if account.ConcurrencyLimit == 0 {
			account.ConcurrencyLimit = request.ConcurrencyLimit
		}
	}
}

func mergeImportRequest(destination *importRequest, source importRequest) {
	destination.Accounts = append(destination.Accounts, source.Accounts...)
	destination.Tokens = append(destination.Tokens, source.Tokens...)
	destination.Basic = append(destination.Basic, source.Basic...)
	destination.SSOBasic = append(destination.SSOBasic, source.SSOBasic...)
	destination.Auto = append(destination.Auto, source.Auto...)
	destination.Super = append(destination.Super, source.Super...)
	destination.SSOSuper = append(destination.SSOSuper, source.SSOSuper...)
	destination.Heavy = append(destination.Heavy, source.Heavy...)
	destination.SSOHeavy = append(destination.SSOHeavy, source.SSOHeavy...)
	if destination.Kind == "" {
		destination.Kind = source.Kind
	}
	if destination.Tier == "" {
		destination.Tier = source.Tier
	}
	if destination.Pool == "" {
		destination.Pool = source.Pool
	}
	if len(destination.Tags) == 0 {
		destination.Tags = source.Tags
	}
	if destination.Priority == 0 {
		destination.Priority = source.Priority
	}
	if destination.ConcurrencyLimit == 0 {
		destination.ConcurrencyLimit = source.ConcurrencyLimit
	}
	if destination.InitialStatus == "" {
		destination.InitialStatus = source.InitialStatus
	}
	if destination.Status == "" {
		destination.Status = source.Status
	}
	if destination.PostImportAction == "" {
		destination.PostImportAction = source.PostImportAction
	}
}

func applyImportQueryOptions(r *http.Request, request *importRequest) {
	if r == nil || request == nil {
		return
	}
	query := r.URL.Query()
	if value := strings.TrimSpace(query.Get("initial_status")); value != "" {
		request.InitialStatus = domain.AccountStatus(value)
		request.Status = ""
	} else if value := strings.TrimSpace(query.Get("status")); value != "" {
		request.InitialStatus = domain.AccountStatus(value)
		request.Status = ""
	}
	if value := strings.TrimSpace(query.Get("post_import_action")); value != "" {
		request.PostImportAction = value
	}
}

func normalizeImportInitialStatus(value string) (domain.AccountStatus, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if value == "cooling" {
		value = string(domain.AccountCooldown)
	}
	status := domain.AccountStatus(value)
	if !apiAccountStatusValid(status) {
		return "", errors.New("initial_status must be active, cooldown, expired, disabled, or error")
	}
	return status, nil
}

func normalizePostImportAction(value string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "", "none":
		return "none", nil
	case "refresh", "refresh_probe":
		return normalized, nil
	default:
		return "", errors.New("post_import_action must be none, refresh, or refresh_probe")
	}
}

func applyImportStatusOverride(accounts []accountRequest, status domain.AccountStatus, now time.Time) {
	for index := range accounts {
		accounts[index].Status = status
		accounts[index].Disabled = status == domain.AccountDisabled
		switch status {
		case domain.AccountCooldown:
			if accounts[index].CooldownUntil == nil || !accounts[index].CooldownUntil.After(now) {
				until := now.Add(15 * time.Minute)
				accounts[index].CooldownUntil = &until
			}
		case domain.AccountActive:
			accounts[index].CooldownUntil = nil
			accounts[index].LastError = ""
		default:
			accounts[index].CooldownUntil = nil
		}
	}
}

func (h *Handler) runImportPostAction(parent context.Context, action string, accountIDs []string) *importPostActionResult {
	result := &importPostActionResult{
		Action: action,
		Total:  len(accountIDs),
		Items:  make([]importAccountActionResult, len(accountIDs)),
	}
	for index, accountID := range accountIDs {
		result.Items[index] = importAccountActionResult{AccountID: accountID, Action: action}
	}
	if len(accountIDs) == 0 {
		return result
	}
	if h.config.OAuthRefresh == nil {
		for index := range result.Items {
			result.Items[index].Message = "OAuth credential refresh is not configured."
		}
		h.finalizeImportPostAction(parent, result)
		return result
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), accountImportActionTimeout)
	defer cancel()
	type refreshJob struct {
		index int
		id    string
	}
	jobs := make(chan refreshJob, len(accountIDs))
	for index, accountID := range accountIDs {
		jobs <- refreshJob{index: index, id: accountID}
	}
	close(jobs)
	workers := min(accountImportActionMaxConcurrency, len(accountIDs))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for job := range jobs {
				var err error
				if action == "refresh_probe" {
					_, err = h.config.OAuthRefresh.RefreshAccountForProbeDeferred(ctx, job.id)
				} else {
					_, err = h.config.OAuthRefresh.RefreshAccountDeferred(ctx, job.id)
				}
				if err != nil {
					result.Items[job.index].Message = err.Error()
					continue
				}
				result.Items[job.index].Refreshed = true
				result.Items[job.index].Success = action == "refresh"
				result.Items[job.index].Message = "OAuth credentials refreshed."
			}
		}()
	}
	wait.Wait()

	if err := h.reloadAccounts(ctx); err != nil {
		for index := range result.Items {
			if result.Items[index].Refreshed {
				result.Items[index].Success = false
				result.Items[index].Message = "OAuth credentials refreshed, but the account pool reload failed."
			}
		}
	} else if action == "refresh_probe" && h.config.AccountProbe == nil {
		for index := range result.Items {
			if result.Items[index].Refreshed {
				result.Items[index].Success = false
				result.Items[index].Message = "OAuth credentials refreshed, but account probing is not configured."
			}
		}
	} else if action == "refresh_probe" {
		probeIDs := make([]string, 0, len(accountIDs))
		for _, item := range result.Items {
			if item.Refreshed {
				probeIDs = append(probeIDs, item.AccountID)
			}
		}
		probeResults := h.probeImportedAccounts(ctx, probeIDs)
		probes := make(map[string]accountprobe.Result, len(probeResults))
		for _, probe := range probeResults {
			probes[probe.AccountID] = probe
		}
		for index := range result.Items {
			item := &result.Items[index]
			if !item.Refreshed {
				continue
			}
			probe, ok := probes[item.AccountID]
			if !ok {
				item.Message = "OAuth credentials refreshed, but the account probe did not complete."
				continue
			}
			item.Probed = true
			item.Success = probe.Success
			item.Message = probe.Message
			item.Probe = &probe
			if probe.Account != nil {
				item.Status = probe.Account.Status
			}
		}
	}

	h.finalizeImportPostAction(parent, result)
	return result
}

func (h *Handler) finalizeImportPostAction(parent context.Context, result *importPostActionResult) {
	if result == nil {
		return
	}
	cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(parent), accountImportCleanupTimeout)
	defer cleanupCancel()

	type finalJob struct{ index int }
	jobs := make(chan finalJob, len(result.Items))
	for index := range result.Items {
		jobs <- finalJob{index: index}
	}
	close(jobs)
	workers := min(accountImportActionMaxConcurrency, len(result.Items))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for job := range jobs {
				item := &result.Items[job.index]
				if h.config.Management == nil {
					continue
				}
				account, err := h.config.Management.GetAccount(cleanupContext, item.AccountID)
				if err != nil {
					continue
				}
				item.Status = account.Status
				if item.Success || (result.Action != "refresh_probe" && account.Status != domain.AccountError) {
					continue
				}
				message := strings.Join(strings.Fields(strings.TrimSpace(item.Message)), " ")
				if message == "" {
					message = "Post-import credential verification did not complete; retry the account probe."
				}
				if runes := []rune(message); len(runes) > 500 {
					message = string(runes[:500])
				}
				status := domain.AccountError
				var cooldownUntil *time.Time
				updated, updateErr := h.config.Management.UpdateAccount(cleanupContext, item.AccountID, admin.UpdateAccountInput{
					Status: &status, CooldownUntil: &cooldownUntil, LastError: &message,
				})
				if updateErr == nil {
					item.Status = updated.Status
					if item.Probe != nil {
						probeAccount := *updated
						item.Probe.Account = &probeAccount
					}
					if h.config.Accounts != nil {
						h.config.Accounts.RemoveAccounts([]string{item.AccountID})
						if clearErr := h.config.Accounts.ClearCooldown(cleanupContext, item.AccountID); clearErr != nil {
							slog.Warn("clear coordinated cooldown for failed import probe", "account_id", item.AccountID, "error", clearErr)
						}
					}
				}
			}
		}()
	}
	wait.Wait()
	if err := h.reloadAccounts(cleanupContext); err != nil {
		slog.Warn("reload account pool after import action", "error", err)
	}
	for _, item := range result.Items {
		if item.Success {
			result.Succeeded++
		}
	}
	result.Failed = result.Total - result.Succeeded
}

func (h *Handler) probeImportedAccounts(ctx context.Context, accountIDs []string) []accountprobe.Result {
	results := make([]accountprobe.Result, len(accountIDs))
	if len(accountIDs) == 0 {
		return results
	}
	type probeJob struct {
		index int
		id    string
	}
	jobs := make(chan probeJob, len(accountIDs))
	for index, accountID := range accountIDs {
		jobs <- probeJob{index: index, id: accountID}
	}
	close(jobs)
	workers := min(accountImportActionMaxConcurrency, len(accountIDs))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for job := range jobs {
				probe, err := h.config.AccountProbe.Probe(ctx, job.id, accountprobe.Input{})
				results[job.index] = safeImportProbeResult(job.id, probe, err)
			}
		}()
	}
	wait.Wait()
	return results
}

func safeImportProbeResult(accountID string, probe accountprobe.Result, probeErr error) accountprobe.Result {
	probe.AccountID = accountID
	lowerMessage := strings.ToLower(probe.Message)
	switch {
	case probe.Success:
		probe.Message = "Upstream request completed successfully."
	case probe.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(probe.Message), "protection challenge"):
		probe.Message = "upstream protection challenge returned HTTP 403; check the account proxy and retry"
	case errors.Is(probeErr, context.DeadlineExceeded) || strings.Contains(lowerMessage, "timed out") || strings.Contains(lowerMessage, "deadline exceeded"):
		probe.Message = "Account probe timed out."
	case errors.Is(probeErr, context.Canceled) || strings.Contains(lowerMessage, "canceled"):
		probe.Message = "Account probe was canceled."
	case probe.StatusCode >= http.StatusOK && probe.StatusCode < http.StatusMultipleChoices:
		probe.Message = "Account probe returned an invalid upstream response."
	case probe.StatusCode > 0:
		probe.Message = "Account probe returned HTTP " + strconv.Itoa(probe.StatusCode) + "."
	default:
		probe.Message = "Account probe failed."
	}
	if probe.CompletedAt.IsZero() {
		probe.CompletedAt = time.Now().UTC()
	}
	if probe.Account != nil {
		account := *probe.Account
		if !probe.Success {
			account.LastError = probe.Message
		}
		probe.Account = &account
	}
	return probe
}

func (h *Handler) tokenSummary(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	items, err := h.allAccounts(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	byStatus, byKind := map[string]int{}, map[string]int{}
	for _, item := range items {
		byStatus[string(item.Status)]++
		byKind[string(item.Kind)]++
	}
	writeData(w, http.StatusOK, map[string]any{"total": len(items), "by_status": byStatus, "by_kind": byKind})
}

func (h *Handler) deleteTokens(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request tokenDeleteRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.IDs = append(request.IDs, request.Tokens...)
	items, err := h.allAccounts(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	accountByCredential := make(map[string]string, len(items)*3)
	for _, item := range items {
		credentials, err := h.config.Management.GetAccountCredentials(r.Context(), item.ID)
		if err != nil {
			continue
		}
		for _, credential := range []string{credentials.SSO, credentials.SSORW, credentials.AccessToken} {
			if normalized := normalizeImportedCredential(credential); normalized != "" {
				accountByCredential[normalized] = item.ID
			}
		}
	}
	deleted := 0
	for _, identifier := range request.IDs {
		if h.config.Management.DeleteAccount(r.Context(), identifier) == nil {
			deleted++
			continue
		}
		normalized := normalizeImportedCredential(identifier)
		if accountID, ok := accountByCredential[normalized]; ok {
			if h.config.Management.DeleteAccount(r.Context(), accountID) == nil {
				deleted++
			}
		}
	}
	if deleted > 0 {
		if err := h.reloadAccounts(r.Context()); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": deleted})
}

type tokenDeleteRequest struct {
	IDs    stringList `json:"ids"`
	Tokens stringList `json:"tokens"`
}

func (r *tokenDeleteRequest) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		var identifiers stringList
		if err := json.Unmarshal(data, &identifiers); err != nil {
			return err
		}
		r.Tokens = identifiers
		return nil
	}
	type requestAlias tokenDeleteRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode((*requestAlias)(r))
}

func (r accountRequest) createInput() admin.CreateAccountInput {
	credentials := domain.Credentials{}
	if r.Credentials != nil {
		credentials = *r.Credentials
	}
	mergeCredentials(&credentials, r.AccessToken, r.RefreshToken, r.IDToken, r.SSO, r.SSORW, r.UserID, r.CFClearance, r.UserAgent, r.BaseURL)
	tier := firstNonEmpty(r.Tier, r.Pool)
	status := r.Status
	if status == "" {
		status = domain.AccountActive
	}
	if r.Disabled {
		status = domain.AccountDisabled
	}
	return admin.CreateAccountInput{Name: r.Name, Kind: r.Kind, Tier: tier, Status: status, Email: r.Email, Credentials: credentials, ProxyID: r.ProxyID, Models: r.Models, Tags: r.Tags, Priority: r.Priority, ConcurrencyLimit: r.ConcurrencyLimit, Quota: r.Quota, CooldownUntil: r.CooldownUntil, LastError: r.LastError}
}

func (h *Handler) accountUpdateInput(ctx context.Context, id string, request accountPatchRequest) (admin.UpdateAccountInput, error) {
	input := admin.UpdateAccountInput{Name: request.Name, Kind: request.Kind, Tier: request.Tier, Status: request.Status, Email: request.Email, ProxyID: request.ProxyID, Models: request.Models, Tags: request.Tags, Priority: request.Priority, ConcurrencyLimit: request.ConcurrencyLimit, Quota: request.Quota, CooldownUntil: request.CooldownUntil, LastError: request.LastError}
	if input.Tier == nil {
		input.Tier = request.Pool
	}
	if request.Credentials != nil || request.AccessToken != nil || request.RefreshToken != nil || request.SSO != nil || request.SSORW != nil || request.UserID != nil || request.CFClearance != nil || request.UserAgent != nil || request.BaseURL != nil {
		credentials, err := h.config.Management.GetAccountCredentials(ctx, id)
		if err != nil {
			return input, err
		}
		if request.Credentials != nil {
			credentials = *request.Credentials
		}
		setString(&credentials.AccessToken, request.AccessToken)
		setString(&credentials.RefreshToken, request.RefreshToken)
		setString(&credentials.SSO, request.SSO)
		setString(&credentials.SSORW, request.SSORW)
		setString(&credentials.UserID, request.UserID)
		setString(&credentials.CFClearance, request.CFClearance)
		setString(&credentials.UserAgent, request.UserAgent)
		setString(&credentials.BaseURL, request.BaseURL)
		input.Credentials = &credentials
	}
	return input, nil
}

func importedAccount(token string, kind domain.CredentialKind, tier string, request importRequest) accountRequest {
	if kind == "" {
		kind = domain.CredentialGrokSSO
	}
	token = normalizeImportedCredential(token)
	item := accountRequest{Name: "Imported " + maskedToken(token), Kind: kind, Tier: tier, Tags: request.Tags, Priority: request.Priority, ConcurrencyLimit: request.ConcurrencyLimit}
	if kind == domain.CredentialCLIOAuth {
		item.AccessToken = token
	} else {
		item.SSO, item.SSORW = token, token
	}
	return item
}

func importedCredentialAccount(credential importCredential, kind domain.CredentialKind, tier string, request importRequest) accountRequest {
	item := importedAccount(credential.Token, kind, normalizeImportTier(tier), request)
	status, tags := grok2APIImportMetadata(credential.Status, credential.Tags)
	item.Tags = uniqueStrings(append(item.Tags, tags...))
	if note := strings.TrimSpace(credential.Note); note != "" {
		item.Name = note
	}
	switch status {
	case "cooling", "cooldown":
		until := time.Now().UTC().Add(15 * time.Minute)
		item.Status, item.CooldownUntil = domain.AccountCooldown, &until
	case "disabled":
		item.Status = domain.AccountDisabled
	case "invalid", "error":
		item.Status = domain.AccountError
	case "expired":
		item.Status = domain.AccountExpired
	}
	if credential.Disabled {
		item.Status, item.CooldownUntil = domain.AccountDisabled, nil
	}
	return item
}

func grok2APIImportMetadata(status string, tags []string) (string, []string) {
	status = strings.ToLower(strings.TrimSpace(status))
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		const prefix = "grok-go-status:"
		if strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			candidate := strings.ToLower(strings.TrimSpace(trimmed[len(prefix):]))
			switch candidate {
			case "active", "cooling", "cooldown", "disabled", "invalid", "error", "expired":
				if status == "" {
					status = candidate
				}
				continue
			}
		}
		cleaned = append(cleaned, trimmed)
	}
	return status, uniqueStrings(cleaned)
}

func mergeCredentials(destination *domain.Credentials, access, refresh, idToken, sso, ssoRW, userID, clearance, userAgent, baseURL string) {
	for target, value := range map[*string]string{&destination.AccessToken: access, &destination.RefreshToken: refresh, &destination.IDToken: idToken, &destination.SSO: sso, &destination.SSORW: ssoRW, &destination.UserID: userID, &destination.CFClearance: clearance, &destination.UserAgent: userAgent, &destination.BaseURL: baseURL} {
		if strings.TrimSpace(value) != "" {
			*target = strings.TrimSpace(value)
		}
	}
}

func setString(destination *string, value *string) {
	if value != nil {
		*destination = strings.TrimSpace(*value)
	}
}

func (h *Handler) reloadAccounts(ctx context.Context) error {
	if h.config.Accounts != nil {
		if err := h.config.Accounts.Reload(ctx); err != nil {
			return err
		}
	}
	h.notifyConfiguration(ctx, configevent.ScopeAccounts)
	return nil
}

func (h *Handler) syncDeletedAccounts(ctx context.Context, accountIDs []string) {
	baseContext := context.WithoutCancel(ctx)
	if h.config.Accounts != nil {
		h.config.Accounts.RemoveAccounts(accountIDs)
		reloadContext, cancel := context.WithTimeout(baseContext, accountDeleteSyncTimeout)
		err := h.config.Accounts.Reload(reloadContext)
		cancel()
		if err != nil {
			slog.Warn("account pool reload after deletion failed", "error", err)
		}
	}

	notifyContext, cancel := context.WithTimeout(baseContext, accountDeleteSyncTimeout)
	defer cancel()
	h.notifyConfiguration(notifyContext, configevent.ScopeAccounts)
}

func pagination(r *http.Request) store.Pagination {
	limit := queryInt(r, "limit", queryInt(r, "page_size", 50))
	page := max(1, queryInt(r, "page", 1))
	offset := queryInt(r, "offset", (page-1)*limit)
	return store.Pagination{Limit: min(max(limit, 1), 500), Offset: max(offset, 0)}
}

func listEnvelope[T any](items []T, limit, offset int, totals ...int64) map[string]any {
	if limit <= 0 {
		limit = 50
	}
	total := int64(len(items))
	if len(totals) > 0 {
		total = totals[0]
	}
	return map[string]any{"items": items, "total": total, "page": offset/limit + 1, "page_size": limit}
}

func maskedToken(token string) string {
	if len(token) <= 8 {
		return "credential"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
