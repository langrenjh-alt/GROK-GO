package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	accountpool "github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
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
	filter := store.AccountFilter{Pagination: pagination(r), Query: r.URL.Query().Get("q"), Kind: domain.CredentialKind(r.URL.Query().Get("kind")), Status: domain.AccountStatus(r.URL.Query().Get("status")), Model: r.URL.Query().Get("model")}
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
	if err := h.config.Management.DeleteAccount(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := h.reloadAccounts(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
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
	result := struct {
		Status   string           `json:"status"`
		Count    int              `json:"count"`
		Imported int              `json:"imported"`
		Skipped  int              `json:"skipped"`
		Failed   int              `json:"failed"`
		Items    []domain.Account `json:"items"`
		Errors   []string         `json:"errors,omitempty"`
	}{Status: "success"}
	for _, item := range request.Accounts {
		input := item.createInput()
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
		return importRequest{Accounts: []accountRequest{account}}, nil
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
