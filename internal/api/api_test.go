package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accountprobe"
	accountpool "github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	mediastore "github.com/langrenjh-alt/GROK-GO/internal/media"
	"github.com/langrenjh-alt/GROK-GO/internal/persistence"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func TestSetupLoginSessionAndCSRF(t *testing.T) {
	environment := newTestEnvironment(t)
	status := environment.request(t, http.MethodGet, "/status", nil, "", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"setup_required":true`) {
		t.Fatalf("unexpected status: %d %s", status.Code, status.Body.String())
	}
	badSetup := environment.request(t, http.MethodPost, "/setup", map[string]any{"username": "admin", "password": "correct horse battery staple", "bootstrap_token": "wrong"}, "", "")
	if badSetup.Code != http.StatusForbidden || len(environment.repository.admins) != 0 {
		t.Fatalf("bootstrap token was not enforced: %d %s", badSetup.Code, badSetup.Body.String())
	}

	setup := environment.request(t, http.MethodPost, "/setup", map[string]any{"username": "admin", "password": "correct horse battery staple", "bootstrap_token": "bootstrap-secret"}, "", "")
	if setup.Code != http.StatusCreated {
		t.Fatalf("unexpected setup: %d %s", setup.Code, setup.Body.String())
	}
	cookie, csrf := environment.login(t)

	me := environment.request(t, http.MethodGet, "/auth/me", nil, cookie, "")
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), "admin@grok-go.local") {
		t.Fatalf("unexpected me response: %d %s", me.Code, me.Body.String())
	}
	missing := environment.request(t, http.MethodPost, "/auth/logout", map[string]any{}, cookie, "")
	if missing.Code != http.StatusForbidden || !strings.Contains(missing.Body.String(), "invalid_csrf") {
		t.Fatalf("expected CSRF rejection: %d %s", missing.Code, missing.Body.String())
	}
	logout := environment.request(t, http.MethodPost, "/auth/logout", map[string]any{}, cookie, csrf)
	if logout.Code != http.StatusOK {
		t.Fatalf("unexpected logout: %d %s", logout.Code, logout.Body.String())
	}
	me = environment.request(t, http.MethodGet, "/auth/me", nil, cookie, "")
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalidated session, got %d", me.Code)
	}
}

func TestAuthenticationStateErrorsUseProtocolStatusAndRetryAfter(t *testing.T) {
	limited := httptest.NewRecorder()
	writeServiceError(limited, &admin.LoginRateLimitError{RetryAfter: 90 * time.Second})
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "90" || !strings.Contains(limited.Body.String(), "login_rate_limited") {
		t.Fatalf("unexpected rate-limit response: %d %v %s", limited.Code, limited.Header(), limited.Body.String())
	}

	unavailable := httptest.NewRecorder()
	writeServiceError(unavailable, fmt.Errorf("%w: Redis offline", admin.ErrAuthStateUnavailable))
	if unavailable.Code != http.StatusServiceUnavailable || !strings.Contains(unavailable.Body.String(), "auth_state_unavailable") {
		t.Fatalf("unexpected unavailable response: %d %s", unavailable.Code, unavailable.Body.String())
	}
}

func TestAdministratorEmailUpdateNormalizesAndRequiresReauthentication(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)

	wrongPassword := environment.request(t, http.MethodPatch, "/auth/email", map[string]any{
		"email": "new-admin@example.com", "current_password": "wrong password",
	}, environment.cookie, environment.csrf)
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("expected current-password rejection: %d %s", wrongPassword.Code, wrongPassword.Body.String())
	}

	changed := environment.request(t, http.MethodPatch, "/auth/email", map[string]any{
		"email": " New-Admin@Example.COM ", "current_password": "correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"email":"new-admin@example.com"`) || !strings.Contains(changed.Body.String(), `"reauthenticate":true`) {
		t.Fatalf("unexpected email update: %d %s", changed.Code, changed.Body.String())
	}
	if me := environment.request(t, http.MethodGet, "/auth/me", nil, environment.cookie, ""); me.Code != http.StatusUnauthorized {
		t.Fatalf("old session remained valid: %d %s", me.Code, me.Body.String())
	}
	oldLogin := environment.request(t, http.MethodPost, "/auth/login", map[string]any{"email": "admin@grok-go.local", "password": "correct horse battery staple"}, "", "")
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old email remained valid: %d %s", oldLogin.Code, oldLogin.Body.String())
	}
	environment.loginWithCredentials(t, "new-admin@example.com", "correct horse battery staple")
}

func TestAdministratorPasswordUpdateRequiresConfirmationAndRevokesSessions(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	mismatch := environment.request(t, http.MethodPost, "/auth/password", map[string]any{
		"current_password": "correct horse battery staple",
		"new_password":     "new correct horse battery staple",
		"confirm_password": "different correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if mismatch.Code != http.StatusBadRequest || !strings.Contains(mismatch.Body.String(), "password_confirmation_mismatch") {
		t.Fatalf("unexpected confirmation response: %d %s", mismatch.Code, mismatch.Body.String())
	}
	weak := environment.request(t, http.MethodPost, "/auth/password", map[string]any{
		"current_password": "correct horse battery staple", "new_password": "too-short", "confirm_password": "too-short",
	}, environment.cookie, environment.csrf)
	if weak.Code != http.StatusBadRequest || !strings.Contains(weak.Body.String(), "at least 12") {
		t.Fatalf("unexpected weak-password response: %d %s", weak.Code, weak.Body.String())
	}
	tooLongPassword := strings.Repeat("x", 4097)
	tooLong := environment.request(t, http.MethodPost, "/auth/password", map[string]any{
		"current_password": "correct horse battery staple", "new_password": tooLongPassword, "confirm_password": tooLongPassword,
	}, environment.cookie, environment.csrf)
	if tooLong.Code != http.StatusBadRequest || !strings.Contains(tooLong.Body.String(), "password_too_long") {
		t.Fatalf("unexpected long-password response: %d %s", tooLong.Code, tooLong.Body.String())
	}
	changed := environment.request(t, http.MethodPost, "/auth/password", map[string]any{
		"current_password": "correct horse battery staple",
		"new_password":     "new correct horse battery staple",
		"confirm_password": "new correct horse battery staple",
	}, environment.cookie, environment.csrf)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"reauthenticate":true`) {
		t.Fatalf("unexpected password update: %d %s", changed.Code, changed.Body.String())
	}
	if me := environment.request(t, http.MethodGet, "/auth/me", nil, environment.cookie, ""); me.Code != http.StatusUnauthorized {
		t.Fatalf("old session remained valid: %d %s", me.Code, me.Body.String())
	}
	oldLogin := environment.request(t, http.MethodPost, "/auth/login", map[string]any{"username": "admin", "password": "correct horse battery staple"}, "", "")
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password remained valid: %d %s", oldLogin.Code, oldLogin.Body.String())
	}
	environment.loginWithCredentials(t, "admin@grok-go.local", "new correct horse battery staple")
}

func TestAccountImportEncryptsCredentials(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	response := environment.request(t, http.MethodPost, "/accounts/import", map[string]any{"kind": "grok_sso", "tier": "basic", "tokens": []string{"sso-secret-one", "sso-secret-two"}, "concurrency_limit": 2}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":2`) {
		t.Fatalf("unexpected import response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "sso-secret") {
		t.Fatalf("plaintext credential leaked in response: %s", response.Body.String())
	}
	if len(environment.repository.accounts) != 2 {
		t.Fatalf("expected two accounts, got %d", len(environment.repository.accounts))
	}
	if !environment.notifier.Has(configevent.ScopeAccounts) {
		t.Fatal("account import notification was not published")
	}
	for _, account := range environment.repository.accounts {
		if len(account.CredentialCipher) == 0 {
			t.Fatalf("credential was not encrypted: %#v", account)
		}
		credentials, err := environment.management.GetAccountCredentials(context.Background(), account.ID)
		if err != nil || !strings.HasPrefix(credentials.SSO, "sso-secret-") {
			t.Fatalf("stored credentials = %#v, %v", credentials, err)
		}
	}
}

func TestGrok2APIImportPreservesLegacyPoolsAndItemMetadata(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	response := environment.request(t, http.MethodPost, "/accounts/import", map[string]any{
		"kind": "grok_sso",
		"tags": []string{"batch", "shared"},
		"ssoBasic": []map[string]any{{
			"token": "\ufeffsso=\u200bbasic-token\u200d",
			"tags":  []string{"auto-register", "shared"},
			"note":  "basic@example.test",
		}},
		"ssoSuper": []map[string]any{{
			"token": "sso=super-token",
			"tags":  []string{"super-only"},
			"note":  "Super account note",
		}},
		"ssoHeavy": []map[string]any{{
			"token": "\u2060heavy-token",
			"tags":  []string{"heavy-only"},
			"note":  "Heavy account note",
		}},
	}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":3`) {
		t.Fatalf("legacy pool import = %d %s", response.Code, response.Body.String())
	}
	for _, token := range []string{"basic-token", "super-token", "heavy-token"} {
		if strings.Contains(response.Body.String(), token) {
			t.Fatalf("legacy pool import exposed a credential: %s", response.Body.String())
		}
	}

	want := map[string]struct {
		tier  string
		token string
		tags  string
	}{
		"basic@example.test": {tier: "basic", token: "basic-token", tags: "auto-register,batch,shared"},
		"Super account note": {tier: "super", token: "super-token", tags: "batch,shared,super-only"},
		"Heavy account note": {tier: "heavy", token: "heavy-token", tags: "batch,heavy-only,shared"},
	}
	if len(environment.repository.accounts) != len(want) {
		t.Fatalf("imported account count = %d", len(environment.repository.accounts))
	}
	for _, account := range environment.repository.accounts {
		expected, ok := want[account.Name]
		if !ok {
			t.Fatalf("legacy note was not preserved as account name: %#v", account)
		}
		credentials, err := environment.management.GetAccountCredentials(context.Background(), account.ID)
		if err != nil {
			t.Fatal(err)
		}
		tags := append([]string(nil), account.Tags...)
		sort.Strings(tags)
		if account.Tier != expected.tier || credentials.SSO != expected.token || credentials.SSORW != expected.token || strings.Join(tags, ",") != expected.tags {
			t.Fatalf("legacy account = tier %q token %q tags %v, want tier %q token %q tags %q", account.Tier, credentials.SSO, tags, expected.tier, expected.token, expected.tags)
		}
	}
}

func TestGrok2APIListExportImportPreservesPerItemPoolAndStatus(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	response := environment.request(t, http.MethodPost, "/accounts/import", map[string]any{
		"kind": "grok_sso",
		"tokens": []map[string]any{
			{"token": "active-super-token", "pool": "super", "status": "active", "tags": []string{"from-list"}},
			{"token": "disabled-heavy-token", "pool": "heavy", "status": "disabled"},
			{"token": "cooling-basic-token", "pool": "basic", "status": "cooling"},
		},
	}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":3`) {
		t.Fatalf("grok2api list export import = %d %s", response.Code, response.Body.String())
	}
	want := map[string]struct {
		tier   string
		status domain.AccountStatus
	}{
		"active-super-token":   {tier: "super", status: domain.AccountActive},
		"disabled-heavy-token": {tier: "heavy", status: domain.AccountDisabled},
		"cooling-basic-token":  {tier: "basic", status: domain.AccountCooldown},
	}
	for _, account := range environment.repository.accounts {
		credentials, err := environment.management.GetAccountCredentials(context.Background(), account.ID)
		if err != nil {
			t.Fatal(err)
		}
		expected, ok := want[credentials.SSO]
		if !ok {
			t.Fatalf("unexpected imported credential for account %#v", account)
		}
		if account.Tier != expected.tier || account.Status != expected.status {
			t.Fatalf("imported %q = tier %q status %q, want tier %q status %q", credentials.SSO, account.Tier, account.Status, expected.tier, expected.status)
		}
		if account.Status == domain.AccountCooldown && (account.CooldownUntil == nil || !account.CooldownUntil.After(time.Now())) {
			t.Fatalf("cooling account has no future cooldown: %#v", account)
		}
	}
}

func TestNormalizeImportTierSupportsGrok2APIAliases(t *testing.T) {
	tests := map[string]string{
		"": "basic", "auto": "basic", "basic": "basic", "ssobasic": "basic", "ssoBasic": "basic",
		"super": "super", "ssoSuper": "super", "heavy": "heavy", "ssoHeavy": "heavy", " custom ": "custom",
	}
	for input, expected := range tests {
		if actual := normalizeImportTier(input); actual != expected {
			t.Errorf("normalizeImportTier(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestGrok2APIAddNormalizesPoolAliases(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	tests := []struct {
		pool string
		want string
	}{
		{pool: "auto", want: "basic"},
		{pool: "ssobasic", want: "basic"},
		{pool: "ssoBasic", want: "basic"},
		{pool: "ssoSuper", want: "super"},
		{pool: "ssoHeavy", want: "heavy"},
	}
	for index, test := range tests {
		token := fmt.Sprintf("pool-alias-token-%d", index)
		response := environment.request(t, http.MethodPost, "/tokens/add", map[string]any{
			"kind": "grok_sso", "pool": test.pool, "tokens": []string{token},
		}, environment.cookie, environment.csrf)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":1`) || strings.Contains(response.Body.String(), token) {
			t.Fatalf("pool alias %q import = %d %s", test.pool, response.Code, response.Body.String())
		}
		found := false
		for _, account := range environment.repository.accounts {
			credentials, err := environment.management.GetAccountCredentials(context.Background(), account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if credentials.SSO == token {
				found = true
				if account.Tier != test.want {
					t.Fatalf("pool alias %q stored tier %q, want %q", test.pool, account.Tier, test.want)
				}
			}
		}
		if !found {
			t.Fatalf("pool alias %q token was not imported", test.pool)
		}
	}
}

func TestGrok2APIImportErrorsDoNotExposeItemTokens(t *testing.T) {
	const sentinel = "pool-item-token-must-stay-secret"
	_, err := parseImportData([]byte(`{"basic":[{"token":"` + sentinel + `","tags":42}]}`))
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("pool item error exposed credential content: %v", err)
	}
}

func TestDeleteTokensAcceptsGrok2APIRawArray(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	created, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "Raw array delete", Kind: domain.CredentialGrokSSO,
		Credentials: domain.Credentials{SSO: "delete-token", SSORW: "delete-token"}, ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := environment.request(t, http.MethodDelete, "/tokens", []string{"\ufeffsso=\u200bdelete-token"}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":1`) {
		t.Fatalf("raw token delete = %d %s", response.Code, response.Body.String())
	}
	if _, err = environment.repository.GetAccount(context.Background(), created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("raw token array did not delete account: %v", err)
	}

	bad := environment.request(t, http.MethodDelete, "/tokens", map[string]any{"tokens": []string{"unused"}, "unexpected": true}, environment.cookie, environment.csrf)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("object delete accepted unknown field: %d %s", bad.Code, bad.Body.String())
	}
}

func TestAccountBatchUpdateEnableDisableAndSchedulingPolicy(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	first, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "First", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "first"}, ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "Second", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "second"}, ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	missingCSRF := environment.request(t, http.MethodPatch, "/accounts/batch", map[string]any{"ids": []string{first.ID}, "status": "disabled"}, environment.cookie, "")
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("batch update without CSRF = %d", missingCSRF.Code)
	}
	missingProxy := environment.request(t, http.MethodPatch, "/accounts/batch", map[string]any{
		"ids": []string{first.ID, second.ID}, "proxy_id": "missing-proxy", "priority": 99,
	}, environment.cookie, environment.csrf)
	if missingProxy.Code != http.StatusNotFound {
		t.Fatalf("batch update with missing proxy = %d %s", missingProxy.Code, missingProxy.Body.String())
	}
	for _, id := range []string{first.ID, second.ID} {
		item, getErr := environment.repository.GetAccount(context.Background(), id)
		if getErr != nil || item.Priority != 0 || item.ProxyID != "" || item.Status != domain.AccountActive {
			t.Fatalf("failed batch left partial update on %s: %+v, %v", id, item, getErr)
		}
	}
	response := environment.request(t, http.MethodPatch, "/accounts/batch", map[string]any{
		"ids": []string{first.ID, second.ID}, "status": "disabled", "priority": 42, "concurrency_limit": 3,
	}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"updated":2`) {
		t.Fatalf("batch update = %d %s", response.Code, response.Body.String())
	}
	for _, id := range []string{first.ID, second.ID} {
		item, getErr := environment.repository.GetAccount(context.Background(), id)
		if getErr != nil || item.Status != domain.AccountDisabled || item.Priority != 42 || item.ConcurrencyLimit != 3 {
			t.Fatalf("updated account %s = %+v, %v", id, item, getErr)
		}
	}
	model := domain.ModelSpec{ID: "grok-test", CredentialKinds: []domain.CredentialKind{domain.CredentialGrokSSO}}
	if _, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model}); !errors.Is(acquireErr, accountpool.ErrNoAccount) {
		t.Fatalf("disabled accounts remained schedulable: %v", acquireErr)
	}
	enabled := environment.request(t, http.MethodPatch, "/accounts/"+first.ID, map[string]any{"status": "active"}, environment.cookie, environment.csrf)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable account = %d %s", enabled.Code, enabled.Body.String())
	}
	leases := make([]*accountpool.Lease, 0, 3)
	for range 3 {
		lease, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model})
		if acquireErr != nil {
			t.Fatalf("updated concurrency was not applied: %v", acquireErr)
		}
		leases = append(leases, lease)
	}
	if _, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model}); !errors.Is(acquireErr, accountpool.ErrNoAccount) {
		t.Fatalf("account exceeded updated concurrency: %v", acquireErr)
	}
	for _, lease := range leases {
		if releaseErr := lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK}); releaseErr != nil {
			t.Fatal(releaseErr)
		}
	}
	secondEnabled := environment.request(t, http.MethodPatch, "/accounts/"+second.ID, map[string]any{"status": "active"}, environment.cookie, environment.csrf)
	if secondEnabled.Code != http.StatusOK {
		t.Fatalf("enable second account = %d %s", secondEnabled.Code, secondEnabled.Body.String())
	}

	policy := environment.request(t, http.MethodPut, "/accounts/policy", map[string]any{"strategy": "round_robin"}, environment.cookie, environment.csrf)
	if policy.Code != http.StatusOK || !strings.Contains(policy.Body.String(), `"strategy":"round_robin"`) {
		t.Fatalf("update policy = %d %s", policy.Code, policy.Body.String())
	}
	loaded := environment.request(t, http.MethodGet, "/accounts/policy", nil, environment.cookie, "")
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), `"round_robin"`) {
		t.Fatalf("get policy = %d %s", loaded.Code, loaded.Body.String())
	}
	settings, err := environment.settings.LoadSettings(context.Background())
	if err != nil || settings["account_scheduling_strategy"] != "round_robin" || environment.accounts.Strategy() != accountpool.StrategyRoundRobin {
		t.Fatalf("policy was not persisted and applied: settings=%v strategy=%s err=%v", settings, environment.accounts.Strategy(), err)
	}
	if !environment.notifier.Has(configevent.ScopeAccountStrategy) {
		t.Fatal("account strategy notification was not published")
	}
	selected := make([]string, 0, 4)
	for range 4 {
		lease, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model})
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		selected = append(selected, lease.Account.ID)
		if releaseErr := lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK}); releaseErr != nil {
			t.Fatal(releaseErr)
		}
	}
	if selected[0] == selected[1] || selected[0] != selected[2] || selected[1] != selected[3] {
		t.Fatalf("round-robin selection = %v", selected)
	}
}

func TestAccountProbeEndpointsRequireCSRFAndReturnRealResults(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	first, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "Probe One", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "first"}, ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "Probe Two", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "second"}, ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	missingCSRF := environment.request(t, http.MethodPost, "/accounts/"+first.ID+"/probe", map[string]any{}, environment.cookie, "")
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("probe without CSRF = %d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	probed := environment.request(t, http.MethodPost, "/accounts/"+first.ID+"/probe", map[string]any{}, environment.cookie, environment.csrf)
	if probed.Code != http.StatusOK || !strings.Contains(probed.Body.String(), `"success":true`) || !strings.Contains(probed.Body.String(), `"status_code":200`) {
		t.Fatalf("single probe = %d %s", probed.Code, probed.Body.String())
	}
	if probed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("single probe cache policy = %q", probed.Header().Get("Cache-Control"))
	}
	if strings.Contains(probed.Body.String(), `"sso"`) || strings.Contains(probed.Body.String(), `"credential_cipher"`) {
		t.Fatalf("probe leaked account credentials: %s", probed.Body.String())
	}
	batch := environment.request(t, http.MethodPost, "/accounts/probe", map[string]any{"ids": []string{first.ID, second.ID}}, environment.cookie, environment.csrf)
	if batch.Code != http.StatusOK || !strings.Contains(batch.Body.String(), `"total":2`) || !strings.Contains(batch.Body.String(), `"succeeded":2`) || !strings.Contains(batch.Body.String(), `"failed":0`) {
		t.Fatalf("batch probe = %d %s", batch.Code, batch.Body.String())
	}
}

func TestAccountQuotaSummaryDistinguishesPartialUnlimitedAndMixedWindows(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	items := []domain.Account{
		{Status: domain.AccountActive, Quota: domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 80, ResetAt: &reset}},
		{Status: domain.AccountActive, Quota: domain.QuotaSnapshot{RequestsLimit: 200, RequestsRemaining: 100, ResetAt: &reset}},
		{Status: domain.AccountActive, Quota: domain.QuotaSnapshot{}},
	}
	summary := summarizeQuotaMetric(items, false, now)
	if summary.State != "partial" || summary.Limit == nil || *summary.Limit != 300 || summary.Used == nil || *summary.Used != 120 || summary.Remaining == nil || *summary.Remaining != 180 || summary.UnknownAccounts != 1 {
		t.Fatalf("partial summary = %+v", summary)
	}
	items = append(items, domain.Account{Status: domain.AccountActive, Quota: domain.QuotaSnapshot{RequestsUnlimited: true}})
	if unlimited := summarizeQuotaMetric(items, false, now); unlimited.State != "unlimited" || unlimited.Limit != nil {
		t.Fatalf("unlimited summary = %+v", unlimited)
	}
	otherReset := reset.Add(time.Hour)
	mixed := summarizeQuotaMetric([]domain.Account{
		{Quota: domain.QuotaSnapshot{RequestsLimit: 10, RequestsRemaining: 5, ResetAt: &reset}},
		{Quota: domain.QuotaSnapshot{RequestsLimit: 10, RequestsRemaining: 5, ResetAt: &otherReset}},
	}, false, now)
	if mixed.State != "mixed" || mixed.WindowCount != 2 || mixed.Limit != nil {
		t.Fatalf("mixed summary = %+v", mixed)
	}
}

func TestAccountAndDashboardSummariesCoverMoreThanOnePage(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	var target *domain.Account
	for index := range 505 {
		priority := 1
		if index == 504 {
			priority = 0
		}
		item, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
			Name: "Bulk account " + fmt.Sprint(index), Kind: domain.CredentialGrokSSO,
			Credentials: domain.Credentials{SSO: "bulk-token-" + fmt.Sprint(index)}, Priority: priority,
		})
		if err != nil {
			t.Fatal(err)
		}
		if index == 504 {
			target = item
		}
	}

	quota := environment.request(t, http.MethodGet, "/accounts/quota-summary", nil, environment.cookie, "")
	if quota.Code != http.StatusOK {
		t.Fatalf("quota summary = %d %s", quota.Code, quota.Body.String())
	}
	var quotaEnvelope struct {
		Data struct {
			Total     int `json:"total_accounts"`
			Available int `json:"available_accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(quota.Body.Bytes(), &quotaEnvelope); err != nil {
		t.Fatal(err)
	}
	if quotaEnvelope.Data.Total != 505 || quotaEnvelope.Data.Available != 505 {
		t.Fatalf("quota summary truncated: %+v", quotaEnvelope.Data)
	}

	tokens := environment.request(t, http.MethodGet, "/tokens/summary", nil, environment.cookie, "")
	if tokens.Code != http.StatusOK {
		t.Fatalf("token summary = %d %s", tokens.Code, tokens.Body.String())
	}
	var tokenEnvelope struct {
		Data struct {
			Total    int            `json:"total"`
			ByStatus map[string]int `json:"by_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokens.Body.Bytes(), &tokenEnvelope); err != nil {
		t.Fatal(err)
	}
	if tokenEnvelope.Data.Total != 505 || tokenEnvelope.Data.ByStatus[string(domain.AccountActive)] != 505 {
		t.Fatalf("token summary truncated: %+v", tokenEnvelope.Data)
	}

	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	environment.repository.mu.Lock()
	for index := range 510 {
		enabled := index < 503 || index >= 506
		var expiresAt *time.Time
		if index >= 506 {
			expiresAt = &expired
		}
		id := fmt.Sprintf("bulk-key-%03d", index)
		environment.repository.keys[id] = &domain.ClientKey{ID: id, Enabled: enabled, ExpiresAt: expiresAt}
	}
	environment.repository.mu.Unlock()
	dashboard := environment.request(t, http.MethodGet, "/dashboard", nil, environment.cookie, "")
	if dashboard.Code != http.StatusOK {
		t.Fatalf("dashboard = %d %s", dashboard.Code, dashboard.Body.String())
	}
	var dashboardEnvelope struct {
		Data struct {
			TotalAccounts  int64 `json:"total_accounts"`
			ActiveAccounts int64 `json:"active_accounts"`
			ActiveKeys     int64 `json:"active_keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(dashboard.Body.Bytes(), &dashboardEnvelope); err != nil {
		t.Fatal(err)
	}
	if dashboardEnvelope.Data.TotalAccounts != 505 || dashboardEnvelope.Data.ActiveAccounts != 505 || dashboardEnvelope.Data.ActiveKeys != 503 {
		t.Fatalf("dashboard counts truncated: %+v", dashboardEnvelope.Data)
	}

	deleted := environment.request(t, http.MethodDelete, "/tokens", map[string]any{"tokens": []string{"bulk-token-504"}}, environment.cookie, environment.csrf)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":1`) {
		t.Fatalf("delete credential after first page = %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := environment.repository.GetAccount(context.Background(), target.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("credential after first page was not deleted: %v", err)
	}
	if !environment.notifier.Has(configevent.ScopeAccounts) {
		t.Fatal("account deletion notification was not published")
	}
}

func TestMultipartImportAcceptsTXTAndGrok2APIRootMap(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("kind", "grok_sso")
	textFile, err := writer.CreateFormFile("files", "tokens.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = textFile.Write([]byte("txt-token-one\ntxt-token-two\n"))
	jsonFile, err := writer.CreateFormFile("files", "accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = jsonFile.Write([]byte(`{"super":[{"token":"json-super-token"}]}`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/accounts/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", environment.csrf)
	request.AddCookie(&http.Cookie{Name: "session", Value: environment.cookie})
	response := httptest.NewRecorder()
	environment.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":3`) {
		t.Fatalf("unexpected multipart import: %d %s", response.Code, response.Body.String())
	}
	foundSuper := false
	for _, account := range environment.repository.accounts {
		if account.Tier == "super" {
			foundSuper = true
		}
	}
	if !foundSuper {
		t.Fatal("grok2api root-map tier was not preserved")
	}
}

func TestBuildOAuthRootJSONImportMapsAccountAndCredentials(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	fixture := readBuildOAuthFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/accounts/import", bytes.NewReader(fixture))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", environment.csrf)
	request.AddCookie(&http.Cookie{Name: "session", Value: environment.cookie})
	response := httptest.NewRecorder()
	environment.handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":1`) {
		t.Fatalf("Build OAuth JSON import = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "fixture-access-token") || strings.Contains(response.Body.String(), "fixture-refresh-token") {
		t.Fatalf("Build OAuth import leaked a credential: %s", response.Body.String())
	}
	if len(environment.repository.accounts) != 1 {
		t.Fatalf("imported account count = %d", len(environment.repository.accounts))
	}
	var account *domain.Account
	for _, item := range environment.repository.accounts {
		account = item
	}
	if account == nil || account.Kind != domain.CredentialCLIOAuth || account.Name != "fixture-account@example.test" || account.Email != "fixture-account@example.test" {
		t.Fatalf("imported account identity = %#v", account)
	}
	if account.Tier != "basic" || account.ConcurrencyLimit != 1 || account.Status != domain.AccountDisabled {
		t.Fatalf("imported account defaults = %#v", account)
	}
	credentials, err := environment.management.GetAccountCredentials(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	if credentials.AccessToken != "fixture-access-token" || credentials.RefreshToken != "fixture-refresh-token" || credentials.IDToken != "fixture-id-token" || credentials.TokenType != "Bearer" {
		t.Fatal("Build OAuth token fields were not preserved")
	}
	if credentials.UserID != "fixture-subject-id" || credentials.Email != "fixture-account@example.test" || credentials.BaseURL != "https://cli-chat-proxy.grok.com/v1" || !credentials.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("Build OAuth metadata was not preserved: user_id=%q email=%q base_url=%q expires_at=%s", credentials.UserID, credentials.Email, credentials.BaseURL, credentials.ExpiresAt)
	}
	listed := environment.request(t, http.MethodGet, "/accounts", nil, environment.cookie, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"credential_expires_at":"2030-01-02T03:04:05Z"`) {
		t.Fatalf("account list omitted credential expiry: %d %s", listed.Code, listed.Body.String())
	}
	for _, secret := range []string{"fixture-access-token", "fixture-refresh-token", "fixture-id-token"} {
		if strings.Contains(listed.Body.String(), secret) {
			t.Fatalf("account list exposed a credential: %s", listed.Body.String())
		}
	}
}

func TestBuildOAuthImportNormalizesCPAEndpointAndIgnoresTransportMetadata(t *testing.T) {
	var fixture map[string]any
	if err := json.Unmarshal(readBuildOAuthFixture(t), &fixture); err != nil {
		t.Fatal(err)
	}
	fixture["base_url"] = xAIOfficialBaseURL
	fixture["token_type"] = "bearer"
	fixture["redirect_uri"] = "https://redirect.example.test/callback"
	fixture["token_endpoint"] = "https://tokens.example.test/oauth/token"
	fixture["headers"] = map[string]any{
		"Authorization":    "Bearer file-controlled-token",
		"Host":             "headers.example.test",
		"X-XAI-Token-Auth": "file-controlled-client",
		"X-Future-Flag":    1,
	}
	fixture["future_cpa_metadata"] = map[string]any{"nested": true}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseImportData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 1 || parsed.Accounts[0].Credentials == nil {
		t.Fatalf("parsed account count = %d", len(parsed.Accounts))
	}
	credentials := *parsed.Accounts[0].Credentials
	if credentials.BaseURL != xAICLIBaseURL || credentials.TokenType != "Bearer" || credentials.UserAgent != "" {
		t.Fatalf("normalized metadata = base_url=%q token_type=%q user_agent=%q", credentials.BaseURL, credentials.TokenType, credentials.UserAgent)
	}

	request := httptest.NewRequest(http.MethodPost, xAICLIBaseURL+"/responses", nil)
	if err = (upstream.CLIOAuthAdapter{}).Apply(request, credentials); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer fixture-access-token" || request.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" || request.Header.Get("Host") != "" {
		t.Fatal("file metadata influenced CLI request headers")
	}
}

func TestBuildOAuthImportRejectsUnsafeSchemasWithoutLeakingSecrets(t *testing.T) {
	const sentinel = "credential-value-must-not-appear"
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		message string
	}{
		{name: "wrong type", mutate: func(value map[string]any) { value["type"] = "codex" }, message: "OAuth import record type must be xai"},
		{name: "API key auth kind", mutate: func(value map[string]any) { value["auth_kind"] = "apikey" }, message: "OAuth import record authentication kind must be oauth"},
		{name: "missing access token", mutate: func(value map[string]any) { delete(value, "access_token") }, message: "OAuth import record requires both access and refresh tokens"},
		{name: "missing refresh token", mutate: func(value map[string]any) { delete(value, "refresh_token") }, message: "OAuth import record requires both access and refresh tokens"},
		{name: "empty refresh token", mutate: func(value map[string]any) { value["refresh_token"] = "  " }, message: "OAuth import record requires both access and refresh tokens"},
		{name: "unsupported token type", mutate: func(value map[string]any) { value["token_type"] = "Basic" }, message: "OAuth import record token type must be Bearer"},
		{name: "control in token type", mutate: func(value map[string]any) { value["token_type"] = "Bearer\r\nX-Test: value" }, message: "OAuth import record token type must be Bearer"},
		{name: "HTTP endpoint", mutate: func(value map[string]any) { value["base_url"] = "http://api.x.ai/v1" }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "custom origin", mutate: func(value map[string]any) { value["base_url"] = "https://upstream.example.test/v1" }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "loopback origin", mutate: func(value map[string]any) { value["base_url"] = "https://127.0.0.1/v1" }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "userinfo", mutate: func(value map[string]any) { value["base_url"] = "https://user@api.x.ai/v1" }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "explicit port", mutate: func(value map[string]any) { value["base_url"] = "https://api.x.ai:443/v1" }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "query", mutate: func(value map[string]any) { value["base_url"] = "https://api.x.ai/v1?target=" + sentinel }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "fragment", mutate: func(value map[string]any) { value["base_url"] = "https://api.x.ai/v1#" + sentinel }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "wrong path", mutate: func(value map[string]any) { value["base_url"] = "https://api.x.ai/v1/responses" }, message: "OAuth import record base URL must use an official xAI endpoint"},
		{name: "encoded path", mutate: func(value map[string]any) { value["base_url"] = "https://api.x.ai/%76%31" }, message: "OAuth import record base URL must use an official xAI endpoint"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var fixture map[string]any
			if err := json.Unmarshal(readBuildOAuthFixture(t), &fixture); err != nil {
				t.Fatal(err)
			}
			fixture["access_token"] = sentinel
			fixture["refresh_token"] = sentinel
			test.mutate(fixture)
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseImportData(data)
			if err == nil || err.Error() != test.message {
				t.Fatalf("parse error = %v, want %q", err, test.message)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("parse error exposed a credential: %v", err)
			}
		})
	}
}

func TestBuildOAuthImportAcceptsLegacyMinimalRecordAndCooldown(t *testing.T) {
	cooldown := time.Now().UTC().Add(10 * time.Minute).Unix()
	parsed, err := parseImportData([]byte(fmt.Sprintf(`{"access_token":"legacy-access","refresh_token":"legacy-refresh","disabled":false,"cooldown_until":%d}`, cooldown)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 1 || parsed.Accounts[0].Credentials == nil || parsed.Accounts[0].Status != domain.AccountCooldown || parsed.Accounts[0].CooldownUntil == nil || parsed.Accounts[0].Credentials.BaseURL != xAICLIBaseURL {
		t.Fatalf("legacy OAuth record = %+v", parsed.Accounts)
	}

	for _, field := range []string{"type", "auth_kind"} {
		var fixture map[string]any
		if err := json.Unmarshal(readBuildOAuthFixture(t), &fixture); err != nil {
			t.Fatal(err)
		}
		delete(fixture, field)
		data, _ := json.Marshal(fixture)
		if _, err := parseImportData(data); err != nil {
			t.Fatalf("optional %s rejected: %v", field, err)
		}
	}
}

func TestNormalizeBuildOAuthBaseURLAcceptsOnlyOfficialEndpoints(t *testing.T) {
	for _, value := range []string{"", xAICLIBaseURL, xAICLIBaseURL + "/", xAIOfficialBaseURL, xAIOfficialBaseURL + "/", "https://API.X.AI/v1"} {
		if got, err := normalizeBuildOAuthBaseURL(value); err != nil || got != xAICLIBaseURL {
			t.Errorf("normalizeBuildOAuthBaseURL(%q) = %q, %v", value, got, err)
		}
	}
}

func TestBuildOAuthDetectionPreservesGenericAccountShape(t *testing.T) {
	data := []byte(`{"name":"Generic OAuth","kind":"cli_oauth","access_token":"test-access","refresh_token":"test-refresh"}`)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if isBuildOAuthImport(object) {
		t.Fatal("generic account object was classified as a CPA OAuth record")
	}
	account, err := parseImportAccount(data)
	if err != nil {
		t.Fatal(err)
	}
	if account.Kind != domain.CredentialCLIOAuth || account.AccessToken == "" || account.RefreshToken == "" {
		t.Fatal("generic account fields were not preserved")
	}
}

func TestParseBuildOAuthArrayAndRedactsErrors(t *testing.T) {
	fixture := readBuildOAuthFixture(t)
	parsed, err := parseImportData(append(append([]byte{'['}, fixture...), ']'))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Accounts) != 1 || parsed.Accounts[0].Kind != domain.CredentialCLIOAuth || parsed.Accounts[0].Credentials == nil || parsed.Accounts[0].Credentials.UserID != "fixture-subject-id" {
		t.Fatalf("Build OAuth array was not parsed: %#v", parsed.Accounts)
	}

	const sentinel = "credential-value-must-not-appear"
	_, err = parseImportData([]byte(`{"type":"xai","auth_kind":"oauth","access_token":"` + sentinel + `","unexpected":"` + sentinel + `"}`))
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("import error exposed credential content: %v", err)
	}
}

func TestManualOAuthRefreshFailureUpdatesPoolAndNotifiesPeers(t *testing.T) {
	const responseSecret = "refresh-response-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + responseSecret + `"}`))
	}))
	defer server.Close()

	environment := newAuthenticatedEnvironment(t)
	created, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "OAuth refresh failure", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive,
		Credentials:      domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(time.Hour)},
		ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	refreshStore := &testOAuthRefreshStore{repository: environment.repository, management: environment.management}
	refreshService := &upstream.RefreshService{
		OAuth: upstream.NewOAuthService(upstream.OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: refreshStore, FailureCooldown: 20 * time.Minute,
	}
	environment.handler = NewAdminHandler(Config{
		Auth: environment.auth, Management: environment.management, AdminRepository: environment.repository,
		Accounts: environment.accounts, OAuthRefresh: refreshService, Settings: environment.settings,
		ConfigChanges: environment.notifier, SessionCookie: "session", CSRFCookie: "csrf", CSRFHeader: "X-CSRF-Token",
	})

	response := environment.request(t, http.MethodPost, "/oauth/refresh/"+created.ID, map[string]any{}, environment.cookie, environment.csrf)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), responseSecret) || !strings.Contains(response.Body.String(), "OAuth credential refresh was rejected") {
		t.Fatalf("manual refresh failure response = %d %s", response.Code, response.Body.String())
	}
	account, err := environment.repository.GetAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != domain.AccountCooldown || account.CooldownUntil == nil || account.LastError == "" || strings.Contains(account.LastError, responseSecret) {
		t.Fatalf("manual refresh failure state = %#v", account)
	}
	if !environment.notifier.Has(configevent.ScopeAccounts) {
		t.Fatal("manual refresh failure did not notify peer instances")
	}
	_, err = environment.accounts.Acquire(context.Background(), accountpool.Selection{
		Model: domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}},
	})
	if !errors.Is(err, accountpool.ErrNoAccount) {
		t.Fatalf("local account pool remained stale after refresh failure: %v", err)
	}
}

func TestManualOAuthRefreshSuccessRestoresAccountAndPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "refresh_token": "new-refresh", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer server.Close()

	environment := newAuthenticatedEnvironment(t)
	created, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: "OAuth refresh recovery", Kind: domain.CredentialCLIOAuth, Status: domain.AccountExpired,
		Credentials:      domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute)},
		ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	lastError := "previous refresh failure"
	if _, err := environment.management.UpdateAccount(context.Background(), created.ID, admin.UpdateAccountInput{LastError: &lastError}); err != nil {
		t.Fatal(err)
	}
	refreshService := &upstream.RefreshService{
		OAuth: upstream.NewOAuthService(upstream.OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: &testOAuthRefreshStore{repository: environment.repository, management: environment.management},
	}
	environment.handler = NewAdminHandler(Config{
		Auth: environment.auth, Management: environment.management, AdminRepository: environment.repository,
		Accounts: environment.accounts, OAuthRefresh: refreshService, Settings: environment.settings,
		ConfigChanges: environment.notifier, SessionCookie: "session", CSRFCookie: "csrf", CSRFHeader: "X-CSRF-Token",
	})

	response := environment.request(t, http.MethodPost, "/oauth/refresh/"+created.ID, map[string]any{}, environment.cookie, environment.csrf)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "new-access") || strings.Contains(response.Body.String(), "new-refresh") {
		t.Fatalf("manual refresh success response = %d %s", response.Code, response.Body.String())
	}
	account, err := environment.repository.GetAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != domain.AccountActive || account.CooldownUntil != nil || account.LastError != "" || account.FailureCount != 0 {
		t.Fatalf("manual refresh recovery state = %#v", account)
	}
	credentials, err := environment.management.GetAccountCredentials(context.Background(), created.ID)
	if err != nil || credentials.AccessToken != "new-access" || credentials.RefreshToken != "new-refresh" || !credentials.ExpiresAt.After(time.Now().Add(50*time.Minute)) {
		t.Fatal("manual refresh did not persist the rotated credentials and expiration")
	}
	if !environment.notifier.Has(configevent.ScopeAccounts) {
		t.Fatal("manual refresh success did not notify peer instances")
	}
	lease, err := environment.accounts.Acquire(context.Background(), accountpool.Selection{
		Model: domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}},
	})
	if err != nil {
		t.Fatalf("local pool did not reactivate refreshed account: %v", err)
	}
	if err := lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK}); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartImportMergesMultipleBuildOAuthFiles(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	fixture := readBuildOAuthFixture(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("tier", "super")
	_ = writer.WriteField("concurrency_limit", "6")
	for index, filename := range []string{"first.json", "second.json"} {
		file, err := writer.CreateFormFile("files", filename)
		if err != nil {
			t.Fatal(err)
		}
		content := fixture
		if index == 1 {
			var second map[string]any
			if err := json.Unmarshal(fixture, &second); err != nil {
				t.Fatal(err)
			}
			second["email"] = "second-account@example.test"
			second["access_token"] = "second-fixture-access-token"
			second["refresh_token"] = "second-fixture-refresh-token"
			content, _ = json.Marshal(second)
		}
		if _, err := file.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/accounts/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", environment.csrf)
	request.AddCookie(&http.Cookie{Name: "session", Value: environment.cookie})
	response := httptest.NewRecorder()
	environment.handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imported":2`) {
		t.Fatalf("multi-file Build OAuth import = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "fixture-access-token") || len(environment.repository.accounts) != 2 {
		t.Fatalf("multi-file import leaked or lost accounts: %s", response.Body.String())
	}
	for _, account := range environment.repository.accounts {
		if account.Kind != domain.CredentialCLIOAuth || account.Tier != "super" || account.ConcurrencyLimit != 6 || account.Status != domain.AccountDisabled {
			t.Fatalf("multi-file import metadata = %#v", account)
		}
	}
}

func TestAccountImportIsIdempotentAcrossExistingAndRepeatedCredentials(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	payload := map[string]any{"kind": "grok_sso", "tier": "basic", "tokens": []string{"duplicate-token", "duplicate-token"}}
	first := environment.request(t, http.MethodPost, "/accounts/import", payload, environment.cookie, environment.csrf)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"imported":1`) || !strings.Contains(first.Body.String(), `"skipped":1`) {
		t.Fatalf("first idempotent import = %d %s", first.Code, first.Body.String())
	}
	second := environment.request(t, http.MethodPost, "/accounts/import", payload, environment.cookie, environment.csrf)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"count":0`) || !strings.Contains(second.Body.String(), `"skipped":2`) || len(environment.repository.accounts) != 1 {
		t.Fatalf("repeated idempotent import = %d %s accounts=%d", second.Code, second.Body.String(), len(environment.repository.accounts))
	}
}

func readBuildOAuthFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/build_oauth_account.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClientKeyCanBeRevealedAndDeleted(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	created := environment.request(t, http.MethodPost, "/keys", map[string]any{"name": "CI", "rpm": 60, "concurrency_limit": 4, "daily_request_limit": 1000, "monthly_token_limit": 10000}, environment.cookie, environment.csrf)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"key":"grok_`) {
		t.Fatalf("unexpected key create response: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data struct {
			Key    string           `json:"key"`
			Record domain.ClientKey `json:"record"`
		} `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &envelope) != nil || envelope.Data.Key == "" {
		t.Fatalf("missing issued key: %s", created.Body.String())
	}
	if envelope.Data.Record.RPM != 60 || envelope.Data.Record.ConcurrencyLimit != 4 || envelope.Data.Record.DailyRequestLimit != 1000 || envelope.Data.Record.MonthlyTokenLimit != 10000 {
		t.Fatalf("explicit key limits were not preserved: %+v", envelope.Data.Record)
	}
	listed := environment.request(t, http.MethodGet, "/keys", nil, environment.cookie, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"prefix":"grok_`) || strings.Contains(listed.Body.String(), envelope.Data.Key) || strings.Contains(listed.Body.String(), "digest") {
		t.Fatalf("unexpected key list response: %d %s", listed.Code, listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), `"secret_available":true`) {
		t.Fatalf("key should report an encrypted recoverable secret: %s", listed.Body.String())
	}
	revealed := environment.request(t, http.MethodPost, "/keys/"+envelope.Data.Record.ID+"/reveal", nil, environment.cookie, environment.csrf)
	if revealed.Code != http.StatusOK || !strings.Contains(revealed.Body.String(), envelope.Data.Key) || revealed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected reveal response: %d %s", revealed.Code, revealed.Body.String())
	}
	deleted := environment.request(t, http.MethodDelete, "/keys/"+envelope.Data.Record.ID, nil, environment.cookie, environment.csrf)
	if deleted.Code != http.StatusOK {
		t.Fatalf("unexpected delete response: %d %s", deleted.Code, deleted.Body.String())
	}
	missing := environment.request(t, http.MethodGet, "/keys/"+envelope.Data.Record.ID, nil, environment.cookie, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted key is still available: %d %s", missing.Code, missing.Body.String())
	}
}

func TestClientKeyDefaultsToUnlimited(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	created := environment.request(t, http.MethodPost, "/keys", map[string]any{"name": "Unlimited"}, environment.cookie, environment.csrf)
	if created.Code != http.StatusCreated {
		t.Fatalf("unexpected key create response: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data struct {
			Record domain.ClientKey `json:"record"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	key := envelope.Data.Record
	if key.RPM != 0 || key.ConcurrencyLimit != 0 || key.DailyRequestLimit != 0 || key.MonthlyTokenLimit != 0 {
		t.Fatalf("default key limits = %+v, want unlimited zero values", key)
	}
}

func TestMediaAdministrationSummaryBatchDeleteAndConfirmedCleanup(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	mediaRoot := t.TempDir()
	fileStore, err := mediastore.NewFileStore(mediaRoot, 1024)
	if err != nil {
		t.Fatal(err)
	}
	mediaAdmin := persistence.MediaAdmin{Store: fileStore}
	environment.handler = NewAdminHandler(Config{
		Auth: environment.auth, Management: environment.management, AdminRepository: environment.repository,
		Accounts: environment.accounts, AccountProbe: environment.probe, Settings: environment.settings, Media: mediaAdmin,
		SessionCookie: "session", CSRFCookie: "csrf", CSRFHeader: "X-CSRF-Token",
	})
	now := time.Now().UTC()
	image, err := fileStore.Put(context.Background(), "image", "image/png", strings.NewReader("image-data"), now.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	video, err := fileStore.Put(context.Background(), "video", "video/mp4", strings.NewReader("video-content"), now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	summary := environment.request(t, http.MethodGet, "/media/summary", nil, environment.cookie, "")
	if summary.Code != http.StatusOK || !strings.Contains(summary.Body.String(), `"total_objects":2`) || !strings.Contains(summary.Body.String(), `"image_objects":1`) || !strings.Contains(summary.Body.String(), `"video_objects":1`) || !strings.Contains(summary.Body.String(), `"expiring_soon_objects":1`) {
		t.Fatalf("media summary = %d %s", summary.Code, summary.Body.String())
	}
	if summary.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("media summary cache policy = %q", summary.Header().Get("Cache-Control"))
	}

	missingCSRF := environment.request(t, http.MethodPost, "/media/batch-delete", map[string]any{"ids": []string{image.ID}}, environment.cookie, "")
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("batch delete without CSRF = %d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	deleted := environment.request(t, http.MethodPost, "/media/batch-delete", map[string]any{"ids": []string{image.ID, "../../outside"}}, environment.cookie, environment.csrf)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"requested":2`) || !strings.Contains(deleted.Body.String(), `"deleted":1`) || !strings.Contains(deleted.Body.String(), `"failed":1`) || strings.Contains(deleted.Body.String(), mediaRoot) {
		t.Fatalf("batch delete = %d %s", deleted.Code, deleted.Body.String())
	}
	if _, reader, err := fileStore.Open(context.Background(), video.ID); err != nil {
		t.Fatalf("batch delete removed unselected video: %v", err)
	} else {
		_ = reader.Close()
	}

	unconfirmed := environment.request(t, http.MethodPost, "/media/cleanup", map[string]any{"mode": "all"}, environment.cookie, environment.csrf)
	if unconfirmed.Code != http.StatusBadRequest || !strings.Contains(unconfirmed.Body.String(), "cleanup_confirmation_required") {
		t.Fatalf("unconfirmed clear = %d %s", unconfirmed.Code, unconfirmed.Body.String())
	}
	cleared := environment.request(t, http.MethodPost, "/media/cleanup", map[string]any{"mode": "all", "confirm": true}, environment.cookie, environment.csrf)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"deleted":1`) || !strings.Contains(cleared.Body.String(), `"deleted_bytes":13`) {
		t.Fatalf("confirmed clear = %d %s", cleared.Code, cleared.Body.String())
	}
}

func TestTrustedOriginValidation(t *testing.T) {
	handler := &Handler{config: Config{TrustedOrigin: "https://grok.example.test"}}
	request := httptest.NewRequest(http.MethodPost, "/settings", nil)
	request.Header.Set("Origin", "https://grok.example.test")
	if !handler.originAllowed(request) {
		t.Fatal("expected configured origin to be accepted")
	}
	request.Header.Set("Origin", "https://other.example.test")
	if handler.originAllowed(request) {
		t.Fatal("expected cross-origin request to be rejected")
	}
	request.Header.Del("Origin")
	request.Header.Set("Referer", "https://grok.example.test/settings/")
	if !handler.originAllowed(request) {
		t.Fatal("expected same-origin referer to be accepted")
	}
}

func TestClientIPRequiresExplicitProxyTrust(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	request.RemoteAddr = "192.0.2.10:54321"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	if got := (&Handler{}).clientIP(request); got != "192.0.2.10" {
		t.Fatalf("untrusted client IP = %q", got)
	}
	if got := (&Handler{config: Config{TrustProxyHeaders: true}}).clientIP(request); got != "198.51.100.20" {
		t.Fatalf("trusted proxy client IP = %q", got)
	}
}

func TestDashboardCacheMetricsUseSuccessfulConversationalUsageOnly(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	now := time.Now().UTC()
	logs := []domain.RequestLog{
		{RequestID: "cache-partial", Endpoint: "/v1/chat/completions", StatusCode: http.StatusOK, DurationMS: 100, InputTokens: 100, OutputTokens: 20, CachedTokens: 40, UsageParsed: true, Metadata: json.RawMessage(`{"cache_identity_applied":true,"cache_affinity_reused":true}`), CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "cache-rate-limited", Endpoint: "/v1/responses", StatusCode: http.StatusTooManyRequests, DurationMS: 300, InputTokens: 300, OutputTokens: 30, CachedTokens: 180, UsageParsed: true, CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "missing-usage", Endpoint: "/v1/responses", StatusCode: http.StatusOK, DurationMS: 500, CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "parsed-zero-usage", Endpoint: "/v1/messages", StatusCode: http.StatusOK, DurationMS: 80, UsageParsed: true, CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "cache-over-report", Endpoint: "/v1/messages", StatusCode: http.StatusOK, DurationMS: 100, InputTokens: 100, CachedTokens: 250, UsageParsed: true, CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "non-cache-media", Endpoint: "/v1/images/generations", StatusCode: http.StatusOK, DurationMS: 200, InputTokens: 400, CachedTokens: 300, UsageParsed: true, CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "cache-miss", Endpoint: "/v1/responses", StatusCode: http.StatusOK, DurationMS: 120, InputTokens: 200, OutputTokens: 10, UsageParsed: true, Metadata: json.RawMessage(`{"cache_identity_applied":true,"cache_affinity_reused":true}`), CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "cache-warmup", Endpoint: "/v1/responses", StatusCode: http.StatusOK, DurationMS: 50, InputTokens: 50, OutputTokens: 5, UsageParsed: true, Metadata: json.RawMessage(`{"cache_identity_applied":true,"cache_affinity_reused":false,"cache_affinity_established":true}`), CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "outside-window", Endpoint: "/v1/chat/completions", StatusCode: http.StatusOK, DurationMS: 10, InputTokens: 100, OutputTokens: 10, CachedTokens: 100, UsageParsed: true, CreatedAt: now.Add(-25 * time.Hour)},
	}
	for index := range logs {
		if err := environment.management.CreateRequestLog(context.Background(), &logs[index]); err != nil {
			t.Fatal(err)
		}
	}

	response := environment.request(t, http.MethodGet, "/dashboard", nil, environment.cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard response = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Requests                    int64     `json:"requests_24h"`
			SuccessRate                 float64   `json:"success_rate"`
			AverageLatency              int64     `json:"avg_latency_ms"`
			Tokens                      int64     `json:"tokens_24h"`
			InputTokens                 int64     `json:"input_tokens_24h"`
			CachedTokens                int64     `json:"cached_tokens_24h"`
			UsageSamples                int64     `json:"usage_samples_24h"`
			CacheSamples                int64     `json:"cache_samples_24h"`
			CacheRequestHits            int64     `json:"cache_request_hits_24h"`
			CacheWarmupCandidates       int64     `json:"cache_warmup_candidates_24h"`
			CacheAffinityReuses         int64     `json:"cache_affinity_reuses_24h"`
			CacheAffinityMisses         int64     `json:"cache_affinity_misses_24h"`
			CacheEligibleRequests       int64     `json:"cache_eligible_requests_24h"`
			CacheHitRate                float64   `json:"cache_hit_rate"`
			CacheTokenReuseRate         float64   `json:"cache_token_reuse_rate"`
			CacheRequestHitRate         float64   `json:"cache_request_hit_rate"`
			CacheUsageCoverage          float64   `json:"cache_usage_coverage"`
			CacheAffinityMissRate       float64   `json:"cache_affinity_miss_rate"`
			HourlyRequests              []int64   `json:"hourly_requests"`
			HourlyCacheEligibleRequests []int64   `json:"hourly_cache_eligible_requests"`
			HourlyInputTokens           []int64   `json:"hourly_input_tokens"`
			HourlyCachedTokens          []int64   `json:"hourly_cached_tokens"`
			HourlyUsageSamples          []int64   `json:"hourly_usage_samples"`
			HourlyCacheSamples          []int64   `json:"hourly_cache_samples"`
			HourlyCacheRequestHits      []int64   `json:"hourly_cache_request_hits"`
			HourlyCacheWarmupCandidates []int64   `json:"hourly_cache_warmup_candidates"`
			HourlyCacheAffinityReuses   []int64   `json:"hourly_cache_affinity_reuses"`
			HourlyCacheAffinityMisses   []int64   `json:"hourly_cache_affinity_misses"`
			HourlyCacheTokenReuseRates  []float64 `json:"hourly_cache_token_reuse_rate"`
			HourlyCacheRequestHitRates  []float64 `json:"hourly_cache_request_hit_rate"`
			HourlyCacheUsageCoverage    []float64 `json:"hourly_cache_usage_coverage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope.Data
	if data.Requests != 8 || data.SuccessRate != float64(7)*100/8 || data.AverageLatency != 181 || data.Tokens != 1215 {
		t.Fatalf("request summary = %+v", data)
	}
	if data.InputTokens != 450 || data.CachedTokens != 140 || data.UsageSamples != 5 || data.CacheSamples != 4 || data.CacheRequestHits != 2 || data.CacheWarmupCandidates != 1 || data.CacheAffinityReuses != 2 || data.CacheAffinityMisses != 1 || data.CacheEligibleRequests != 6 {
		t.Fatalf("cache summary = %+v", data)
	}
	if data.CacheHitRate != float64(140)*100/450 || data.CacheTokenReuseRate != float64(140)*100/450 || data.CacheRequestHitRate != 50 || data.CacheUsageCoverage != float64(5)*100/6 || data.CacheAffinityMissRate != 50 {
		t.Fatalf("cache rates = %+v", data)
	}
	if len(data.HourlyRequests) != 24 || len(data.HourlyCacheTokenReuseRates) != 24 || data.HourlyRequests[23] != 8 || data.HourlyCacheEligibleRequests[23] != 6 || data.HourlyInputTokens[23] != 450 || data.HourlyCachedTokens[23] != 140 || data.HourlyUsageSamples[23] != 5 || data.HourlyCacheSamples[23] != 4 || data.HourlyCacheRequestHits[23] != 2 || data.HourlyCacheWarmupCandidates[23] != 1 || data.HourlyCacheAffinityReuses[23] != 2 || data.HourlyCacheAffinityMisses[23] != 1 || data.HourlyCacheTokenReuseRates[23] != float64(140)*100/450 || data.HourlyCacheRequestHitRates[23] != 50 || data.HourlyCacheUsageCoverage[23] != float64(5)*100/6 {
		t.Fatalf("latest hourly cache point = %+v", data)
	}
}

type testEnvironment struct {
	handler    http.Handler
	auth       *admin.AuthService
	repository *memoryRepository
	management *admin.ManagementService
	accounts   *accountpool.Pool
	probe      *accountprobe.Service
	settings   *MemorySettingsStore
	notifier   *recordingConfigNotifier
	cookie     string
	csrf       string
}

type testAccountPoolStore struct {
	repository *memoryRepository
	management *admin.ManagementService
}

type testOAuthRefreshStore struct {
	repository *memoryRepository
	management *admin.ManagementService
}

func (s *testOAuthRefreshStore) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	return s.repository.ListAccounts(ctx, store.AccountFilter{})
}

func (s *testOAuthRefreshStore) Credentials(ctx context.Context, id string) (domain.Credentials, error) {
	return s.management.GetAccountCredentials(ctx, id)
}

func (s *testOAuthRefreshStore) SaveOAuthRefresh(ctx context.Context, id string, update upstream.OAuthRefreshUpdate) error {
	status := update.Status
	cooldown := update.CooldownUntil
	lastError := update.LastError
	_, err := s.management.UpdateAccount(ctx, id, admin.UpdateAccountInput{
		Credentials: update.Credentials, Status: &status, CooldownUntil: &cooldown, LastError: &lastError,
	})
	return err
}

func (s *testAccountPoolStore) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	return s.repository.ListAccounts(ctx, store.AccountFilter{})
}

func (s *testAccountPoolStore) Credentials(ctx context.Context, id string) (domain.Credentials, error) {
	return s.management.GetAccountCredentials(ctx, id)
}

func (s *testAccountPoolStore) UpdateAccount(ctx context.Context, account domain.Account) error {
	return s.repository.UpdateAccount(ctx, &account)
}

func newTestEnvironment(t *testing.T) *testEnvironment {
	t.Helper()
	repository := newMemoryRepository()
	cipher, err := security.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := security.NewPasswordHasher(security.Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := security.NewTokenManager([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	totp, err := security.NewTOTP("GROK-GO")
	if err != nil {
		t.Fatal(err)
	}
	auth := admin.NewAuthService(repository, cipher, hasher, tokens, totp, time.Hour)
	management := admin.NewManagementService(repository, repository, repository, repository, repository, cipher, tokens)
	accountPool := accountpool.NewPool(&testAccountPoolStore{repository: repository, management: management}, accountpool.DefaultPolicy())
	accountProbe, err := accountprobe.New(accountprobe.Config{
		Accounts: accountPool,
		Reader:   management,
		Upstream: upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
			return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventDone})}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewMemorySettingsStore(map[string]any{})
	notifier := &recordingConfigNotifier{}
	handler := NewAdminHandler(Config{Auth: auth, Management: management, AdminRepository: repository, Accounts: accountPool, AccountProbe: accountProbe, Settings: settings, ConfigChanges: notifier, BootstrapToken: "bootstrap-secret", SessionCookie: "session", CSRFCookie: "csrf", CSRFHeader: "X-CSRF-Token"})
	return &testEnvironment{handler: handler, auth: auth, repository: repository, management: management, accounts: accountPool, probe: accountProbe, settings: settings, notifier: notifier}
}

func newAuthenticatedEnvironment(t *testing.T) *testEnvironment {
	environment := newTestEnvironment(t)
	setup := environment.request(t, http.MethodPost, "/setup", map[string]any{"username": "admin", "password": "correct horse battery staple", "bootstrap_token": "bootstrap-secret"}, "", "")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup failed: %s", setup.Body.String())
	}
	environment.cookie, environment.csrf = environment.login(t)
	return environment
}

func (e *testEnvironment) login(t *testing.T) (string, string) {
	t.Helper()
	return e.loginWithCredentials(t, "admin", "correct horse battery staple")
}

func (e *testEnvironment) loginWithCredentials(t *testing.T, email, password string) (string, string) {
	t.Helper()
	response := e.request(t, http.MethodPost, "/auth/login", map[string]any{"email": email, "password": password}, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", response.Code, response.Body.String())
	}
	var cookie string
	for _, item := range response.Result().Cookies() {
		if item.Name == "session" {
			if item.Path != "/" {
				t.Fatalf("session cookie path = %q", item.Path)
			}
			cookie = item.Value
		}
	}
	csrf := response.Header().Get("X-CSRF-Token")
	if cookie == "" || csrf == "" {
		t.Fatalf("missing auth material: headers=%v", response.Header())
	}
	return cookie, csrf
}

func (e *testEnvironment) request(t *testing.T, method, path string, body any, cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "session", Value: cookie})
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	return response
}

type memoryRepository struct {
	mu                      sync.Mutex
	admins                  map[string]*store.AdminRecord
	sessions                map[string]*store.AdminSession
	accounts                map[string]*domain.Account
	accountListCalls        int
	accountListErr          error
	accountListContextErr   error
	accountListHasDeadline  bool
	accountBatchDeleteCalls int
	keys                    map[string]*domain.ClientKey
	models                  map[string]*domain.ModelSpec
	proxies                 map[string]*domain.Proxy
	logs                    map[string]*domain.RequestLog
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{admins: map[string]*store.AdminRecord{}, sessions: map[string]*store.AdminSession{}, accounts: map[string]*domain.Account{}, keys: map[string]*domain.ClientKey{}, models: map[string]*domain.ModelSpec{}, proxies: map[string]*domain.Proxy{}, logs: map[string]*domain.RequestLog{}}
}

func (r *memoryRepository) CountAdmins(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.admins)), nil
}
func (r *memoryRepository) CreateAdmin(_ context.Context, value *store.AdminRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value.ID = nextID("admin", len(r.admins))
	value.CreatedAt = time.Now()
	value.UpdatedAt = value.CreatedAt
	copy := *value
	r.admins[value.ID] = &copy
	return nil
}
func (r *memoryRepository) GetAdminByID(_ context.Context, id string) (*store.AdminRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepository) GetAdminByEmail(_ context.Context, email string) (*store.AdminRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range r.admins {
		if strings.EqualFold(value.Email, email) {
			copy := *value
			return &copy, nil
		}
	}
	return nil, store.ErrNotFound
}
func (r *memoryRepository) UpdateAdminEmail(_ context.Context, id, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for otherID, other := range r.admins {
		if otherID != id && strings.EqualFold(other.Email, email) {
			return store.ErrConflict
		}
	}
	value.Email = email
	return nil
}
func (r *memoryRepository) UpdateAdminPassword(_ context.Context, id, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.PasswordHash = hash
	return nil
}
func (r *memoryRepository) SetPendingTOTP(_ context.Context, id string, cipher []byte, expires time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.PendingTOTPSecretCipher = append([]byte(nil), cipher...)
	value.PendingTOTPExpiresAt = &expires
	return nil
}
func (r *memoryRepository) EnableTOTP(_ context.Context, id string, cipher []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.TOTPSecretCipher = append([]byte(nil), cipher...)
	value.TOTPEnabled = true
	value.PendingTOTPSecretCipher = nil
	value.PendingTOTPExpiresAt = nil
	return nil
}
func (r *memoryRepository) DisableTOTP(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.TOTPEnabled = false
	value.TOTPSecretCipher = nil
	return nil
}
func (r *memoryRepository) RecordAdminLogin(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.admins[id]
	if !ok {
		return store.ErrNotFound
	}
	value.LastLoginAt = &at
	return nil
}
func (r *memoryRepository) CreateAdminSession(_ context.Context, value *store.AdminSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value.ID = nextID("session", len(r.sessions))
	value.CreatedAt = time.Now()
	value.LastSeenAt = value.CreatedAt
	copy := *value
	r.sessions[string(value.TokenDigest)] = &copy
	return nil
}
func (r *memoryRepository) GetAdminSessionByDigest(_ context.Context, digest []byte) (*store.AdminSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.sessions[string(digest)]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepository) TouchAdminSession(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range r.sessions {
		if value.ID == id {
			value.LastSeenAt = at
			return nil
		}
	}
	return store.ErrNotFound
}
func (r *memoryRepository) DeleteAdminSessionByDigest(_ context.Context, digest []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, string(digest))
	return nil
}
func (r *memoryRepository) DeleteAdminSessionsForAdmin(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range r.sessions {
		if value.AdminID == id {
			delete(r.sessions, key)
		}
	}
	return nil
}
func (r *memoryRepository) DeleteExpiredAdminSessions(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for key, value := range r.sessions {
		if !value.ExpiresAt.After(before) {
			delete(r.sessions, key)
			count++
		}
	}
	return count, nil
}

func (r *memoryRepository) CreateAccount(_ context.Context, value *domain.Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *value
	copy.CreatedAt = time.Now()
	copy.UpdatedAt = copy.CreatedAt
	r.accounts[value.ID] = &copy
	value.CreatedAt, value.UpdatedAt = copy.CreatedAt, copy.UpdatedAt
	return nil
}
func (r *memoryRepository) GetAccount(_ context.Context, id string) (*domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.accounts[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	copy.CredentialCipher = append([]byte(nil), value.CredentialCipher...)
	return &copy, nil
}
func (r *memoryRepository) ListAccounts(ctx context.Context, filter store.AccountFilter) ([]domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accountListCalls++
	r.accountListContextErr = ctx.Err()
	_, r.accountListHasDeadline = ctx.Deadline()
	if r.accountListErr != nil {
		return nil, r.accountListErr
	}
	var result []domain.Account
	for _, value := range r.accounts {
		if !matchesAccountFilter(value, filter) {
			continue
		}
		copy := *value
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	offset := max(filter.Offset, 0)
	if offset >= len(result) {
		return []domain.Account{}, nil
	}
	end := min(offset+limit, len(result))
	return result[offset:end], nil
}
func (r *memoryRepository) CountAccounts(_ context.Context, filter store.AccountFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, value := range r.accounts {
		if matchesAccountFilter(value, filter) {
			total++
		}
	}
	return total, nil
}

func matchesAccountFilter(value *domain.Account, filter store.AccountFilter) bool {
	if filter.Kind != "" && value.Kind != filter.Kind || filter.Status != "" && value.Status != filter.Status {
		return false
	}
	if filter.Tier != "" && !strings.EqualFold(value.Tier, strings.TrimSpace(filter.Tier)) {
		return false
	}
	if filter.ProxyID != nil && value.ProxyID != strings.TrimSpace(*filter.ProxyID) {
		return false
	}
	if filter.Model != "" {
		matched := false
		for _, model := range value.Models {
			if model == filter.Model {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	searchable := strings.ToLower(value.Name + " " + value.Email + " " + strings.Join(value.Tags, " "))
	return strings.Contains(searchable, query)
}
func (r *memoryRepository) UpdateAccount(_ context.Context, value *domain.Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.accounts[value.ID]; !ok {
		return store.ErrNotFound
	}
	copy := *value
	r.accounts[value.ID] = &copy
	return nil
}
func (r *memoryRepository) UpdateAccounts(_ context.Context, values []*domain.Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range values {
		if value == nil {
			return errors.New("account is required")
		}
		if _, ok := r.accounts[value.ID]; !ok {
			return store.ErrNotFound
		}
	}
	updates := make(map[string]*domain.Account, len(values))
	for _, value := range values {
		copy := *value
		copy.CredentialCipher = append([]byte(nil), value.CredentialCipher...)
		copy.Models = append([]string(nil), value.Models...)
		copy.Tags = append([]string(nil), value.Tags...)
		copy.UpdatedAt = time.Now()
		value.UpdatedAt = copy.UpdatedAt
		updates[value.ID] = &copy
	}
	for id, value := range updates {
		r.accounts[id] = value
	}
	return nil
}
func (r *memoryRepository) DeleteAccount(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.accounts[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.accounts, id)
	return nil
}
func (r *memoryRepository) DeleteAccounts(_ context.Context, ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accountBatchDeleteCalls++
	for _, id := range ids {
		if _, ok := r.accounts[id]; !ok {
			return store.ErrNotFound
		}
	}
	for _, id := range ids {
		delete(r.accounts, id)
	}
	return nil
}

func (r *memoryRepository) CreateClientKey(_ context.Context, value *domain.ClientKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value.ID = nextID("key", len(r.keys))
	value.CreatedAt = time.Now()
	value.UpdatedAt = value.CreatedAt
	copy := *value
	copy.Digest = append([]byte(nil), value.Digest...)
	copy.SecretCipher = append([]byte(nil), value.SecretCipher...)
	r.keys[value.ID] = &copy
	return nil
}
func (r *memoryRepository) GetClientKey(_ context.Context, id string) (*domain.ClientKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.keys[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	copy.Digest = append([]byte(nil), value.Digest...)
	copy.SecretCipher = append([]byte(nil), value.SecretCipher...)
	return &copy, nil
}
func (r *memoryRepository) GetClientKeyByDigest(_ context.Context, digest []byte) (*domain.ClientKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range r.keys {
		if string(value.Digest) == string(digest) {
			copy := *value
			return &copy, nil
		}
	}
	return nil, store.ErrNotFound
}
func (r *memoryRepository) ListClientKeys(context.Context, store.Pagination) ([]domain.ClientKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.ClientKey, 0, len(r.keys))
	for _, value := range r.keys {
		result = append(result, *value)
	}
	return result, nil
}
func (r *memoryRepository) CountClientKeys(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.keys)), nil
}
func (r *memoryRepository) CountActiveClientKeys(_ context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, value := range r.keys {
		if value.Enabled && (value.ExpiresAt == nil || value.ExpiresAt.After(now)) {
			total++
		}
	}
	return total, nil
}
func (r *memoryRepository) UpdateClientKey(_ context.Context, value *domain.ClientKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.keys[value.ID]
	if !ok {
		return store.ErrNotFound
	}
	digest := current.Digest
	secretCipher := current.SecretCipher
	copy := *value
	copy.Digest = digest
	copy.SecretCipher = secretCipher
	r.keys[value.ID] = &copy
	return nil
}
func (r *memoryRepository) TouchClientKey(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.keys[id]
	if !ok {
		return store.ErrNotFound
	}
	value.LastUsedAt = &at
	return nil
}
func (r *memoryRepository) DeleteClientKey(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.keys[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.keys, id)
	return nil
}

func (r *memoryRepository) CreateModel(_ context.Context, value *domain.ModelSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *value
	r.models[value.ID] = &copy
	return nil
}
func (r *memoryRepository) GetModel(_ context.Context, id string) (*domain.ModelSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.models[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepository) ListModels(context.Context, store.ModelFilter) ([]domain.ModelSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.ModelSpec, 0, len(r.models))
	for _, value := range r.models {
		result = append(result, *value)
	}
	return result, nil
}
func (r *memoryRepository) CountModels(context.Context, store.ModelFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.models)), nil
}
func (r *memoryRepository) UpdateModel(_ context.Context, value *domain.ModelSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.models[value.ID]; !ok {
		return store.ErrNotFound
	}
	copy := *value
	r.models[value.ID] = &copy
	return nil
}
func (r *memoryRepository) DeleteModel(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.models[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.models, id)
	return nil
}

func (r *memoryRepository) CreateProxy(_ context.Context, value *domain.Proxy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value.ID = nextID("proxy", len(r.proxies))
	copy := *value
	r.proxies[value.ID] = &copy
	return nil
}
func (r *memoryRepository) GetProxy(_ context.Context, id string) (*domain.Proxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.proxies[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepository) ListProxies(context.Context, store.Pagination) ([]domain.Proxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.Proxy, 0, len(r.proxies))
	for _, value := range r.proxies {
		result = append(result, *value)
	}
	return result, nil
}
func (r *memoryRepository) CountProxies(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.proxies)), nil
}
func (r *memoryRepository) UpdateProxy(_ context.Context, value *domain.Proxy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.proxies[value.ID]; !ok {
		return store.ErrNotFound
	}
	copy := *value
	r.proxies[value.ID] = &copy
	return nil
}
func (r *memoryRepository) DeleteProxy(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.proxies[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.proxies, id)
	return nil
}

func (r *memoryRepository) CreateRequestLog(_ context.Context, value *domain.RequestLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value.ID = nextID("log", len(r.logs))
	copy := *value
	r.logs[value.ID] = &copy
	return nil
}
func (r *memoryRepository) GetRequestLog(_ context.Context, id string) (*domain.RequestLog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.logs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepository) ListRequestLogs(context.Context, store.RequestLogFilter) ([]domain.RequestLog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.RequestLog, 0, len(r.logs))
	for _, value := range r.logs {
		result = append(result, *value)
	}
	return result, nil
}
func (r *memoryRepository) CountRequestLogs(context.Context, store.RequestLogFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.logs)), nil
}
func (r *memoryRepository) GetRequestLogStats(_ context.Context, from, to time.Time) (*store.RequestLogStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := &store.RequestLogStats{Hourly: make([]store.RequestLogHourStats, 0, 24)}
	hours := make(map[int]*store.RequestLogHourStats)
	for _, value := range r.logs {
		if value.CreatedAt.Before(from) || !value.CreatedAt.Before(to) {
			continue
		}
		stats.Requests++
		if value.StatusCode >= 200 && value.StatusCode < 400 {
			stats.Successes++
		}
		stats.DurationMS += value.DurationMS
		stats.InputTokens += value.InputTokens
		stats.OutputTokens += value.OutputTokens
		hoursAgo := int(to.Sub(value.CreatedAt).Hours())
		hour := hours[hoursAgo]
		if hour == nil {
			hour = &store.RequestLogHourStats{HoursAgo: hoursAgo}
			hours[hoursAgo] = hour
		}
		hour.Requests++
		if store.CacheEligibleRequest(value.Endpoint, value.StatusCode) {
			stats.CacheEligibleRequests++
			hour.CacheEligibleRequests++
		}
		if store.CacheEligibleRequest(value.Endpoint, value.StatusCode) && value.UsageParsed {
			stats.UsageSamples++
			hour.UsageSamples++
		}
		if store.CacheEligibleRequest(value.Endpoint, value.StatusCode) && value.UsageParsed && value.InputTokens > 0 {
			cached := store.NormalizedCachedTokens(value.InputTokens, value.CachedTokens)
			var cacheMetadata struct {
				IdentityApplied     bool `json:"cache_identity_applied"`
				AffinityReused      bool `json:"cache_affinity_reused"`
				AffinityEstablished bool `json:"cache_affinity_established"`
			}
			_ = json.Unmarshal(value.Metadata, &cacheMetadata)
			stats.CacheInputTokens += value.InputTokens
			stats.CachedTokens += cached
			stats.CacheSamples++
			hour.InputTokens += value.InputTokens
			hour.CachedTokens += cached
			hour.CacheSamples++
			if cached > 0 {
				stats.CacheRequestHits++
				hour.CacheRequestHits++
			}
			if cacheMetadata.AffinityEstablished && cached == 0 {
				stats.CacheWarmupCandidates++
				hour.CacheWarmupCandidates++
			}
			if cacheMetadata.AffinityReused {
				stats.CacheAffinityReuses++
				hour.CacheAffinityReuses++
				if cached == 0 {
					stats.CacheAffinityMisses++
					hour.CacheAffinityMisses++
				}
			}
		}
	}
	for _, hour := range hours {
		stats.Hourly = append(stats.Hourly, *hour)
	}
	return stats, nil
}
func (r *memoryRepository) UpdateRequestLog(_ context.Context, value *domain.RequestLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.logs[value.ID]; !ok {
		return store.ErrNotFound
	}
	copy := *value
	r.logs[value.ID] = &copy
	return nil
}
func (r *memoryRepository) DeleteRequestLog(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.logs[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.logs, id)
	return nil
}
func (r *memoryRepository) DeleteRequestLogsBefore(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for id, value := range r.logs {
		if value.CreatedAt.Before(before) {
			delete(r.logs, id)
			count++
		}
	}
	return count, nil
}

func nextID(prefix string, count int) string { return fmt.Sprintf("%s-%d", prefix, count+1) }
