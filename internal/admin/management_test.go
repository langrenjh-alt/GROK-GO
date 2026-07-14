package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func TestManagementEncryptsCredentialsAndIssuesClientKey(t *testing.T) {
	accounts := &fakeAccountRepo{values: map[string]*domain.Account{}}
	keys := &fakeClientKeyRepo{byID: map[string]*domain.ClientKey{}, byDigest: map[string]*domain.ClientKey{}}
	cipher, _ := security.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	tokens, _ := security.NewTokenManager([]byte("abcdef0123456789abcdef0123456789"))
	service := NewManagementService(accounts, nil, nil, keys, nil, cipher, tokens)

	created, err := service.CreateAccount(context.Background(), CreateAccountInput{
		Name: "Primary", Kind: domain.CredentialCLIOAuth,
		Credentials: domain.Credentials{AccessToken: "secret-access", RefreshToken: "secret-refresh"},
		Models:      []string{"grok-4.5", "grok-4.5"},
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if len(created.CredentialCipher) != 0 || len(accounts.values[created.ID].CredentialCipher) == 0 {
		t.Fatalf("credential redaction/encryption failed: returned=%d stored=%d", len(created.CredentialCipher), len(accounts.values[created.ID].CredentialCipher))
	}
	credentials, err := service.GetAccountCredentials(context.Background(), created.ID)
	if err != nil || credentials.AccessToken != "secret-access" {
		t.Fatalf("GetAccountCredentials() = %+v, %v", credentials, err)
	}

	issued, err := service.CreateClientKey(context.Background(), CreateClientKeyInput{Name: "CI"})
	if err != nil {
		t.Fatalf("CreateClientKey() error = %v", err)
	}
	if issued.Plaintext == "" || len(issued.Key.Digest) == 0 || len(issued.Key.SecretCipher) == 0 {
		t.Fatalf("issued key = %+v", issued)
	}
	revealed, err := service.RevealClientKey(context.Background(), issued.Key.ID)
	if err != nil || revealed != issued.Plaintext {
		t.Fatalf("RevealClientKey() = %q, %v", revealed, err)
	}
	listed, err := service.ListClientKeys(context.Background(), store.Pagination{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListClientKeys() = %+v, %v", listed, err)
	}
	for _, item := range listed {
		if !item.SecretAvailable || len(item.SecretCipher) != 0 || len(item.Digest) != 0 {
			t.Fatalf("listed key secret metadata = %+v", item)
		}
	}
	if issued.Key.RPM != 0 || issued.Key.ConcurrencyLimit != 0 || issued.Key.DailyRequestLimit != 0 || issued.Key.MonthlyTokenLimit != 0 {
		t.Fatalf("default key limits = %+v, want unlimited zero values", issued.Key)
	}
	limited, err := service.CreateClientKey(context.Background(), CreateClientKeyInput{
		Name: "Limited", RPM: 60, ConcurrencyLimit: 4,
		DailyRequestLimit: 1_000, MonthlyTokenLimit: 10_000,
	})
	if err != nil {
		t.Fatalf("CreateClientKey(limited) error = %v", err)
	}
	if limited.Key.RPM != 60 || limited.Key.ConcurrencyLimit != 4 || limited.Key.DailyRequestLimit != 1_000 || limited.Key.MonthlyTokenLimit != 10_000 {
		t.Fatalf("explicit key limits = %+v", limited.Key)
	}
	authenticated, err := service.AuthenticateClientKey(context.Background(), issued.Plaintext)
	if err != nil || authenticated.ID != issued.Key.ID || len(authenticated.Digest) != 0 {
		t.Fatalf("AuthenticateClientKey() = %+v, %v", authenticated, err)
	}
	if _, err := service.AuthenticateClientKey(context.Background(), issued.Plaintext+"x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("AuthenticateClientKey(wrong) error = %v", err)
	}
}

func TestValidateModelAcceptsImageEditCapability(t *testing.T) {
	model := &domain.ModelSpec{ID: "grok-imagine-image-edit", UpstreamModel: "auto", DisplayName: "Grok Imagine Image Edit", Capability: domain.CapabilityImageEdit}
	if err := validateModel(model); err != nil {
		t.Fatalf("validateModel(image edit) error = %v", err)
	}
	model.Capability = "audio"
	if err := validateModel(model); err == nil {
		t.Fatal("validateModel accepted unsupported capability")
	}
}

func TestManagementPinsCredentialOriginsAndFingerprintsSecrets(t *testing.T) {
	repository := &fakeAccountRepo{values: map[string]*domain.Account{}}
	cipher, _ := security.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	tokens, _ := security.NewTokenManager([]byte("abcdef0123456789abcdef0123456789"))
	service := NewManagementService(repository, nil, nil, nil, nil, cipher, tokens)

	created, err := service.CreateAccount(context.Background(), CreateAccountInput{
		Name: "OAuth", Kind: domain.CredentialCLIOAuth,
		Credentials: domain.Credentials{AccessToken: "access-one", RefreshToken: "stable-refresh", BaseURL: "https://api.x.ai/v1", TokenType: "bearer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := repository.values[created.ID]
	if len(stored.CredentialFingerprint) == 0 || len(created.CredentialFingerprint) != 0 {
		t.Fatalf("fingerprint storage/redaction = stored:%d returned:%d", len(stored.CredentialFingerprint), len(created.CredentialFingerprint))
	}
	credentials, err := service.GetAccountCredentials(context.Background(), created.ID)
	if err != nil || credentials.BaseURL != "https://cli-chat-proxy.grok.com/v1" || credentials.TokenType != "Bearer" {
		t.Fatalf("normalized credentials = %+v, %v", credentials, err)
	}
	firstFingerprint := append([]byte(nil), stored.CredentialFingerprint...)
	credentials.AccessToken = "access-two"
	if _, err := service.UpdateAccount(context.Background(), created.ID, UpdateAccountInput{Credentials: &credentials}); err != nil {
		t.Fatal(err)
	}
	if string(repository.values[created.ID].CredentialFingerprint) != string(firstFingerprint) {
		t.Fatal("access-token rotation changed the refresh-token credential identity")
	}

	for _, test := range []CreateAccountInput{
		{Name: "Bad OAuth", Kind: domain.CredentialCLIOAuth, Credentials: domain.Credentials{AccessToken: "a", BaseURL: "https://example.test/v1"}},
		{Name: "Bad Console", Kind: domain.CredentialConsoleSSO, Credentials: domain.Credentials{SSO: "s", BaseURL: "https://example.test/v1"}},
		{Name: "Bad Grok", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "s", BaseURL: "https://example.test"}},
	} {
		if _, err := service.CreateAccount(context.Background(), test); err == nil {
			t.Fatalf("accepted custom credential origin for %s", test.Kind)
		}
	}
}

func TestSanitizeAccountErrorRedactsSecretsURLsAndBoundsText(t *testing.T) {
	for _, value := range []string{
		`Post "https://grok.com/path": dial failed`,
		"Authorization: Bearer secret",
		"refresh_token=secret",
		"sso=secret",
	} {
		if got := sanitizeAccountError(value); got != "redacted upstream error" {
			t.Fatalf("sanitizeAccountError(%q) = %q", value, got)
		}
	}
	plain := strings.Repeat("x", 300)
	if got := sanitizeAccountError(plain); len([]rune(got)) != 256 {
		t.Fatalf("bounded account error length = %d", len([]rune(got)))
	}
	if got := sanitizeAccountError("rate limited"); got != "rate limited" {
		t.Fatalf("plain account error = %q", got)
	}
}

func TestBatchUpdateAccountsPrevalidatesAndWritesAtomically(t *testing.T) {
	accounts := &fakeAccountRepo{values: map[string]*domain.Account{}}
	proxies := &fakeProxyRepo{values: map[string]*domain.Proxy{"proxy-1": {ID: "proxy-1", Name: "Primary"}}}
	cipher, _ := security.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	service := NewManagementService(accounts, nil, proxies, nil, nil, cipher, nil)
	if _, err := service.CreateAccount(context.Background(), CreateAccountInput{Name: "Missing proxy", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "missing-proxy"}, ProxyID: "missing"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("create account missing proxy error = %v", err)
	}
	first, err := service.CreateAccount(context.Background(), CreateAccountInput{Name: "First", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "first"}, ConcurrencyLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateAccount(context.Background(), CreateAccountInput{Name: "Second", Kind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "second"}, ConcurrencyLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	priority := 42
	missing := "missing"
	invalidConcurrency := 0
	if _, err := service.BatchUpdateAccounts(context.Background(), BatchUpdateAccountsInput{IDs: []string{first.ID, second.ID}, ConcurrencyLimit: &invalidConcurrency}); err == nil {
		t.Fatal("batch update accepted invalid concurrency")
	}
	if accounts.batchCalls != 0 || accounts.values[first.ID].ConcurrencyLimit != 1 || accounts.values[second.ID].ConcurrencyLimit != 1 {
		t.Fatalf("invalid-field validation wrote partial state: calls=%d", accounts.batchCalls)
	}

	if _, err := service.BatchUpdateAccounts(context.Background(), BatchUpdateAccountsInput{IDs: []string{first.ID, "missing-account"}, Priority: &priority}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing account error = %v", err)
	}
	if accounts.batchCalls != 0 || accounts.values[first.ID].Priority != 0 {
		t.Fatalf("missing-account validation wrote partial state: calls=%d account=%+v", accounts.batchCalls, accounts.values[first.ID])
	}
	if _, err := service.BatchUpdateAccounts(context.Background(), BatchUpdateAccountsInput{IDs: []string{first.ID, second.ID}, ProxyID: &missing}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing proxy error = %v", err)
	}
	if accounts.batchCalls != 0 || accounts.values[first.ID].ProxyID != "" || accounts.values[second.ID].ProxyID != "" {
		t.Fatalf("missing-proxy validation wrote partial state: calls=%d", accounts.batchCalls)
	}

	proxyID := "proxy-1"
	concurrency := 8
	status := domain.AccountDisabled
	items, err := service.BatchUpdateAccounts(context.Background(), BatchUpdateAccountsInput{
		IDs: []string{first.ID, second.ID, first.ID}, ProxyID: &proxyID,
		Priority: &priority, ConcurrencyLimit: &concurrency, Status: &status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accounts.batchCalls != 1 || len(items) != 2 {
		t.Fatalf("batch result = %+v, calls=%d", items, accounts.batchCalls)
	}
	for _, id := range []string{first.ID, second.ID} {
		stored := accounts.values[id]
		if stored.Priority != priority || stored.ConcurrencyLimit != concurrency || stored.ProxyID != proxyID || stored.Status != domain.AccountDisabled {
			t.Fatalf("updated account %s = %+v", id, stored)
		}
	}
	for _, item := range items {
		if len(item.CredentialCipher) != 0 {
			t.Fatalf("batch response leaked credentials: %+v", item)
		}
	}

	accounts.batchErr = errors.New("transaction failed")
	newPriority := 99
	if _, err := service.BatchUpdateAccounts(context.Background(), BatchUpdateAccountsInput{IDs: []string{first.ID, second.ID}, Priority: &newPriority}); err == nil {
		t.Fatal("repository transaction failure was ignored")
	}
	if accounts.values[first.ID].Priority != priority || accounts.values[second.ID].Priority != priority {
		t.Fatalf("repository failure left partial state: first=%+v second=%+v", accounts.values[first.ID], accounts.values[second.ID])
	}
}

type fakeAccountRepo struct {
	values     map[string]*domain.Account
	batchErr   error
	batchCalls int
}

func (r *fakeAccountRepo) CreateAccount(_ context.Context, account *domain.Account) error {
	copy := *account
	copy.CredentialCipher = append([]byte(nil), account.CredentialCipher...)
	now := time.Now()
	copy.CreatedAt, copy.UpdatedAt = now, now
	r.values[account.ID] = &copy
	account.CreatedAt, account.UpdatedAt = now, now
	return nil
}
func (r *fakeAccountRepo) GetAccount(_ context.Context, id string) (*domain.Account, error) {
	value, ok := r.values[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	copy.CredentialCipher = append([]byte(nil), value.CredentialCipher...)
	return &copy, nil
}
func (r *fakeAccountRepo) ListAccounts(context.Context, store.AccountFilter) ([]domain.Account, error) {
	out := make([]domain.Account, 0, len(r.values))
	for _, value := range r.values {
		out = append(out, *value)
	}
	return out, nil
}
func (r *fakeAccountRepo) CountAccounts(context.Context, store.AccountFilter) (int64, error) {
	return int64(len(r.values)), nil
}
func (r *fakeAccountRepo) UpdateAccount(_ context.Context, account *domain.Account) error {
	if _, ok := r.values[account.ID]; !ok {
		return store.ErrNotFound
	}
	copy := *account
	r.values[account.ID] = &copy
	return nil
}
func (r *fakeAccountRepo) UpdateAccounts(_ context.Context, accounts []*domain.Account) error {
	r.batchCalls++
	if r.batchErr != nil {
		return r.batchErr
	}
	for _, account := range accounts {
		if account == nil {
			return errors.New("account is required")
		}
		if _, ok := r.values[account.ID]; !ok {
			return store.ErrNotFound
		}
	}
	updates := make(map[string]*domain.Account, len(accounts))
	for _, account := range accounts {
		copy := *account
		copy.CredentialCipher = append([]byte(nil), account.CredentialCipher...)
		copy.Models = append([]string(nil), account.Models...)
		copy.Tags = append([]string(nil), account.Tags...)
		updates[account.ID] = &copy
	}
	for id, account := range updates {
		r.values[id] = account
	}
	return nil
}
func (r *fakeAccountRepo) DeleteAccount(_ context.Context, id string) error {
	if _, ok := r.values[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.values, id)
	return nil
}

type fakeClientKeyRepo struct {
	byID     map[string]*domain.ClientKey
	byDigest map[string]*domain.ClientKey
}

type fakeProxyRepo struct{ values map[string]*domain.Proxy }

func (r *fakeProxyRepo) CreateProxy(_ context.Context, proxy *domain.Proxy) error {
	copy := *proxy
	r.values[proxy.ID] = &copy
	return nil
}
func (r *fakeProxyRepo) GetProxy(_ context.Context, id string) (*domain.Proxy, error) {
	proxy, ok := r.values[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *proxy
	return &copy, nil
}
func (r *fakeProxyRepo) ListProxies(context.Context, store.Pagination) ([]domain.Proxy, error) {
	result := make([]domain.Proxy, 0, len(r.values))
	for _, proxy := range r.values {
		result = append(result, *proxy)
	}
	return result, nil
}
func (r *fakeProxyRepo) CountProxies(context.Context) (int64, error) {
	return int64(len(r.values)), nil
}
func (r *fakeProxyRepo) UpdateProxy(_ context.Context, proxy *domain.Proxy) error {
	if _, ok := r.values[proxy.ID]; !ok {
		return store.ErrNotFound
	}
	copy := *proxy
	r.values[proxy.ID] = &copy
	return nil
}
func (r *fakeProxyRepo) DeleteProxy(_ context.Context, id string) error {
	if _, ok := r.values[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.values, id)
	return nil
}

func (r *fakeClientKeyRepo) CreateClientKey(_ context.Context, key *domain.ClientKey) error {
	if key.ID == "" {
		key.ID = "key-1"
	}
	now := time.Now()
	key.CreatedAt, key.UpdatedAt = now, now
	copy := *key
	copy.Digest = append([]byte(nil), key.Digest...)
	copy.SecretCipher = append([]byte(nil), key.SecretCipher...)
	r.byID[key.ID] = &copy
	r.byDigest[string(key.Digest)] = &copy
	return nil
}
func (r *fakeClientKeyRepo) GetClientKey(_ context.Context, id string) (*domain.ClientKey, error) {
	value, ok := r.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	copy.Digest = append([]byte(nil), value.Digest...)
	copy.SecretCipher = append([]byte(nil), value.SecretCipher...)
	return &copy, nil
}
func (r *fakeClientKeyRepo) GetClientKeyByDigest(_ context.Context, digest []byte) (*domain.ClientKey, error) {
	value, ok := r.byDigest[string(digest)]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *fakeClientKeyRepo) ListClientKeys(context.Context, store.Pagination) ([]domain.ClientKey, error) {
	out := make([]domain.ClientKey, 0, len(r.byID))
	for _, value := range r.byID {
		out = append(out, *value)
	}
	return out, nil
}
func (r *fakeClientKeyRepo) CountClientKeys(context.Context) (int64, error) {
	return int64(len(r.byID)), nil
}
func (r *fakeClientKeyRepo) CountActiveClientKeys(_ context.Context, now time.Time) (int64, error) {
	var total int64
	for _, value := range r.byID {
		if value.Enabled && (value.ExpiresAt == nil || value.ExpiresAt.After(now)) {
			total++
		}
	}
	return total, nil
}
func (r *fakeClientKeyRepo) UpdateClientKey(_ context.Context, key *domain.ClientKey) error {
	value, ok := r.byID[key.ID]
	if !ok {
		return store.ErrNotFound
	}
	value.Name, value.Enabled = key.Name, key.Enabled
	return nil
}
func (r *fakeClientKeyRepo) TouchClientKey(_ context.Context, id string, at time.Time) error {
	value, ok := r.byID[id]
	if !ok {
		return store.ErrNotFound
	}
	value.LastUsedAt = &at
	return nil
}
func (r *fakeClientKeyRepo) DeleteClientKey(_ context.Context, id string) error {
	if _, ok := r.byID[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}
