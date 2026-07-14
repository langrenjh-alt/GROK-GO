package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestOAuthPKCEExchangeAndRefresh(t *testing.T) {
	var grants []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, r.Form)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh-2", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()

	service := NewOAuthService(OAuthConfig{AuthorizationURL: "https://accounts.example/authorize", TokenURL: server.URL, ClientID: "client", RedirectURL: "http://localhost/callback", Scope: "openid offline_access"}, server.Client())
	session, err := service.Begin("state")
	if err != nil {
		t.Fatal(err)
	}
	if session.Verifier == "" || session.Challenge == "" || !strings.Contains(session.AuthorizationURL, "code_challenge_method=S256") {
		t.Fatalf("invalid PKCE session: %#v", session)
	}
	credentials, err := service.Exchange(context.Background(), "code", session.Verifier)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "access" || credentials.ExpiresAt.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}
	_, err = service.Refresh(context.Background(), domain.Credentials{RefreshToken: "refresh-1"})
	if err != nil {
		t.Fatal(err)
	}
	if grants[0].Get("grant_type") != "authorization_code" || grants[1].Get("grant_type") != "refresh_token" {
		t.Fatalf("unexpected grants: %#v", grants)
	}
}

func TestOAuthRefreshPreservesCredentialClientID(t *testing.T) {
	var clientID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		clientID = r.Form.Get("client_id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()

	service := NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "service-client"}, server.Client())
	credentials, err := service.Refresh(context.Background(), domain.Credentials{RefreshToken: "old-refresh", ClientID: "imported-client"})
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "imported-client" || credentials.ClientID != "imported-client" {
		t.Fatalf("client ID was not preserved: request=%q credentials=%q", clientID, credentials.ClientID)
	}
}

func TestOAuthStateExchangeConsumesVerifierOnce(t *testing.T) {
	var grant url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		grant = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600})
	}))
	defer server.Close()

	coordinator := newMemoryCoordinator()
	service := NewOAuthService(OAuthConfig{
		AuthorizationURL: "https://accounts.example/authorize",
		TokenURL:         server.URL,
		ClientID:         "client",
		RedirectURL:      "http://localhost/callback",
		Scope:            "openid offline_access",
		StateTTL:         2 * time.Minute,
	}, server.Client(), coordinator)

	session, err := service.BeginContext(context.Background(), "state")
	if err != nil {
		t.Fatal(err)
	}
	if session.Verifier != "" {
		t.Fatal("coordinated OAuth session exposed its PKCE verifier")
	}
	if coordinator.ttl(oauthStateKey("state")) != 2*time.Minute {
		t.Fatalf("unexpected state TTL: %s", coordinator.ttl(oauthStateKey("state")))
	}
	credentials, err := service.ExchangeState(context.Background(), "code", session.State)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "access" || grant.Get("code_verifier") == "" {
		t.Fatalf("unexpected exchange: credentials=%#v grant=%#v", credentials, grant)
	}
	if _, err := service.ExchangeState(context.Background(), "code", session.State); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("expected consumed state to be rejected, got %v", err)
	}
}

func TestRefreshServiceUsesPerAccountOwnerLock(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()

	credentials := domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute)}
	store := newBarrierCredentialStore(credentials, 2)
	coordinator := newMemoryCoordinator()
	oauth := NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client())
	services := []*RefreshService{
		{OAuth: oauth, Store: store, Locks: coordinator, Before: time.Hour, LockTTL: time.Minute},
		{OAuth: oauth, Store: store, Locks: coordinator, Before: time.Hour, LockTTL: time.Minute},
	}

	start := make(chan struct{})
	errorsCh := make(chan error, len(services))
	for _, service := range services {
		go func(service *RefreshService) {
			<-start
			errorsCh <- service.RefreshDue(context.Background())
		}(service)
	}
	close(start)
	for range services {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}

	if refreshes.Load() != 1 {
		t.Fatalf("expected one upstream refresh, got %d", refreshes.Load())
	}
	if store.saveCount() != 1 {
		t.Fatalf("expected one credential save, got %d", store.saveCount())
	}
	if coordinator.exists(refreshLockKey("account")) {
		t.Fatal("refresh lock remained after refresh completed")
	}
	if coordinator.compareDeleteCount() < 1 || !coordinator.allComparesMatched() {
		t.Fatal("refresh lock was not released with its owner token")
	}
}

func TestRefreshServiceConcurrentManualRefreshReusesRotatedCredentials(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()

	store := newBarrierCredentialStore(domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh"}, 2)
	store.setAccountState(domain.AccountExpired, nil, "previous refresh failure")
	coordinator := newMemoryCoordinator()
	var callbacks atomic.Int32
	service := &RefreshService{
		OAuth: NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: store, Locks: coordinator, LockTTL: time.Minute,
		OnAccountsChanged: func(context.Context) { callbacks.Add(1) },
	}

	start := make(chan struct{})
	results := make(chan domain.Credentials, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			credentials, err := service.RefreshAccount(context.Background(), "account")
			results <- credentials
			errorsCh <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
		credentials := <-results
		if credentials.AccessToken != "new-access" || credentials.RefreshToken != "new-refresh" {
			t.Fatalf("unexpected refreshed credentials: %#v", credentials)
		}
	}
	if refreshes.Load() != 1 || store.saveCount() != 1 || callbacks.Load() != 1 {
		t.Fatalf("expected one token rotation, save, and callback; got refreshes=%d saves=%d callbacks=%d", refreshes.Load(), store.saveCount(), callbacks.Load())
	}
	if coordinator.compareDeleteCount() != 2 || !coordinator.allComparesMatched() {
		t.Fatal("concurrent refresh requests did not release their owner locks")
	}
	update := store.refreshUpdate()
	if update.Status != domain.AccountActive || update.CooldownUntil != nil || update.LastError != "" {
		t.Fatalf("successful refresh did not restore active state: %#v", update)
	}
}

func TestRefreshAccountForProbeKeepsRefreshedAccountOutOfScheduling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()

	store := newBarrierCredentialStore(domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh"}, 1)
	store.setAccountState(domain.AccountCooldown, timePointer(time.Now().Add(time.Hour)), "previous refresh failure")
	var callbacks atomic.Int32
	service := &RefreshService{
		OAuth:             NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store:             store,
		OnAccountsChanged: func(context.Context) { callbacks.Add(1) },
	}

	credentials, err := service.RefreshAccountForProbe(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "new-access" || credentials.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected refreshed credentials: %#v", credentials)
	}
	update := store.refreshUpdate()
	if update.Credentials == nil || update.Credentials.AccessToken != "new-access" {
		t.Fatalf("refreshed credentials were not saved: %#v", update)
	}
	if update.Status != domain.AccountError || update.CooldownUntil != nil || update.LastError != "" {
		t.Fatalf("account was not held for its health probe: %#v", update)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("account-change callbacks = %d, want 1", callbacks.Load())
	}
}

func TestRefreshAccountForProbeFailureKeepsVerificationHold(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if refreshCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"unexpected-access","refresh_token":"unexpected-refresh","expires_in":3600}`))
	}))
	defer server.Close()

	store := newBarrierCredentialStore(domain.Credentials{
		AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: now.Add(30 * time.Minute),
	}, 1)
	store.setAccountState(domain.AccountError, nil, "post-import verification pending")
	service := &RefreshService{
		OAuth: NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: store, Now: func() time.Time { return now },
	}

	if _, err := service.RefreshAccountForProbe(context.Background(), "account"); err == nil {
		t.Fatal("failed refresh returned no error")
	}
	update := store.refreshUpdate()
	if update.Status != domain.AccountError || update.CooldownUntil != nil || update.LastError == "" {
		t.Fatalf("failed probe refresh released the verification hold: %#v", update)
	}

	if err := service.RefreshDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if account := store.accountState(); account.Status != domain.AccountError || account.CooldownUntil != nil {
		t.Fatalf("background refresh promoted an unverified account: %#v", account)
	}
	if store.saveCount() != 1 || refreshCalls.Load() != 1 {
		t.Fatalf("state saves = %d, refresh calls = %d, want 1/1", store.saveCount(), refreshCalls.Load())
	}
}

func TestRefreshAccountForProbePreservesHoldAfterAnotherInstanceRotatesCredentials(t *testing.T) {
	var refreshes atomic.Int32
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var requestStartedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		requestStartedOnce.Do(func() { close(requestStarted) })
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()
	defer func() {
		select {
		case <-releaseRequest:
		default:
			close(releaseRequest)
		}
	}()

	store := newBarrierCredentialStore(domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh"}, 1)
	coordinator := newMemoryCoordinator()
	var callbacks atomic.Int32
	service := &RefreshService{
		OAuth: NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: store, Locks: coordinator, LockTTL: time.Minute,
		OnAccountsChanged: func(context.Context) { callbacks.Add(1) },
	}

	ordinaryResult := make(chan domain.Credentials, 1)
	ordinaryErr := make(chan error, 1)
	go func() {
		credentials, err := service.RefreshAccount(context.Background(), "account")
		ordinaryResult <- credentials
		ordinaryErr <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary refresh did not reach the token endpoint")
	}

	probeResult := make(chan domain.Credentials, 1)
	probeErr := make(chan error, 1)
	go func() {
		credentials, err := service.RefreshAccountForProbe(context.Background(), "account")
		probeResult <- credentials
		probeErr <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for store.reads.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.reads.Load() < 3 {
		t.Fatal("probe refresh did not load the pre-rotation credentials")
	}
	close(releaseRequest)

	if err := <-ordinaryErr; err != nil {
		t.Fatal(err)
	}
	if err := <-probeErr; err != nil {
		t.Fatal(err)
	}
	for _, credentials := range []domain.Credentials{<-ordinaryResult, <-probeResult} {
		if credentials.AccessToken != "new-access" || credentials.RefreshToken != "new-refresh" {
			t.Fatalf("unexpected refreshed credentials: %#v", credentials)
		}
	}
	if refreshes.Load() != 1 || store.saveCount() != 2 || callbacks.Load() != 2 {
		t.Fatalf("refreshes=%d saves=%d callbacks=%d, want 1/2/2", refreshes.Load(), store.saveCount(), callbacks.Load())
	}
	update := store.refreshUpdate()
	if update.Credentials != nil || update.Status != domain.AccountError || update.CooldownUntil != nil || update.LastError != "" {
		t.Fatalf("lock waiter did not preserve the probe hold: %#v", update)
	}
}

func TestRefreshDueUsesCredentialExpiryBoundaryAndCooldown(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name          string
		expiresAt     time.Time
		cooldownUntil *time.Time
		wantRefreshes int32
		wantCallbacks int32
		wantSaves     int
		wantStatus    domain.AccountStatus
	}{
		{name: "inside one-hour window", expiresAt: now.Add(59 * time.Minute), wantRefreshes: 1, wantCallbacks: 1, wantSaves: 1, wantStatus: domain.AccountActive},
		{name: "outside one-hour window", expiresAt: now.Add(61 * time.Minute), wantStatus: domain.AccountActive},
		{name: "already expired", expiresAt: now.Add(-time.Minute), wantRefreshes: 1, wantCallbacks: 1, wantSaves: 1, wantStatus: domain.AccountActive},
		{name: "active cooldown", expiresAt: now.Add(30 * time.Minute), cooldownUntil: timePointer(now.Add(5 * time.Minute)), wantStatus: domain.AccountCooldown},
		{name: "elapsed cooldown before refresh window", expiresAt: now.Add(61 * time.Minute), cooldownUntil: timePointer(now.Add(-time.Minute)), wantCallbacks: 1, wantSaves: 1, wantStatus: domain.AccountActive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var refreshes atomic.Int32
			var callbacks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				refreshes.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
			}))
			defer server.Close()

			store := newBarrierCredentialStore(domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: test.expiresAt}, 1)
			if test.cooldownUntil != nil {
				store.setAccountState(domain.AccountCooldown, test.cooldownUntil, "refresh cooling down")
			}
			service := &RefreshService{
				OAuth: NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
				Store: store, Before: time.Hour, Now: func() time.Time { return now },
				OnAccountsChanged: func(context.Context) { callbacks.Add(1) },
			}
			if err := service.RefreshDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := refreshes.Load(); got != test.wantRefreshes {
				t.Fatalf("upstream refresh count = %d, want %d", got, test.wantRefreshes)
			}
			if got := callbacks.Load(); got != test.wantCallbacks {
				t.Fatalf("account-change callbacks = %d, want %d", got, test.wantCallbacks)
			}
			if got := store.saveCount(); got != test.wantSaves {
				t.Fatalf("state saves = %d, want %d", got, test.wantSaves)
			}
			if got := store.accountState().Status; got != test.wantStatus {
				t.Fatalf("account status = %q, want %q", got, test.wantStatus)
			}
		})
	}
}

func TestRefreshDueBatchesAccountChangeCallback(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	store := newBatchRefreshStore(now.Add(30*time.Minute), 3)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()

	var callbacks atomic.Int32
	var updatesAtCallback atomic.Int32
	service := &RefreshService{
		OAuth: NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
		Store: store, Before: time.Hour, Concurrency: 3, Now: func() time.Time { return now },
		OnAccountsChanged: func(context.Context) {
			callbacks.Add(1)
			updatesAtCallback.Store(int32(store.updateCount()))
		},
	}
	if err := service.RefreshDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 3 || store.updateCount() != 3 || callbacks.Load() != 1 || updatesAtCallback.Load() != 3 {
		t.Fatalf("refreshes=%d updates=%d callbacks=%d updates_at_callback=%d", refreshes.Load(), store.updateCount(), callbacks.Load(), updatesAtCallback.Load())
	}
}

func TestRefreshFailureUpdatesExpiredOrCooldownStateWithRedactedError(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	const responseSecret = "upstream-response-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + responseSecret + `"}`))
	}))
	defer server.Close()

	for _, test := range []struct {
		name       string
		expiresAt  time.Time
		wantStatus domain.AccountStatus
		wantUntil  *time.Time
	}{
		{name: "expired credential", expiresAt: now.Add(-time.Minute), wantStatus: domain.AccountExpired},
		{name: "unexpired credential", expiresAt: now.Add(time.Hour), wantStatus: domain.AccountCooldown, wantUntil: timePointer(now.Add(20 * time.Minute))},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newBarrierCredentialStore(domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: test.expiresAt}, 1)
			service := &RefreshService{
				OAuth: NewOAuthService(OAuthConfig{TokenURL: server.URL, ClientID: "client"}, server.Client()),
				Store: store, FailureCooldown: 20 * time.Minute, Now: func() time.Time { return now },
			}
			_, err := service.RefreshAccount(context.Background(), "account")
			if err == nil || strings.Contains(err.Error(), responseSecret) {
				t.Fatalf("refresh error was missing or exposed upstream content: %v", err)
			}
			update := store.refreshUpdate()
			if update.Status != test.wantStatus || strings.Contains(update.LastError, responseSecret) || update.LastError == "" {
				t.Fatalf("refresh failure state = %#v", update)
			}
			if test.wantUntil == nil && update.CooldownUntil != nil || test.wantUntil != nil && (update.CooldownUntil == nil || !update.CooldownUntil.Equal(*test.wantUntil)) {
				t.Fatalf("refresh cooldown = %v, want %v", update.CooldownUntil, test.wantUntil)
			}
			if store.saveCount() != 1 {
				t.Fatalf("state saves=%d", store.saveCount())
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }

type batchRefreshStore struct {
	mu          sync.Mutex
	accounts    []domain.Account
	credentials map[string]domain.Credentials
	updates     int
}

func newBatchRefreshStore(expiresAt time.Time, count int) *batchRefreshStore {
	result := &batchRefreshStore{credentials: make(map[string]domain.Credentials, count)}
	for index := range count {
		id := fmt.Sprintf("account-%d", index)
		result.accounts = append(result.accounts, domain.Account{ID: id, Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive})
		result.credentials[id] = domain.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: expiresAt}
	}
	return result
}

func (s *batchRefreshStore) ListAccounts(context.Context) ([]domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Account(nil), s.accounts...), nil
}

func (s *batchRefreshStore) Credentials(_ context.Context, id string) (domain.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credentials[id], nil
}

func (s *batchRefreshStore) SaveOAuthRefresh(_ context.Context, id string, update OAuthRefreshUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.Credentials != nil {
		s.credentials[id] = *update.Credentials
	}
	for index := range s.accounts {
		if s.accounts[index].ID == id {
			s.accounts[index].Status = update.Status
			s.accounts[index].CooldownUntil = update.CooldownUntil
			s.accounts[index].LastError = update.LastError
			break
		}
	}
	s.updates++
	return nil
}

func (s *batchRefreshStore) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

type memoryCoordinator struct {
	mu             sync.Mutex
	values         map[string][]byte
	ttls           map[string]time.Duration
	compareDeletes int
	compareMatched bool
}

func newMemoryCoordinator() *memoryCoordinator {
	return &memoryCoordinator{values: make(map[string][]byte), ttls: make(map[string]time.Duration), compareMatched: true}
}

func (c *memoryCoordinator) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.values[key]; exists {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	c.ttls[key] = ttl
	return true, nil
}

func (c *memoryCoordinator) GetDelete(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, exists := c.values[key]
	if !exists {
		return nil, false, nil
	}
	delete(c.values, key)
	delete(c.ttls, key)
	return append([]byte(nil), value...), true, nil
}

func (c *memoryCoordinator) CompareDelete(_ context.Context, key string, expected []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compareDeletes++
	value, exists := c.values[key]
	if !exists || string(value) != string(expected) {
		c.compareMatched = false
		return false, nil
	}
	delete(c.values, key)
	delete(c.ttls, key)
	return true, nil
}

func (c *memoryCoordinator) ttl(key string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ttls[key]
}

func (c *memoryCoordinator) exists(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.values[key]
	return exists
}

func (c *memoryCoordinator) compareDeleteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compareDeletes
}

func (c *memoryCoordinator) allComparesMatched() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compareMatched
}

type barrierCredentialStore struct {
	mu          sync.Mutex
	credentials domain.Credentials
	account     domain.Account
	lastUpdate  OAuthRefreshUpdate
	reads       atomic.Int32
	ready       chan struct{}
	readyOnce   sync.Once
	wantReads   int32
	saves       int
}

func newBarrierCredentialStore(credentials domain.Credentials, wantReads int32) *barrierCredentialStore {
	return &barrierCredentialStore{
		credentials: credentials,
		account:     domain.Account{ID: "account", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive},
		ready:       make(chan struct{}), wantReads: wantReads,
	}
}

func (s *barrierCredentialStore) ListAccounts(context.Context) ([]domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []domain.Account{s.account}, nil
}

func (s *barrierCredentialStore) Credentials(context.Context, string) (domain.Credentials, error) {
	if s.reads.Add(1) >= s.wantReads {
		s.readyOnce.Do(func() { close(s.ready) })
	}
	<-s.ready
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credentials, nil
}

func (s *barrierCredentialStore) SaveOAuthRefresh(_ context.Context, _ string, update OAuthRefreshUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.Credentials != nil {
		s.credentials = *update.Credentials
	}
	s.account.Status = update.Status
	s.account.CooldownUntil = update.CooldownUntil
	s.account.LastError = update.LastError
	s.lastUpdate = update
	s.saves++
	return nil
}

func (s *barrierCredentialStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *barrierCredentialStore) setAccountState(status domain.AccountStatus, cooldownUntil *time.Time, lastError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account.Status = status
	s.account.CooldownUntil = cooldownUntil
	s.account.LastError = lastError
}

func (s *barrierCredentialStore) accountState() domain.Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.account
}

func (s *barrierCredentialStore) refreshUpdate() OAuthRefreshUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUpdate
}
