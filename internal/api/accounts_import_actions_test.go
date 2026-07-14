package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accountprobe"
	accountpool "github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func TestMultipartBuildOAuthImportCanActivateRefreshAndProbe(t *testing.T) {
	var refreshCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "rotated-import-access", "refresh_token": "rotated-import-refresh",
			"token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer server.Close()

	environment := newAuthenticatedEnvironment(t)
	configureImportRefresh(t, environment, server)
	response := multipartImportRequest(t, environment, "/accounts/import", map[string]string{
		"initial_status":     "active",
		"post_import_action": "refresh_probe",
	}, readBuildOAuthFixture(t))
	if response.Code != http.StatusOK {
		t.Fatalf("multipart import = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data accountImportResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope.Data
	if result.Imported != 1 || result.PostAction == nil || result.PostAction.Action != "refresh_probe" || result.PostAction.Total != 1 || result.PostAction.Succeeded != 1 || result.PostAction.Failed != 0 {
		t.Fatalf("import result = %+v", result)
	}
	action := result.PostAction.Items[0]
	if !action.Refreshed || !action.Probed || !action.Success || action.Status != domain.AccountActive || action.Probe == nil || !action.Probe.Success {
		t.Fatalf("post-import action = %+v", action)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d", refreshCalls.Load())
	}
	if strings.Contains(response.Body.String(), "rotated-import-access") || strings.Contains(response.Body.String(), "rotated-import-refresh") {
		t.Fatalf("import response exposed rotated credentials: %s", response.Body.String())
	}
	account := onlyStoredAccount(t, environment)
	if account.Status != domain.AccountActive || account.CooldownUntil != nil {
		t.Fatalf("imported account state = %+v", account)
	}
	credentials, err := environment.management.GetAccountCredentials(context.Background(), account.ID)
	if err != nil || credentials.AccessToken != "rotated-import-access" || credentials.RefreshToken != "rotated-import-refresh" {
		t.Fatalf("rotated credentials were not stored: %v", err)
	}
}

func TestRefreshProbeImportStaysOutOfSchedulingUntilProbeSucceeds(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "held-access", "refresh_token": "held-refresh",
			"token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer refreshServer.Close()

	environment := newAuthenticatedEnvironment(t)
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	probe, err := accountprobe.New(accountprobe.Config{
		Accounts: environment.accounts,
		Reader:   environment.management,
		Upstream: upstream.ClientFunc(func(ctx context.Context, _ upstream.Request) (*upstream.Response, error) {
			close(probeStarted)
			select {
			case <-releaseProbe:
				return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventDone})}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	environment.probe = probe
	configureImportRefresh(t, environment, refreshServer)

	request := newMultipartImportHTTPRequest(t, environment, "/accounts/import", map[string]string{
		"initial_status":     "active",
		"post_import_action": "refresh_probe",
		"concurrency_limit":  "2",
	}, readBuildOAuthFixture(t))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		environment.handler.ServeHTTP(response, request)
	}()

	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		close(releaseProbe)
		t.Fatal("post-import probe did not start")
	}
	accounts, err := environment.management.ListAccounts(context.Background(), store.AccountFilter{})
	if err != nil || len(accounts) != 1 || accounts[0].Status != domain.AccountError {
		close(releaseProbe)
		t.Fatalf("account was schedulable before probe completion: accounts=%+v err=%v", accounts, err)
	}
	model := domain.ModelSpec{ID: "grok-4.5", UpstreamModel: "grok-4.5", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	if lease, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model}); !errors.Is(acquireErr, accountpool.ErrNoAccount) {
		if lease != nil {
			_ = lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK})
		}
		close(releaseProbe)
		t.Fatalf("pending account entered normal scheduling: %v", acquireErr)
	}
	close(releaseProbe)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("import request did not finish after probe release")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("import response = %d %s", response.Code, response.Body.String())
	}
	if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountActive {
		t.Fatalf("successful probe did not activate account: %+v", stored)
	}
}

func TestRefreshProbeImportFailureKeepsAccountInErrorHold(t *testing.T) {
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusUnauthorized} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "held-access", "refresh_token": "held-refresh",
					"token_type": "Bearer", "expires_in": 3600,
				})
			}))
			defer refreshServer.Close()

			environment := newAuthenticatedEnvironment(t)
			probe, err := accountprobe.New(accountprobe.Config{
				Accounts: environment.accounts,
				Reader:   environment.management,
				Upstream: upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
					return &upstream.Response{
						StatusCode: statusCode,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       json.RawMessage(`{"error":"rejected"}`),
					}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			environment.probe = probe
			configureImportRefresh(t, environment, refreshServer)

			response := multipartImportRequest(t, environment, "/accounts/import", map[string]string{
				"initial_status":     "active",
				"post_import_action": "refresh_probe",
			}, readBuildOAuthFixture(t))
			if response.Code != http.StatusOK {
				t.Fatalf("import response = %d %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Data accountImportResult `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			action := envelope.Data.PostAction
			if action == nil || action.Succeeded != 0 || action.Failed != 1 || len(action.Items) != 1 || !action.Items[0].Probed || action.Items[0].Success || action.Items[0].Status != domain.AccountError || action.Items[0].Probe == nil || action.Items[0].Probe.Account == nil || action.Items[0].Probe.Account.Status != domain.AccountError {
				t.Fatalf("failed post-import action = %+v", action)
			}
			account := onlyStoredAccount(t, environment)
			if account.Status != domain.AccountError || account.CooldownUntil != nil || account.LastError == "" {
				t.Fatalf("failed probe released imported account: %+v", account)
			}
			model := domain.ModelSpec{ID: "grok-4.5", UpstreamModel: "grok-4.5", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
			if lease, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model}); !errors.Is(acquireErr, accountpool.ErrNoAccount) {
				if lease != nil {
					_ = lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK})
				}
				t.Fatalf("failed probe account entered scheduling: %v", acquireErr)
			}
		})
	}
}

func TestBuildOAuthImportStatusControlsAndDisabledPreservation(t *testing.T) {
	t.Run("JSON status alias overrides file status", func(t *testing.T) {
		environment := newAuthenticatedEnvironment(t)
		var account map[string]any
		if err := json.Unmarshal(readBuildOAuthFixture(t), &account); err != nil {
			t.Fatal(err)
		}
		response := environment.request(t, http.MethodPost, "/accounts/import", map[string]any{
			"status": "active", "post_import_action": "none", "accounts": []any{account},
		}, environment.cookie, environment.csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("JSON import = %d %s", response.Code, response.Body.String())
		}
		if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountActive || stored.CooldownUntil != nil {
			t.Fatalf("status alias was not applied: %+v", stored)
		}
	})

	t.Run("query initial status overrides raw build OAuth", func(t *testing.T) {
		environment := newAuthenticatedEnvironment(t)
		response := environment.request(t, http.MethodPost, "/accounts/import?initial_status=active", json.RawMessage(readBuildOAuthFixture(t)), environment.cookie, environment.csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("query override import = %d %s", response.Code, response.Body.String())
		}
		if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountActive {
			t.Fatalf("query override was not applied: %+v", stored)
		}
	})

	t.Run("raw build OAuth accepts embedded controls", func(t *testing.T) {
		environment := newAuthenticatedEnvironment(t)
		var payload map[string]any
		if err := json.Unmarshal(readBuildOAuthFixture(t), &payload); err != nil {
			t.Fatal(err)
		}
		payload["initial_status"] = "active"
		payload["post_import_action"] = "none"
		response := environment.request(t, http.MethodPost, "/accounts/import", payload, environment.cookie, environment.csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("raw controlled import = %d %s", response.Code, response.Body.String())
		}
		if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountActive {
			t.Fatalf("embedded initial status was not applied: %+v", stored)
		}
	})

	t.Run("query alias overrides body canonical status", func(t *testing.T) {
		environment := newAuthenticatedEnvironment(t)
		var account map[string]any
		if err := json.Unmarshal(readBuildOAuthFixture(t), &account); err != nil {
			t.Fatal(err)
		}
		response := environment.request(t, http.MethodPost, "/accounts/import?status=disabled", map[string]any{
			"initial_status": "active", "post_import_action": "none", "accounts": []any{account},
		}, environment.cookie, environment.csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("query alias import = %d %s", response.Code, response.Body.String())
		}
		if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountDisabled {
			t.Fatalf("query status did not override body initial_status: %+v", stored)
		}
	})

	t.Run("active override clears generic disabled flag", func(t *testing.T) {
		environment := newAuthenticatedEnvironment(t)
		response := environment.request(t, http.MethodPost, "/accounts/import", map[string]any{
			"initial_status":     "active",
			"post_import_action": "none",
			"accounts": []any{map[string]any{
				"name": "generic CLI OAuth", "kind": "cli_oauth", "disabled": true,
				"credentials": map[string]any{"access_token": "generic-access", "refresh_token": "generic-refresh", "token_type": "Bearer"},
			}},
		}, environment.cookie, environment.csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("generic status override import = %d %s", response.Code, response.Body.String())
		}
		if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountActive {
			t.Fatalf("generic disabled flag overrode active status: %+v", stored)
		}
	})

	t.Run("file disabled is preserved without status override", func(t *testing.T) {
		var refreshCalls atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			refreshCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()
		environment := newAuthenticatedEnvironment(t)
		configureImportRefresh(t, environment, server)
		response := environment.request(t, http.MethodPost, "/accounts/import?post_import_action=refresh_probe", json.RawMessage(readBuildOAuthFixture(t)), environment.cookie, environment.csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("disabled import = %d %s", response.Code, response.Body.String())
		}
		if stored := onlyStoredAccount(t, environment); stored.Status != domain.AccountDisabled {
			t.Fatalf("explicit disabled status was not preserved: %+v", stored)
		}
		if refreshCalls.Load() != 0 {
			t.Fatalf("disabled account was refreshed %d times", refreshCalls.Load())
		}
		var envelope struct {
			Data accountImportResult `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.PostAction == nil || envelope.Data.PostAction.Total != 0 {
			t.Fatalf("disabled action summary = %+v", envelope.Data.PostAction)
		}
	})
}

func TestImportPostActionFailureIsReportedWithoutCredentialBody(t *testing.T) {
	const responseSecret = "post-import-refresh-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + responseSecret + `"}`))
	}))
	defer server.Close()

	environment := newAuthenticatedEnvironment(t)
	configureImportRefresh(t, environment, server)
	response := multipartImportRequest(t, environment, "/accounts/import", map[string]string{
		"initial_status":     "active",
		"post_import_action": "refresh",
	}, readBuildOAuthFixture(t))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), responseSecret) {
		t.Fatalf("refresh failure import = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data accountImportResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Imported != 1 || envelope.Data.PostAction == nil || envelope.Data.PostAction.Succeeded != 0 || envelope.Data.PostAction.Failed != 1 || !strings.Contains(envelope.Data.PostAction.Items[0].Message, "rejected") {
		t.Fatalf("refresh failure summary = %+v", envelope.Data)
	}
}

func TestImportWithoutProbeServicePersistsDiagnosticHold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "held-without-probe-access", "refresh_token": "held-without-probe-refresh",
			"token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer server.Close()

	environment := newAuthenticatedEnvironment(t)
	environment.probe = nil
	configureImportRefresh(t, environment, server)
	response := multipartImportRequest(t, environment, "/accounts/import", map[string]string{
		"initial_status":     "active",
		"post_import_action": "refresh_probe",
	}, readBuildOAuthFixture(t))
	if response.Code != http.StatusOK {
		t.Fatalf("import response = %d %s", response.Code, response.Body.String())
	}
	account := onlyStoredAccount(t, environment)
	if account.Status != domain.AccountError || !strings.Contains(account.LastError, "probing is not configured") {
		t.Fatalf("unverified account state = %+v", account)
	}
	model := domain.ModelSpec{ID: "grok-4.5", UpstreamModel: "grok-4.5", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	if lease, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model}); !errors.Is(acquireErr, accountpool.ErrNoAccount) {
		if lease != nil {
			_ = lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK})
		}
		t.Fatalf("unverified account entered scheduling: %v", acquireErr)
	}
}

func TestImportRejectsInvalidStatusAndPostAction(t *testing.T) {
	for _, test := range []struct {
		query string
		code  string
	}{
		{query: "initial_status=unknown", code: "invalid_initial_status"},
		{query: "post_import_action=probe_only", code: "invalid_post_import_action"},
	} {
		environment := newAuthenticatedEnvironment(t)
		response := environment.request(t, http.MethodPost, "/accounts/import?"+test.query, json.RawMessage(readBuildOAuthFixture(t)), environment.cookie, environment.csrf)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.code) || len(environment.repository.accounts) != 0 {
			t.Fatalf("invalid import %q = %d %s", test.query, response.Code, response.Body.String())
		}
	}
}

func configureImportRefresh(t *testing.T, environment *testEnvironment, server *httptest.Server) {
	t.Helper()
	refreshService := &upstream.RefreshService{
		OAuth: upstream.NewOAuthService(upstream.OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: &testOAuthRefreshStore{repository: environment.repository, management: environment.management},
	}
	environment.handler = NewAdminHandler(Config{
		Auth: environment.auth, Management: environment.management, AdminRepository: environment.repository,
		Accounts: environment.accounts, AccountProbe: environment.probe, OAuthRefresh: refreshService,
		Settings: environment.settings, ConfigChanges: environment.notifier,
		SessionCookie: "session", CSRFCookie: "csrf", CSRFHeader: "X-CSRF-Token",
	})
}

func multipartImportRequest(t *testing.T, environment *testEnvironment, path string, fields map[string]string, fixture []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := newMultipartImportHTTPRequest(t, environment, path, fields, fixture)
	response := httptest.NewRecorder()
	environment.handler.ServeHTTP(response, request)
	return response
}

func newMultipartImportHTTPRequest(t *testing.T, environment *testEnvironment, path string, fields map[string]string, fixture []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := writer.CreateFormFile("files", "xai-account@example.test.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(fixture); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", environment.csrf)
	request.AddCookie(&http.Cookie{Name: "session", Value: environment.cookie})
	return request
}

func onlyStoredAccount(t *testing.T, environment *testEnvironment) *domain.Account {
	t.Helper()
	if len(environment.repository.accounts) != 1 {
		t.Fatalf("stored accounts = %d", len(environment.repository.accounts))
	}
	for _, account := range environment.repository.accounts {
		return account
	}
	return nil
}
