package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestAccountExportRequiresStepUpAndProducesNoStoreNativeBackup(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	expiresAt := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	created, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "OAuth primary", Kind: domain.CredentialCLIOAuth, Tier: "heavy",
		Status: domain.AccountDisabled, Email: "oauth@example.com", Priority: 170,
		ConcurrencyLimit: 7, Models: []string{"grok-4.5"}, Tags: []string{"primary"},
		Credentials: domain.Credentials{
			AccessToken: "export-access-sentinel", RefreshToken: "export-refresh-sentinel",
			IDToken: "export-id-sentinel", TokenType: "bearer", ExpiresAt: expiresAt,
			UserID: "subject-1", BaseURL: xAICLIBaseURL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	wrong := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "native", "ids": []string{created.ID}, "current_password": "wrong password",
	}, environment.cookie, environment.csrf)
	if wrong.Code != http.StatusUnauthorized || strings.Contains(wrong.Body.String(), "export-access-sentinel") {
		t.Fatalf("wrong-password export = %d %s", wrong.Code, wrong.Body.String())
	}

	response := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "native", "ids": []string{created.ID}, "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("native export = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") || !strings.Contains(response.Header().Get("Content-Disposition"), "grok-go-accounts-") {
		t.Fatalf("export headers = %v", response.Header())
	}
	var payload nativeAccountEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != "grok-go-accounts" || payload.Version != 1 || len(payload.Accounts) != 1 {
		t.Fatalf("native envelope = %+v", payload)
	}
	account := payload.Accounts[0]
	if account.Status != domain.AccountDisabled || account.Kind != domain.CredentialCLIOAuth || account.Tier != "heavy" || account.Priority != 170 || account.ConcurrencyLimit != 7 || account.Credentials.AccessToken != "export-access-sentinel" || account.Credentials.RefreshToken != "export-refresh-sentinel" {
		t.Fatalf("native account = %+v", account)
	}
}

func TestAccountExportSub2APIAndCPASchemas(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	first := createExportOAuthAccount(t, environment, "High priority", "high@example.com", 200)
	second := createExportOAuthAccount(t, environment, "Low priority", "low@example.com", 10)

	sub2api := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "sub2api", "ids": []string{first.ID, second.ID}, "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if sub2api.Code != http.StatusOK {
		t.Fatalf("sub2api export = %d %s", sub2api.Code, sub2api.Body.String())
	}
	var data sub2APIEnvelope
	if err := json.Unmarshal(sub2api.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Type != "sub2api-data" || data.Version != 1 || len(data.Proxies) != 0 || len(data.Accounts) != 2 {
		t.Fatalf("sub2api envelope = %+v", data)
	}
	if data.Accounts[0].Platform != "grok" || data.Accounts[0].Type != "oauth" || data.Accounts[0].Priority >= data.Accounts[1].Priority || data.Accounts[0].Credentials["base_url"] != xAICLIBaseURL || data.Accounts[0].Credentials["expires_at"] == nil || data.Accounts[0].ExpiresAt != nil {
		t.Fatalf("sub2api accounts = %+v", data.Accounts)
	}
	if strings.Contains(sub2api.Body.String(), "credential_cipher") || !strings.Contains(sub2api.Body.String(), "refresh-") {
		t.Fatalf("sub2api credential schema = %s", sub2api.Body.String())
	}

	cpa := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "cpa", "ids": []string{first.ID}, "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if cpa.Code != http.StatusOK || !strings.Contains(cpa.Header().Get("Content-Disposition"), "xai-high@example.com.json") {
		t.Fatalf("CPA export = %d %v %s", cpa.Code, cpa.Header(), cpa.Body.String())
	}
	var record cpaAccountExport
	if err := json.Unmarshal(cpa.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Type != "xai" || record.AuthKind != "oauth" || record.TokenType != "Bearer" || record.BaseURL != xAICLIBaseURL || record.AccessToken == "" || record.RefreshToken == "" {
		t.Fatalf("CPA record = %+v", record)
	}

	archive := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "cpa", "ids": []string{first.ID, second.ID}, "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if archive.Code != http.StatusOK || archive.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("CPA archive = %d %v %s", archive.Code, archive.Header(), archive.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Body.Bytes()), int64(archive.Body.Len()))
	if err != nil || len(reader.File) != 2 {
		t.Fatalf("CPA archive files = %+v, %v", reader, err)
	}
	for _, file := range reader.File {
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		content, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if readErr != nil || !json.Valid(content) || !strings.HasSuffix(file.Name, ".json") {
			t.Fatalf("CPA archive entry %q = %s, %v", file.Name, content, readErr)
		}
	}
}

func TestAccountExportGrok2APIAndFormatMismatch(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	sso, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "SSO account", Kind: domain.CredentialGrokSSO, Tier: "super",
		Status: domain.AccountActive, Tags: []string{"imported"}, Priority: 1, ConcurrencyLimit: 2,
		Credentials: domain.Credentials{SSO: "sso-token-sentinel", SSORW: "sso-token-sentinel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	oauth := createExportOAuthAccount(t, environment, "OAuth", "oauth@example.com", 1)

	response := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "grok2api", "ids": []string{sso.ID}, "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("grok2api export = %d %s", response.Code, response.Body.String())
	}
	var pools map[string][]grok2APIAccountExport
	if err := json.Unmarshal(response.Body.Bytes(), &pools); err != nil {
		t.Fatal(err)
	}
	if len(pools["super"]) != 1 || pools["super"][0].Token != "sso-token-sentinel" || pools["super"][0].Note != "SSO account" || len(pools["basic"]) != 0 || len(pools["heavy"]) != 0 {
		t.Fatalf("grok2api pools = %+v", pools)
	}

	disabled := *sso
	disabled.Status = domain.AccountDisabled
	disabled.Tags = []string{"imported"}
	roundTripContent, _, _, err := buildAccountExport(accountExportGrok2API, []exportedAccount{{
		Account: disabled, Credentials: domain.Credentials{SSO: "grok2api-roundtrip-token"},
	}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var roundTripPayload map[string]any
	if err = json.Unmarshal(roundTripContent, &roundTripPayload); err != nil {
		t.Fatal(err)
	}
	roundTripResponse := environment.request(t, http.MethodPost, "/accounts/import", roundTripPayload, environment.cookie, environment.csrf)
	if roundTripResponse.Code != http.StatusOK || !strings.Contains(roundTripResponse.Body.String(), `"imported":1`) {
		t.Fatalf("grok2api status round trip import = %d %s", roundTripResponse.Code, roundTripResponse.Body.String())
	}
	var restored *domain.Account
	for _, account := range environment.repository.accounts {
		credentials, credentialErr := environment.management.GetAccountCredentials(context.Background(), account.ID)
		if credentialErr != nil {
			t.Fatal(credentialErr)
		}
		if credentials.SSO == "grok2api-roundtrip-token" {
			restored = account
			break
		}
	}
	if restored == nil || restored.Status != domain.AccountDisabled || len(restored.Tags) != 1 || restored.Tags[0] != "imported" {
		t.Fatalf("grok2api status round trip = %+v", restored)
	}

	mismatch := environment.request(t, http.MethodPost, "/accounts/export", map[string]any{
		"format": "grok2api", "ids": []string{oauth.ID}, "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if mismatch.Code != http.StatusBadRequest || !strings.Contains(mismatch.Body.String(), "account_export_incompatible") || strings.Contains(mismatch.Body.String(), "access-") || strings.Contains(mismatch.Body.String(), "refresh-") {
		t.Fatalf("incompatible export = %d %s", mismatch.Code, mismatch.Body.String())
	}
}

func createExportOAuthAccount(t *testing.T, environment *testEnvironment, name, email string, priority int) *domain.Account {
	t.Helper()
	created, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: name, Kind: domain.CredentialCLIOAuth, Tier: "basic", Status: domain.AccountActive,
		Email: email, Priority: priority, ConcurrencyLimit: 3, Tags: []string{"oauth"},
		Credentials: domain.Credentials{
			AccessToken: "access-" + email, RefreshToken: "refresh-" + email,
			TokenType: "Bearer", ExpiresAt: time.Now().UTC().Add(3 * time.Hour),
			BaseURL: xAICLIBaseURL, Email: email,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}
