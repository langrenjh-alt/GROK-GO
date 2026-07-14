package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const (
	accountExportNative   = "native"
	accountExportSub2API  = "sub2api"
	accountExportGrok2API = "grok2api"
	accountExportCPA      = "cpa"
)

type accountExportRequest struct {
	Format          string   `json:"format"`
	IDs             []string `json:"ids"`
	CurrentPassword string   `json:"current_password"`
	TOTPCode        string   `json:"totp_code"`
}

type exportedAccount struct {
	Account     domain.Account
	Credentials domain.Credentials
}

type nativeAccountExport struct {
	Name             string                `json:"name"`
	Kind             domain.CredentialKind `json:"kind"`
	Tier             string                `json:"tier"`
	Status           domain.AccountStatus  `json:"status"`
	Email            string                `json:"email,omitempty"`
	Credentials      domain.Credentials    `json:"credentials"`
	ProxyID          string                `json:"proxy_id,omitempty"`
	Models           []string              `json:"models,omitempty"`
	Tags             []string              `json:"tags,omitempty"`
	Priority         int                   `json:"priority"`
	ConcurrencyLimit int                   `json:"concurrency_limit"`
	Quota            domain.QuotaSnapshot  `json:"quota"`
	CooldownUntil    *time.Time            `json:"cooldown_until,omitempty"`
	LastError        string                `json:"last_error,omitempty"`
}

type nativeAccountEnvelope struct {
	Type       string                `json:"type"`
	Version    int                   `json:"version"`
	ExportedAt string                `json:"exported_at"`
	Accounts   []nativeAccountExport `json:"accounts"`
}

type sub2APIAccountExport struct {
	Name               string         `json:"name"`
	Platform           string         `json:"platform"`
	Type               string         `json:"type"`
	Credentials        map[string]any `json:"credentials"`
	Extra              map[string]any `json:"extra,omitempty"`
	Concurrency        int            `json:"concurrency"`
	Priority           int            `json:"priority"`
	ExpiresAt          *int64         `json:"expires_at,omitempty"`
	AutoPauseOnExpired bool           `json:"auto_pause_on_expired"`
}

type sub2APIEnvelope struct {
	Type       string                 `json:"type"`
	Version    int                    `json:"version"`
	ExportedAt string                 `json:"exported_at"`
	Proxies    []any                  `json:"proxies"`
	Accounts   []sub2APIAccountExport `json:"accounts"`
}

type grok2APIAccountExport struct {
	Token string   `json:"token"`
	Tags  []string `json:"tags,omitempty"`
	Note  string   `json:"note,omitempty"`
}

type cpaAccountExport struct {
	Type         string `json:"type"`
	AuthKind     string `json:"auth_kind"`
	Email        string `json:"email,omitempty"`
	Subject      string `json:"sub,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    *int64 `json:"expires_in,omitempty"`
	Expired      string `json:"expired,omitempty"`
	BaseURL      string `json:"base_url"`
	Disabled     bool   `json:"disabled"`
}

func (h *Handler) exportAccounts(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	if h.config.Auth == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Administrator authentication is not configured.")
		return
	}
	var request accountExportRequest
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	switch request.Format {
	case accountExportNative, accountExportSub2API, accountExportGrok2API, accountExportCPA:
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_export_format", "Export format must be native, sub2api, grok2api, or cpa.")
		return
	}
	request.IDs = uniqueStrings(request.IDs)
	if len(request.IDs) == 0 || len(request.IDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_account_batch", "Select between 1 and 500 accounts.")
		return
	}
	principal := principalFromContext(r.Context())
	if principal == nil {
		writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Administrator authentication is required.")
		return
	}
	if err := h.config.Auth.VerifyCredentials(r.Context(), principal.ID, request.CurrentPassword, request.TOTPCode); err != nil {
		writeServiceError(w, err)
		return
	}

	items := make([]exportedAccount, 0, len(request.IDs))
	for _, id := range request.IDs {
		account, err := h.config.Management.GetAccount(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		credentials, err := h.config.Management.GetAccountCredentials(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		items = append(items, exportedAccount{Account: *account, Credentials: credentials})
	}

	content, contentType, filename, err := buildAccountExport(request.Format, items, time.Now().UTC())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "account_export_incompatible", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func buildAccountExport(format string, items []exportedAccount, now time.Time) ([]byte, string, string, error) {
	stamp := now.UTC().Format("20060102-150405")
	var payload any
	switch format {
	case accountExportNative:
		accounts := make([]nativeAccountExport, 0, len(items))
		for _, item := range items {
			account := item.Account
			accounts = append(accounts, nativeAccountExport{
				Name: account.Name, Kind: account.Kind, Tier: account.Tier, Status: account.Status,
				Email: account.Email, Credentials: item.Credentials, ProxyID: account.ProxyID,
				Models: account.Models, Tags: account.Tags, Priority: account.Priority,
				ConcurrencyLimit: account.ConcurrencyLimit, Quota: account.Quota,
				CooldownUntil: account.CooldownUntil, LastError: account.LastError,
			})
		}
		payload = nativeAccountEnvelope{Type: "grok-go-accounts", Version: 1, ExportedAt: now.Format(time.RFC3339), Accounts: accounts}
		content, err := marshalExportJSON(payload)
		return content, "application/json; charset=utf-8", "grok-go-accounts-" + stamp + ".json", err

	case accountExportSub2API:
		priorities := sub2APIPriorities(items)
		accounts := make([]sub2APIAccountExport, 0, len(items))
		for _, item := range items {
			if item.Account.Kind != domain.CredentialCLIOAuth {
				return nil, "", "", fmt.Errorf("sub2api export requires CLI OAuth accounts; %q uses %s", item.Account.Name, item.Account.Kind)
			}
			if strings.TrimSpace(item.Credentials.AccessToken) == "" || strings.TrimSpace(item.Credentials.RefreshToken) == "" {
				return nil, "", "", fmt.Errorf("sub2api export requires complete OAuth credentials for %q", item.Account.Name)
			}
			credentials := canonicalOAuthCredentials(item.Credentials)
			accounts = append(accounts, sub2APIAccountExport{
				Name: item.Account.Name, Platform: "grok", Type: "oauth", Credentials: credentialMap(credentials),
				Extra: map[string]any{"grok_go": map[string]any{
					"version": 1, "kind": item.Account.Kind, "tier": item.Account.Tier,
					"status": item.Account.Status, "email": item.Account.Email,
					"proxy_id": item.Account.ProxyID, "models": item.Account.Models,
					"tags": item.Account.Tags, "priority": item.Account.Priority,
					"concurrency_limit": item.Account.ConcurrencyLimit,
				}},
				Concurrency: item.Account.ConcurrencyLimit, Priority: priorities[item.Account.Priority],
				AutoPauseOnExpired: true,
			})
		}
		payload = sub2APIEnvelope{Type: "sub2api-data", Version: 1, ExportedAt: now.Format(time.RFC3339), Proxies: []any{}, Accounts: accounts}
		content, err := marshalExportJSON(payload)
		return content, "application/json; charset=utf-8", "sub2api-grok-accounts-" + stamp + ".json", err

	case accountExportGrok2API:
		pools := map[string][]grok2APIAccountExport{"basic": {}, "super": {}, "heavy": {}}
		for _, item := range items {
			if item.Account.Kind != domain.CredentialGrokSSO {
				return nil, "", "", fmt.Errorf("grok2api export requires Grok SSO accounts; %q uses %s", item.Account.Name, item.Account.Kind)
			}
			token := normalizeImportedCredential(firstNonEmpty(item.Credentials.SSO, item.Credentials.SSORW))
			if token == "" {
				return nil, "", "", fmt.Errorf("grok2api export requires an SSO credential for %q", item.Account.Name)
			}
			pool := normalizeImportTier(item.Account.Tier)
			if _, ok := pools[pool]; !ok {
				return nil, "", "", fmt.Errorf("grok2api export does not support tier %q", item.Account.Tier)
			}
			tags := append([]string(nil), item.Account.Tags...)
			if item.Account.Status != domain.AccountActive {
				tags = append(tags, "grok-go-status:"+string(item.Account.Status))
			}
			pools[pool] = append(pools[pool], grok2APIAccountExport{Token: token, Tags: uniqueStrings(tags), Note: item.Account.Name})
		}
		content, err := marshalExportJSON(pools)
		return content, "application/json; charset=utf-8", "grok2api-tokens-" + stamp + ".json", err

	case accountExportCPA:
		records := make([]cpaAccountExport, 0, len(items))
		for _, item := range items {
			record, err := buildCPAAccountExport(item, now)
			if err != nil {
				return nil, "", "", err
			}
			records = append(records, record)
		}
		if len(records) == 1 {
			content, err := marshalExportJSON(records[0])
			return content, "application/json; charset=utf-8", "xai-" + exportFilenamePart(items[0].Account) + ".json", err
		}
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for index, record := range records {
			entry, err := writer.Create(fmt.Sprintf("xai-%03d-%s.json", index+1, exportFilenamePart(items[index].Account)))
			if err != nil {
				_ = writer.Close()
				return nil, "", "", err
			}
			content, err := marshalExportJSON(record)
			if err != nil {
				_ = writer.Close()
				return nil, "", "", err
			}
			if _, err := entry.Write(content); err != nil {
				_ = writer.Close()
				return nil, "", "", err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", "", err
		}
		return archive.Bytes(), "application/zip", "cpa-xai-accounts-" + stamp + ".zip", nil
	}
	return nil, "", "", errors.New("unsupported account export format")
}

func canonicalOAuthCredentials(credentials domain.Credentials) domain.Credentials {
	credentials.AccessToken = strings.TrimSpace(credentials.AccessToken)
	credentials.RefreshToken = strings.TrimSpace(credentials.RefreshToken)
	credentials.IDToken = strings.TrimSpace(credentials.IDToken)
	credentials.TokenType = "Bearer"
	credentials.BaseURL = xAICLIBaseURL
	return credentials
}

func credentialMap(credentials domain.Credentials) map[string]any {
	result := make(map[string]any)
	encoded, err := json.Marshal(credentials)
	if err == nil {
		_ = json.Unmarshal(encoded, &result)
	}
	if credentials.ExpiresAt.IsZero() {
		delete(result, "expires_at")
	}
	return result
}

func buildCPAAccountExport(item exportedAccount, now time.Time) (cpaAccountExport, error) {
	if item.Account.Kind != domain.CredentialCLIOAuth {
		return cpaAccountExport{}, fmt.Errorf("CPA export requires CLI OAuth accounts; %q uses %s", item.Account.Name, item.Account.Kind)
	}
	credentials := canonicalOAuthCredentials(item.Credentials)
	if credentials.AccessToken == "" || credentials.RefreshToken == "" {
		return cpaAccountExport{}, fmt.Errorf("CPA export requires complete OAuth credentials for %q", item.Account.Name)
	}
	record := cpaAccountExport{
		Type: "xai", AuthKind: "oauth", Email: firstNonEmpty(item.Account.Email, credentials.Email),
		Subject: credentials.UserID, AccessToken: credentials.AccessToken,
		RefreshToken: credentials.RefreshToken, IDToken: credentials.IDToken,
		TokenType: "Bearer", BaseURL: xAICLIBaseURL,
		Disabled: item.Account.Status == domain.AccountDisabled || item.Account.Status == domain.AccountExpired || item.Account.Status == domain.AccountError,
	}
	if !credentials.ExpiresAt.IsZero() {
		record.Expired = credentials.ExpiresAt.UTC().Format(time.RFC3339Nano)
		seconds := int64(credentials.ExpiresAt.Sub(now).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		record.ExpiresIn = &seconds
	}
	return record, nil
}

func sub2APIPriorities(items []exportedAccount) map[int]int {
	values := make([]int, 0, len(items))
	seen := make(map[int]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item.Account.Priority]; ok {
			continue
		}
		seen[item.Account.Priority] = struct{}{}
		values = append(values, item.Account.Priority)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(values)))
	result := make(map[int]int, len(values))
	for index, value := range values {
		result[value] = index * 10
	}
	return result
}

func marshalExportJSON(value any) ([]byte, error) {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func exportFilenamePart(account domain.Account) string {
	value := firstNonEmpty(account.Email, account.Name, account.ID)
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '@':
			return r
		default:
			return '_'
		}
	}, value)
	value = strings.Trim(value, "._-")
	if value == "" {
		value = "account"
	}
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}
