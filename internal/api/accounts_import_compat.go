package api

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type grokGoInteropMetadata struct {
	Version          int                   `json:"version"`
	Kind             domain.CredentialKind `json:"kind"`
	Tier             string                `json:"tier"`
	Status           domain.AccountStatus  `json:"status"`
	Email            string                `json:"email"`
	ProxyID          string                `json:"proxy_id"`
	Models           []string              `json:"models"`
	Tags             []string              `json:"tags"`
	Priority         int                   `json:"priority"`
	ConcurrencyLimit int                   `json:"concurrency_limit"`
}

func parseInteroperableAccountEnvelope(data []byte, object map[string]json.RawMessage) (importRequest, bool, error) {
	if wrapped, ok := object["data"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(wrapped, &nested) == nil {
			if result, matched, err := parseInteroperableAccountEnvelope(wrapped, nested); matched || err != nil {
				if err == nil {
					applyWrappedImportControls(&result, object)
				}
				return result, matched, err
			}
		}
	}

	typeName := rawString(object["type"])
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "grok-go-accounts":
		result, err := parseNativeAccountEnvelope(data)
		return result, true, err
	case "sub2api-data", "sub2api-bundle":
		result, err := parseSub2APIAccountEnvelope(data)
		return result, true, err
	}
	_, hasProxies := object["proxies"]
	_, hasAccounts := object["accounts"]
	if hasProxies && hasAccounts {
		result, err := parseSub2APIAccountEnvelope(data)
		return result, true, err
	}
	return importRequest{}, false, nil
}

func applyWrappedImportControls(result *importRequest, object map[string]json.RawMessage) {
	if result == nil {
		return
	}
	if value := rawString(object["initial_status"]); strings.TrimSpace(value) != "" {
		result.InitialStatus = domain.AccountStatus(value)
		result.Status = ""
	} else if value := rawString(object["status"]); strings.TrimSpace(value) != "" {
		result.InitialStatus = domain.AccountStatus(value)
		result.Status = ""
	}
	if value := rawString(object["post_import_action"]); strings.TrimSpace(value) != "" {
		result.PostImportAction = value
	}
}

func parseNativeAccountEnvelope(data []byte) (importRequest, error) {
	var envelope nativeAccountEnvelope
	if err := strictJSON(data, &envelope); err != nil {
		return importRequest{}, errors.New("GROK-GO account backup has an invalid JSON schema")
	}
	if envelope.Type != "grok-go-accounts" || envelope.Version != 1 {
		return importRequest{}, errors.New("GROK-GO account backup version is not supported")
	}
	result := importRequest{Accounts: make([]accountRequest, 0, len(envelope.Accounts))}
	for _, item := range envelope.Accounts {
		if strings.TrimSpace(item.Name) == "" || !validCredentialKind(item.Kind) {
			return importRequest{}, errors.New("GROK-GO account backup contains an invalid account")
		}
		if item.Status == "" {
			item.Status = domain.AccountActive
		}
		if !apiAccountStatusValid(item.Status) {
			return importRequest{}, errors.New("GROK-GO account backup contains an invalid status")
		}
		credentials := item.Credentials
		if item.Kind == domain.CredentialCLIOAuth {
			if err := normalizeImportedOAuthCredentials(&credentials); err != nil {
				return importRequest{}, err
			}
		}
		result.Accounts = append(result.Accounts, accountRequest{
			Name: item.Name, Kind: item.Kind, Tier: item.Tier, Status: item.Status,
			Email: item.Email, Credentials: &credentials, ProxyID: item.ProxyID,
			Models: item.Models, Tags: item.Tags, Priority: item.Priority,
			ConcurrencyLimit: item.ConcurrencyLimit, Quota: item.Quota,
			CooldownUntil: item.CooldownUntil, LastError: item.LastError,
		})
	}
	return result, nil
}

func parseSub2APIAccountEnvelope(data []byte) (importRequest, error) {
	var envelope sub2APIEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return importRequest{}, errors.New("sub2api account backup has an invalid JSON schema")
	}
	if envelope.Type != "" && envelope.Type != "sub2api-data" && envelope.Type != "sub2api-bundle" {
		return importRequest{}, errors.New("sub2api account backup type is not supported")
	}
	if envelope.Version != 0 && envelope.Version != 1 {
		return importRequest{}, errors.New("sub2api account backup version is not supported")
	}
	if envelope.Proxies == nil || envelope.Accounts == nil {
		return importRequest{}, errors.New("sub2api account backup requires proxies and accounts arrays")
	}
	result := importRequest{Accounts: make([]accountRequest, 0, len(envelope.Accounts))}
	for _, item := range envelope.Accounts {
		account, err := parseSub2APIAccount(item)
		if err != nil {
			return importRequest{}, err
		}
		result.Accounts = append(result.Accounts, account)
	}
	return result, nil
}

func parseSub2APIAccount(item sub2APIAccountExport) (accountRequest, error) {
	if !strings.EqualFold(strings.TrimSpace(item.Platform), "grok") && !strings.EqualFold(strings.TrimSpace(item.Platform), "xai") {
		return accountRequest{}, errors.New("sub2api account backup contains a non-Grok platform")
	}
	if !strings.EqualFold(strings.TrimSpace(item.Type), "oauth") {
		return accountRequest{}, errors.New("sub2api Grok account must use OAuth credentials")
	}
	credentials, err := credentialsFromMap(item.Credentials)
	if err != nil {
		return accountRequest{}, err
	}
	if credentials.ExpiresAt.IsZero() && item.ExpiresAt != nil && *item.ExpiresAt > 0 {
		credentials.ExpiresAt = time.Unix(*item.ExpiresAt, 0).UTC()
	}
	if err := normalizeImportedOAuthCredentials(&credentials); err != nil {
		return accountRequest{}, err
	}

	metadata, hasMetadata, err := decodeGrokGoMetadata(item.Extra)
	if err != nil {
		return accountRequest{}, err
	}
	account := accountRequest{
		Name: strings.TrimSpace(item.Name), Kind: domain.CredentialCLIOAuth,
		Tier: "basic", Status: domain.AccountActive, Email: strings.TrimSpace(credentials.Email),
		Credentials: &credentials, ConcurrencyLimit: item.Concurrency,
		Priority: -max(0, item.Priority),
	}
	if account.Name == "" {
		account.Name = firstNonEmpty(account.Email, "Imported sub2api Grok OAuth")
	}
	if hasMetadata {
		if metadata.Version != 0 && metadata.Version != 1 {
			return accountRequest{}, errors.New("sub2api account contains unsupported GROK-GO metadata")
		}
		if metadata.Kind != "" && metadata.Kind != domain.CredentialCLIOAuth {
			return accountRequest{}, errors.New("sub2api account contains an incompatible credential kind")
		}
		if metadata.Status != "" && !apiAccountStatusValid(metadata.Status) {
			return accountRequest{}, errors.New("sub2api account contains an invalid GROK-GO status")
		}
		account.Tier = normalizeImportTier(firstNonEmpty(metadata.Tier, account.Tier))
		if metadata.Status != "" {
			account.Status = metadata.Status
		}
		account.Email = firstNonEmpty(metadata.Email, account.Email)
		account.ProxyID = strings.TrimSpace(metadata.ProxyID)
		account.Models = uniqueStrings(metadata.Models)
		account.Tags = uniqueStrings(metadata.Tags)
		account.Priority = metadata.Priority
		if metadata.ConcurrencyLimit > 0 {
			account.ConcurrencyLimit = metadata.ConcurrencyLimit
		}
	}
	return account, nil
}

func credentialsFromMap(values map[string]any) (domain.Credentials, error) {
	if len(values) == 0 {
		return domain.Credentials{}, errors.New("sub2api Grok account has no credentials")
	}
	copyValues := make(map[string]any, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	expiresAtValue, hasExpiresAt := copyValues["expires_at"]
	delete(copyValues, "expires_at")
	encoded, err := json.Marshal(copyValues)
	if err != nil {
		return domain.Credentials{}, errors.New("sub2api Grok credentials have an invalid schema")
	}
	var credentials domain.Credentials
	if err := json.Unmarshal(encoded, &credentials); err != nil {
		return domain.Credentials{}, errors.New("sub2api Grok credentials have an invalid schema")
	}
	if hasExpiresAt && expiresAtValue != nil {
		expiresAt, err := parseSub2APICredentialExpiry(expiresAtValue)
		if err != nil {
			return domain.Credentials{}, err
		}
		credentials.ExpiresAt = expiresAt
	}
	return credentials, nil
}

func parseSub2APICredentialExpiry(value any) (time.Time, error) {
	invalid := func() (time.Time, error) {
		return time.Time{}, errors.New("sub2api Grok credentials have an invalid expiration time")
	}
	switch value := value.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return time.Time{}, nil
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC(), nil
		}
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds < 0 {
			return invalid()
		}
		return time.Unix(seconds, 0).UTC(), nil
	case json.Number:
		if seconds, err := value.Int64(); err == nil && seconds >= 0 {
			return time.Unix(seconds, 0).UTC(), nil
		}
		parsed, err := value.Float64()
		if err != nil {
			return invalid()
		}
		value = json.Number(strconv.FormatFloat(parsed, 'f', -1, 64))
		return parseSub2APICredentialExpiry(string(value))
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= math.Exp2(63) || math.Trunc(value) != value {
			return invalid()
		}
		return time.Unix(int64(value), 0).UTC(), nil
	case float32:
		return parseSub2APICredentialExpiry(float64(value))
	case int:
		return parseSub2APICredentialExpiry(int64(value))
	case int64:
		if value < 0 {
			return invalid()
		}
		return time.Unix(value, 0).UTC(), nil
	case int32:
		return parseSub2APICredentialExpiry(int64(value))
	case time.Time:
		return value.UTC(), nil
	default:
		return invalid()
	}
}

func decodeGrokGoMetadata(extra map[string]any) (grokGoInteropMetadata, bool, error) {
	value, ok := extra["grok_go"]
	if !ok || value == nil {
		return grokGoInteropMetadata{}, false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return grokGoInteropMetadata{}, false, errors.New("sub2api account contains invalid GROK-GO metadata")
	}
	var metadata grokGoInteropMetadata
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		return grokGoInteropMetadata{}, false, errors.New("sub2api account contains invalid GROK-GO metadata")
	}
	return metadata, true, nil
}

func normalizeImportedOAuthCredentials(credentials *domain.Credentials) error {
	if credentials == nil || strings.TrimSpace(credentials.AccessToken) == "" || strings.TrimSpace(credentials.RefreshToken) == "" {
		return errors.New("OAuth account requires both access and refresh tokens")
	}
	credentials.AccessToken = strings.TrimSpace(credentials.AccessToken)
	credentials.RefreshToken = strings.TrimSpace(credentials.RefreshToken)
	credentials.IDToken = strings.TrimSpace(credentials.IDToken)
	credentials.TokenType = strings.TrimSpace(credentials.TokenType)
	if credentials.TokenType == "" {
		credentials.TokenType = "Bearer"
	} else if !strings.EqualFold(credentials.TokenType, "Bearer") {
		return errors.New("OAuth account token type must be Bearer")
	} else {
		credentials.TokenType = "Bearer"
	}
	baseURL, err := normalizeBuildOAuthBaseURL(credentials.BaseURL)
	if err != nil {
		return err
	}
	credentials.BaseURL = baseURL
	return nil
}

func validCredentialKind(kind domain.CredentialKind) bool {
	switch kind {
	case domain.CredentialCLIOAuth, domain.CredentialConsoleSSO, domain.CredentialGrokSSO:
		return true
	default:
		return false
	}
}

func rawString(value json.RawMessage) string {
	var result string
	_ = json.Unmarshal(value, &result)
	return result
}
