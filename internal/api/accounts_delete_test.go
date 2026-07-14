package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accountpool "github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestBatchDeleteAccountsValidationAtomicityAndReload(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	firstID := createBatchDeleteTestAccount(t, environment, "First", "first-sso")
	secondID := createBatchDeleteTestAccount(t, environment, "Second", "second-sso")
	thirdID := createBatchDeleteTestAccount(t, environment, "Third", "third-sso")
	environment.resetAccountOperationCounts()
	environment.notifier.reset()

	missingCSRF := environment.request(t, http.MethodPost, "/accounts/batch-delete", map[string]any{"ids": []string{firstID}}, environment.cookie, "")
	if missingCSRF.Code != http.StatusForbidden || !strings.Contains(missingCSRF.Body.String(), "invalid_csrf") {
		t.Fatalf("missing CSRF response = %d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	assertAccountDeleteSideEffects(t, environment, 0, 0, 0)

	empty := environment.request(t, http.MethodPost, "/accounts/batch-delete", map[string]any{"ids": []string{}}, environment.cookie, environment.csrf)
	if empty.Code != http.StatusBadRequest || !strings.Contains(empty.Body.String(), "invalid_account_batch") {
		t.Fatalf("empty batch response = %d %s", empty.Code, empty.Body.String())
	}
	assertAccountDeleteSideEffects(t, environment, 0, 0, 0)

	missing := environment.request(t, http.MethodPost, "/accounts/batch-delete", map[string]any{"ids": []string{firstID, "missing-account"}}, environment.cookie, environment.csrf)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing account response = %d %s", missing.Code, missing.Body.String())
	}
	if _, ok := environment.repository.accounts[firstID]; !ok || len(environment.repository.accounts) != 3 {
		t.Fatalf("failed batch partially deleted accounts: %#v", environment.repository.accounts)
	}
	assertAccountDeleteSideEffects(t, environment, 1, 0, 0)

	environment.resetAccountOperationCounts()
	environment.notifier.reset()
	success := environment.request(t, http.MethodPost, "/accounts/batch-delete", map[string]any{"ids": []string{firstID, firstID, secondID}}, environment.cookie, environment.csrf)
	if success.Code != http.StatusOK || !strings.Contains(success.Body.String(), `"deleted":2`) {
		t.Fatalf("successful batch response = %d %s", success.Code, success.Body.String())
	}
	if len(environment.repository.accounts) != 1 {
		t.Fatalf("remaining accounts = %#v", environment.repository.accounts)
	}
	if _, ok := environment.repository.accounts[thirdID]; !ok {
		t.Fatalf("unselected account %q was deleted", thirdID)
	}
	assertAccountDeleteSideEffects(t, environment, 1, 1, 1)
}

func TestAccountDeletionReturnsSuccessAndNotifiesWhenPoolReloadFails(t *testing.T) {
	tests := []struct {
		name             string
		method           string
		path             func([]string) string
		body             func([]string) any
		accountCount     int
		batchDeleteCalls int
		responseFragment string
	}{
		{
			name:             "single delete",
			method:           http.MethodDelete,
			path:             func(ids []string) string { return "/accounts/" + ids[0] },
			body:             func([]string) any { return nil },
			accountCount:     1,
			responseFragment: `"deleted":true`,
		},
		{
			name:             "batch delete",
			method:           http.MethodPost,
			path:             func([]string) string { return "/accounts/batch-delete" },
			body:             func(ids []string) any { return map[string]any{"ids": ids} },
			accountCount:     2,
			batchDeleteCalls: 1,
			responseFragment: `"deleted":2`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newAuthenticatedEnvironment(t)
			ids := make([]string, 0, test.accountCount)
			for index := range test.accountCount {
				ids = append(ids, createBatchDeleteTestAccount(t, environment, test.name, test.name+string(rune('a'+index))))
			}
			if err := environment.accounts.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}
			environment.resetAccountOperationCounts()
			environment.notifier.reset()
			environment.repository.mu.Lock()
			environment.repository.accountListErr = errors.New("forced account pool reload failure")
			environment.repository.mu.Unlock()

			requestContext, cancel := context.WithCancel(context.Background())
			cancel()
			response := environment.requestWithContext(t, requestContext, test.method, test.path(ids), test.body(ids))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.responseFragment) {
				t.Fatalf("delete response = %d %s", response.Code, response.Body.String())
			}

			environment.repository.mu.Lock()
			remaining := len(environment.repository.accounts)
			reloadContextErr := environment.repository.accountListContextErr
			reloadHasDeadline := environment.repository.accountListHasDeadline
			environment.repository.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("remaining accounts = %d, want 0", remaining)
			}
			if reloadContextErr != nil || !reloadHasDeadline {
				t.Fatalf("reload context error/deadline = %v/%v, want nil/true", reloadContextErr, reloadHasDeadline)
			}
			assertAccountDeleteSideEffects(t, environment, test.batchDeleteCalls, 1, 1)

			environment.notifier.mu.Lock()
			notifyContextErr := environment.notifier.lastContextErr
			notifyHasDeadline := environment.notifier.lastHasDeadline
			environment.notifier.mu.Unlock()
			if notifyContextErr != nil || !notifyHasDeadline {
				t.Fatalf("notification context error/deadline = %v/%v, want nil/true", notifyContextErr, notifyHasDeadline)
			}
			model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialGrokSSO}}
			if lease, acquireErr := environment.accounts.Acquire(context.Background(), accountpool.Selection{Model: model}); !errors.Is(acquireErr, accountpool.ErrNoAccount) {
				if lease != nil {
					_ = lease.Release(context.Background(), accountpool.Feedback{StatusCode: http.StatusOK})
				}
				t.Fatalf("deleted account remained schedulable after reload failure: %v", acquireErr)
			}
		})
	}
}

func createBatchDeleteTestAccount(t *testing.T, environment *testEnvironment, name, token string) string {
	t.Helper()
	account, err := environment.management.CreateAccount(context.Background(), admin.CreateAccountInput{
		Name: name,
		Kind: domain.CredentialGrokSSO,
		Credentials: domain.Credentials{
			SSO: token,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return account.ID
}

func (e *testEnvironment) resetAccountOperationCounts() {
	e.repository.mu.Lock()
	e.repository.accountListCalls = 0
	e.repository.accountBatchDeleteCalls = 0
	e.repository.accountListContextErr = nil
	e.repository.accountListHasDeadline = false
	e.repository.mu.Unlock()
}

func (e *testEnvironment) requestWithContext(t *testing.T, ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", e.csrf)
	request.AddCookie(&http.Cookie{Name: "session", Value: e.cookie})
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	return response
}

func assertAccountDeleteSideEffects(t *testing.T, environment *testEnvironment, deleteCalls, reloads, notifications int) {
	t.Helper()
	environment.repository.mu.Lock()
	gotDeleteCalls := environment.repository.accountBatchDeleteCalls
	gotReloads := environment.repository.accountListCalls
	environment.repository.mu.Unlock()
	if gotDeleteCalls != deleteCalls || gotReloads != reloads {
		t.Fatalf("batch delete calls/reloads = %d/%d, want %d/%d", gotDeleteCalls, gotReloads, deleteCalls, reloads)
	}
	if got := environment.notifier.count(configevent.ScopeAccounts); got != notifications {
		t.Fatalf("account notifications = %d, want %d", got, notifications)
	}
}

func (n *recordingConfigNotifier) reset() {
	n.mu.Lock()
	n.scopes = nil
	n.lastContextErr = nil
	n.lastHasDeadline = false
	n.mu.Unlock()
}

func (n *recordingConfigNotifier) count(scope configevent.Scope) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, value := range n.scopes {
		if value == scope {
			count++
		}
	}
	return count
}
