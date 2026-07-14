package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestNativeAccountExportImportRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	cooldown := now.Add(20 * time.Minute)
	items := []exportedAccount{{
		Account: domain.Account{
			Name: "Round trip", Kind: domain.CredentialCLIOAuth, Tier: "heavy",
			Status: domain.AccountCooldown, Email: "roundtrip@example.com",
			Models: []string{"grok-4.5"}, Tags: []string{"one", "two"}, Priority: 90,
			ConcurrencyLimit: 8, CooldownUntil: &cooldown, LastError: "rate limited",
			Quota: domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 0, ResetAt: &cooldown, ObservedAt: now},
		},
		Credentials: domain.Credentials{
			AccessToken: "roundtrip-access", RefreshToken: "roundtrip-refresh",
			TokenType: "Bearer", ExpiresAt: now.Add(3 * time.Hour), BaseURL: xAIOfficialBaseURL,
		},
	}}
	content, _, _, err := buildAccountExport(accountExportNative, items, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseImportData(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 1 {
		t.Fatalf("native round trip count = %d", len(parsed.Accounts))
	}
	account := parsed.Accounts[0]
	if account.Kind != domain.CredentialCLIOAuth || account.Tier != "heavy" || account.Status != domain.AccountCooldown || account.Priority != 90 || account.ConcurrencyLimit != 8 || account.CooldownUntil == nil || !account.CooldownUntil.Equal(cooldown) || account.Credentials == nil || account.Credentials.BaseURL != xAICLIBaseURL || account.Credentials.RefreshToken != "roundtrip-refresh" {
		t.Fatalf("native round trip = %+v", account)
	}
}

func TestSub2APIAccountImportSupportsTaggedAndLegacyEnvelopes(t *testing.T) {
	expiresAt := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	payload := map[string]any{
		"type": "sub2api-data", "version": 1, "exported_at": time.Now().UTC().Format(time.RFC3339),
		"proxies": []any{},
		"accounts": []any{map[string]any{
			"name": "Sub2API Grok", "platform": "grok", "type": "oauth",
			"credentials": map[string]any{
				"access_token": "sub2api-access", "refresh_token": "sub2api-refresh",
				"token_type": "bearer", "expires_at": expiresAt.Format(time.RFC3339),
				"base_url": xAIOfficialBaseURL, "email": "sub2api@example.com",
			},
			"extra": map[string]any{"grok_go": map[string]any{
				"version": 1, "kind": "cli_oauth", "tier": "super", "status": "disabled",
				"models": []string{"grok-4.5"}, "tags": []string{"restored"},
				"priority": 55, "concurrency_limit": 6,
			}},
			"concurrency": 2, "priority": 100, "expires_at": expiresAt.Unix(),
		}},
	}
	content, _ := json.Marshal(payload)
	parsed, err := parseImportData(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 1 {
		t.Fatalf("sub2api account count = %d", len(parsed.Accounts))
	}
	account := parsed.Accounts[0]
	if account.Kind != domain.CredentialCLIOAuth || account.Tier != "super" || account.Status != domain.AccountDisabled || account.Priority != 55 || account.ConcurrencyLimit != 6 || account.Credentials == nil || account.Credentials.BaseURL != xAICLIBaseURL || !account.Credentials.ExpiresAt.Equal(expiresAt) || len(account.Models) != 1 || len(account.Tags) != 1 {
		t.Fatalf("sub2api account = %+v", account)
	}

	delete(payload, "type")
	delete(payload, "version")
	delete(payload, "exported_at")
	legacy, _ := json.Marshal(map[string]any{
		"data": payload, "skip_default_group_bind": true,
		"initial_status": "active", "post_import_action": "refresh_probe",
	})
	if parsed, err = parseImportData(legacy); err != nil || len(parsed.Accounts) != 1 {
		t.Fatalf("wrapped legacy sub2api = %+v, %v", parsed, err)
	}
	if parsed.InitialStatus != domain.AccountActive || parsed.PostImportAction != "refresh_probe" {
		t.Fatalf("wrapped sub2api controls = %+v", parsed)
	}
}

func TestSub2APIAccountImportAcceptsHistoricalCredentialExpirationFormats(t *testing.T) {
	expiresAt := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	for _, expiry := range []any{expiresAt.Unix(), strconv.FormatInt(expiresAt.Unix(), 10)} {
		payload := map[string]any{
			"type": "sub2api-data", "version": 1, "proxies": []any{},
			"accounts": []any{map[string]any{
				"name": "Historical Grok", "platform": "grok", "type": "oauth",
				"credentials": map[string]any{
					"access_token": "historical-access", "refresh_token": "historical-refresh",
					"token_type": "Bearer", "expires_at": expiry,
				},
				"concurrency": 1, "priority": 0,
			}},
		}
		content, _ := json.Marshal(payload)
		parsed, err := parseImportData(content)
		if err != nil {
			t.Fatalf("expiry %v: %v", expiry, err)
		}
		if len(parsed.Accounts) != 1 || parsed.Accounts[0].Credentials == nil || !parsed.Accounts[0].Credentials.ExpiresAt.Equal(expiresAt) {
			t.Fatalf("expiry %v parsed as %+v", expiry, parsed)
		}
	}
}

func TestSub2APIAccountImportRejectsOtherPlatformsAndUnsafeOriginsWithoutLeaks(t *testing.T) {
	const sentinel = "secret-must-not-appear"
	base := map[string]any{
		"exported_at": time.Now().UTC().Format(time.RFC3339), "proxies": []any{},
		"accounts": []any{map[string]any{
			"name": "Account", "platform": "grok", "type": "oauth", "concurrency": 1, "priority": 0,
			"credentials": map[string]any{"access_token": sentinel, "refresh_token": sentinel, "base_url": xAICLIBaseURL},
		}},
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "other platform", mutate: func(account map[string]any) { account["platform"] = "openai" }},
		{name: "custom origin", mutate: func(account map[string]any) {
			account["credentials"].(map[string]any)["base_url"] = "https://example.test/v1"
		}},
		{name: "missing refresh", mutate: func(account map[string]any) { delete(account["credentials"].(map[string]any), "refresh_token") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, _ := json.Marshal(base)
			var fixture map[string]any
			_ = json.Unmarshal(encoded, &fixture)
			account := fixture["accounts"].([]any)[0].(map[string]any)
			test.mutate(account)
			encoded, _ = json.Marshal(fixture)
			_, err := parseImportData(encoded)
			if err == nil || strings.Contains(err.Error(), sentinel) {
				t.Fatalf("sub2api rejection = %v", err)
			}
		})
	}
}

func TestGrok2APITopLevelTokenArrayPreservesAccountState(t *testing.T) {
	parsed, err := parseImportData([]byte(`[
		{"token":"disabled-token","pool":"super","status":"disabled","tags":["one"],"note":"Disabled account"},
		{"token":"flag-disabled-token","pool":"heavy","disabled":true,"tags":["two"]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 2 {
		t.Fatalf("grok2api top-level account count = %d", len(parsed.Accounts))
	}
	first, second := parsed.Accounts[0], parsed.Accounts[1]
	if first.Name != "Disabled account" || first.Tier != "super" || first.Status != domain.AccountDisabled || len(first.Tags) != 1 || first.Tags[0] != "one" {
		t.Fatalf("grok2api status account = %+v", first)
	}
	if second.Tier != "heavy" || second.Status != domain.AccountDisabled || len(second.Tags) != 1 || second.Tags[0] != "two" {
		t.Fatalf("grok2api disabled account = %+v", second)
	}
}

func TestGrok2APIListResponseAcceptsPaginationMetadata(t *testing.T) {
	parsed, err := parseImportData([]byte(`{
		"tokens":[{"token":"paged-token","pool":"basic","status":"active"}],
		"total":1,"page":1,"page_size":20,"total_pages":1
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 0 || len(parsed.Tokens) != 1 || parsed.Tokens[0].Token != "paged-token" {
		t.Fatalf("grok2api paged import = %+v", parsed)
	}
}

func TestCPAImportAcceptsUnixExpiration(t *testing.T) {
	expiresAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	payload, _ := json.Marshal(map[string]any{
		"type": "xai", "auth_kind": "oauth", "access_token": "cpa-access",
		"refresh_token": "cpa-refresh", "expires_at": expiresAt.Unix(),
	})
	parsed, err := parseImportData(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 1 || parsed.Accounts[0].Credentials == nil || !parsed.Accounts[0].Credentials.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("CPA Unix expiration import = %+v", parsed)
	}
}
