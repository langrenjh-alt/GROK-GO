package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

var ErrClientKeySecretUnavailable = errors.New("client key secret is unavailable")

type ManagementService struct {
	accounts store.AccountRepository
	models   store.ModelRepository
	proxies  store.ProxyRepository
	keys     store.ClientKeyRepository
	logs     store.RequestLogRepository
	cipher   *security.Cipher
	tokens   *security.TokenManager
	now      func() time.Time
}

func NewManagementService(
	accounts store.AccountRepository,
	models store.ModelRepository,
	proxies store.ProxyRepository,
	keys store.ClientKeyRepository,
	logs store.RequestLogRepository,
	cipher *security.Cipher,
	tokens *security.TokenManager,
) *ManagementService {
	return &ManagementService{
		accounts: accounts, models: models, proxies: proxies, keys: keys,
		logs: logs, cipher: cipher, tokens: tokens, now: time.Now,
	}
}

type CreateAccountInput struct {
	Name             string
	Kind             domain.CredentialKind
	Tier             string
	Status           domain.AccountStatus
	Email            string
	Credentials      domain.Credentials
	ProxyID          string
	Models           []string
	Tags             []string
	Priority         int
	ConcurrencyLimit int
	Quota            domain.QuotaSnapshot
	CooldownUntil    *time.Time
	LastError        string
}

type UpdateAccountInput struct {
	Name             *string
	Kind             *domain.CredentialKind
	Tier             *string
	Status           *domain.AccountStatus
	Email            *string
	Credentials      *domain.Credentials
	ProxyID          *string
	Models           *[]string
	Tags             *[]string
	Priority         *int
	ConcurrencyLimit *int
	Quota            *domain.QuotaSnapshot
	CooldownUntil    **time.Time
	LastError        *string
}

type BatchUpdateAccountsInput struct {
	IDs              []string
	Tier             *string
	Status           *domain.AccountStatus
	ProxyID          *string
	Models           *[]string
	Tags             *[]string
	Priority         *int
	ConcurrencyLimit *int
}

type CreateProxyInput struct {
	Name    string
	URL     string
	Enabled bool
}

type UpdateProxyInput struct {
	Name          *string
	URL           *string
	Enabled       *bool
	Healthy       *bool
	LastCheckedAt **time.Time
	LastError     *string
}

type CreateClientKeyInput struct {
	Name              string
	RPM               int
	ConcurrencyLimit  int
	DailyRequestLimit int64
	MonthlyTokenLimit int64
	ExpiresAt         *time.Time
}

type IssuedClientKey struct {
	Key       domain.ClientKey `json:"key"`
	Plaintext string           `json:"plaintext"`
}

func (s *ManagementService) CreateAccount(ctx context.Context, input CreateAccountInput) (*domain.Account, error) {
	if s.accounts == nil || s.cipher == nil {
		return nil, errors.New("account management is not configured")
	}
	credentials, err := normalizeAccountCredentials(input.Kind, input.Credentials)
	if err != nil {
		return nil, err
	}
	input.Credentials = credentials
	if err := validateAccountInput(input.Name, input.Kind, input.Credentials, input.ConcurrencyLimit); err != nil {
		return nil, err
	}
	proxyID := strings.TrimSpace(input.ProxyID)
	if err := s.validateAccountProxy(ctx, &proxyID); err != nil {
		return nil, err
	}
	id, err := security.GenerateID()
	if err != nil {
		return nil, err
	}
	credentialCipher, err := s.sealCredentials(id, input.Credentials)
	if err != nil {
		return nil, err
	}
	concurrency := input.ConcurrencyLimit
	if concurrency == 0 {
		concurrency = 1
	}
	status := input.Status
	if status == "" {
		status = domain.AccountActive
	}
	if !validAccountStatus(status) {
		return nil, errors.New("invalid account status")
	}
	account := &domain.Account{
		ID: id, Name: strings.TrimSpace(input.Name), Kind: input.Kind, Tier: strings.TrimSpace(input.Tier),
		Status: status, Email: normalizeEmail(input.Email), CredentialCipher: credentialCipher,
		CredentialFingerprint: s.AccountCredentialFingerprint(input.Kind, input.Credentials),
		ProxyID:               proxyID, Models: cleanStrings(input.Models), Tags: cleanStrings(input.Tags),
		Priority: input.Priority, ConcurrencyLimit: concurrency, HealthScore: 1,
		Quota: input.Quota, CooldownUntil: input.CooldownUntil, LastError: sanitizeAccountError(input.LastError),
	}
	if err := s.accounts.CreateAccount(ctx, account); err != nil {
		return nil, err
	}
	return sanitizedAccount(account), nil
}

func (s *ManagementService) GetAccount(ctx context.Context, id string) (*domain.Account, error) {
	account, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	return sanitizedAccount(account), nil
}

func (s *ManagementService) GetAccountCredentials(ctx context.Context, id string) (domain.Credentials, error) {
	account, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return domain.Credentials{}, err
	}
	return s.openCredentials(account.ID, account.CredentialCipher)
}

// AccountCredentialFingerprint returns a keyed, non-reversible identity used
// to make repeated credential imports idempotent.
func (s *ManagementService) AccountCredentialFingerprint(kind domain.CredentialKind, credentials domain.Credentials) []byte {
	if s == nil || s.tokens == nil {
		return nil
	}
	secret := ""
	switch kind {
	case domain.CredentialCLIOAuth:
		secret = firstCredential(credentials.RefreshToken, credentials.AccessToken)
	case domain.CredentialConsoleSSO, domain.CredentialGrokSSO:
		secret = firstCredential(credentials.SSO, credentials.SSORW)
	}
	secret = strings.TrimSpace(strings.TrimPrefix(secret, "sso="))
	if secret == "" {
		return nil
	}
	return s.tokens.Digest("account-credential:v1:" + string(kind) + ":" + secret)
}

func (s *ManagementService) ListAccounts(ctx context.Context, filter store.AccountFilter) ([]domain.Account, error) {
	return s.listAccounts(ctx, filter, false)
}

func (s *ManagementService) ListAccountsWithCredentialExpiry(ctx context.Context, filter store.AccountFilter) ([]domain.Account, error) {
	return s.listAccounts(ctx, filter, true)
}

func (s *ManagementService) listAccounts(ctx context.Context, filter store.AccountFilter, includeCredentialExpiry bool) ([]domain.Account, error) {
	accounts, err := s.accounts.ListAccounts(ctx, filter)
	if err != nil {
		return nil, err
	}
	for index := range accounts {
		if includeCredentialExpiry && accounts[index].Kind == domain.CredentialCLIOAuth {
			credentials, err := s.openCredentials(accounts[index].ID, accounts[index].CredentialCipher)
			if err != nil {
				return nil, err
			}
			if !credentials.ExpiresAt.IsZero() {
				expiresAt := credentials.ExpiresAt.UTC()
				accounts[index].CredentialExpiresAt = &expiresAt
			}
		}
		accounts[index].CredentialCipher = nil
	}
	return accounts, nil
}

func (s *ManagementService) CountAccounts(ctx context.Context, filter store.AccountFilter) (int64, error) {
	return s.accounts.CountAccounts(ctx, filter)
}

func (s *ManagementService) UpdateAccount(ctx context.Context, id string, input UpdateAccountInput) (*domain.Account, error) {
	account, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.validateAccountProxy(ctx, input.ProxyID); err != nil {
		return nil, err
	}
	if err := s.prepareAccountUpdate(account, input); err != nil {
		return nil, err
	}
	if err := s.accounts.UpdateAccount(ctx, account); err != nil {
		return nil, err
	}
	return sanitizedAccount(account), nil
}

func (s *ManagementService) BatchUpdateAccounts(ctx context.Context, input BatchUpdateAccountsInput) ([]domain.Account, error) {
	ids := cleanStrings(input.IDs)
	if len(ids) == 0 || len(ids) > 500 {
		return nil, errors.New("select between 1 and 500 accounts")
	}
	if input.Status == nil && input.Tier == nil && input.ProxyID == nil && input.Models == nil && input.Tags == nil && input.Priority == nil && input.ConcurrencyLimit == nil {
		return nil, errors.New("at least one account field is required")
	}
	patch := UpdateAccountInput{
		Tier: input.Tier, Status: input.Status, ProxyID: input.ProxyID,
		Models: input.Models, Tags: input.Tags, Priority: input.Priority,
		ConcurrencyLimit: input.ConcurrencyLimit,
	}
	if input.Status != nil && !validAccountStatus(*input.Status) {
		return nil, errors.New("invalid account status")
	}
	if input.ConcurrencyLimit != nil && *input.ConcurrencyLimit <= 0 {
		return nil, errors.New("concurrency limit must be positive")
	}
	if err := s.validateAccountProxy(ctx, input.ProxyID); err != nil {
		return nil, err
	}

	accounts := make([]*domain.Account, 0, len(ids))
	for _, id := range ids {
		account, err := s.accounts.GetAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	for _, account := range accounts {
		if err := s.prepareAccountUpdate(account, patch); err != nil {
			return nil, err
		}
	}
	if err := s.accounts.UpdateAccounts(ctx, accounts); err != nil {
		return nil, err
	}
	result := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, *sanitizedAccount(account))
	}
	return result, nil
}

func (s *ManagementService) validateAccountProxy(ctx context.Context, proxyID *string) error {
	if proxyID == nil || strings.TrimSpace(*proxyID) == "" {
		return nil
	}
	if s.proxies == nil {
		return errors.New("proxy management is not configured")
	}
	_, err := s.proxies.GetProxy(ctx, strings.TrimSpace(*proxyID))
	return err
}

func (s *ManagementService) prepareAccountUpdate(account *domain.Account, input UpdateAccountInput) error {
	if account == nil {
		return errors.New("account is required")
	}
	if input.Name != nil {
		account.Name = strings.TrimSpace(*input.Name)
	}
	if input.Kind != nil {
		account.Kind = *input.Kind
	}
	if input.Tier != nil {
		account.Tier = strings.TrimSpace(*input.Tier)
	}
	if input.Status != nil {
		if !validAccountStatus(*input.Status) {
			return errors.New("invalid account status")
		}
		account.Status = *input.Status
		if *input.Status == domain.AccountActive {
			account.CooldownUntil = nil
			account.LastError = ""
			account.HealthScore = 1
			account.FailureCount = 0
		} else if *input.Status == domain.AccountDisabled {
			account.CooldownUntil = nil
		}
	}
	if input.Email != nil {
		account.Email = normalizeEmail(*input.Email)
	}
	if input.ProxyID != nil {
		account.ProxyID = strings.TrimSpace(*input.ProxyID)
	}
	if input.Models != nil {
		account.Models = cleanStrings(*input.Models)
	}
	if input.Tags != nil {
		account.Tags = cleanStrings(*input.Tags)
	}
	if input.Priority != nil {
		account.Priority = *input.Priority
	}
	if input.ConcurrencyLimit != nil {
		if *input.ConcurrencyLimit <= 0 {
			return errors.New("concurrency limit must be positive")
		}
		account.ConcurrencyLimit = *input.ConcurrencyLimit
	}
	if input.Quota != nil {
		account.Quota = *input.Quota
	}
	if input.CooldownUntil != nil {
		account.CooldownUntil = *input.CooldownUntil
	}
	if input.LastError != nil {
		account.LastError = sanitizeAccountError(*input.LastError)
	}
	credentials, err := s.openCredentials(account.ID, account.CredentialCipher)
	if err != nil {
		return err
	}
	if input.Credentials != nil {
		credentials = *input.Credentials
	}
	credentials, err = normalizeAccountCredentials(account.Kind, credentials)
	if err != nil {
		return err
	}
	if input.Credentials != nil || account.CredentialFingerprint == nil {
		account.CredentialCipher, err = s.sealCredentials(account.ID, credentials)
		if err != nil {
			return err
		}
	}
	account.CredentialFingerprint = s.AccountCredentialFingerprint(account.Kind, credentials)
	if err := validateAccountInput(account.Name, account.Kind, credentials, account.ConcurrencyLimit); err != nil {
		return err
	}
	return nil
}

func firstCredential(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sanitizeAccountError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"http://", "https://", "authorization", "bearer ", "access_token", "access token", "access-token",
		"refresh_token", "refresh token", "refresh-token", "id_token", "id token", "id-token", "sso=",
	} {
		if strings.Contains(lower, marker) {
			return "redacted upstream error"
		}
	}
	runes := []rune(value)
	if len(runes) > 256 {
		value = string(runes[:256])
	}
	return value
}

func validAccountStatus(status domain.AccountStatus) bool {
	switch status {
	case domain.AccountActive, domain.AccountCooldown, domain.AccountExpired, domain.AccountDisabled, domain.AccountError:
		return true
	default:
		return false
	}
}

func (s *ManagementService) DeleteAccount(ctx context.Context, id string) error {
	return s.accounts.DeleteAccount(ctx, id)
}

func (s *ManagementService) CreateModel(ctx context.Context, model *domain.ModelSpec) error {
	if err := validateModel(model); err != nil {
		return err
	}
	return s.models.CreateModel(ctx, model)
}
func (s *ManagementService) GetModel(ctx context.Context, id string) (*domain.ModelSpec, error) {
	return s.models.GetModel(ctx, id)
}
func (s *ManagementService) ListModels(ctx context.Context, filter store.ModelFilter) ([]domain.ModelSpec, error) {
	return s.models.ListModels(ctx, filter)
}
func (s *ManagementService) CountModels(ctx context.Context, filter store.ModelFilter) (int64, error) {
	return s.models.CountModels(ctx, filter)
}
func (s *ManagementService) UpdateModel(ctx context.Context, model *domain.ModelSpec) error {
	if err := validateModel(model); err != nil {
		return err
	}
	return s.models.UpdateModel(ctx, model)
}
func (s *ManagementService) DeleteModel(ctx context.Context, id string) error {
	return s.models.DeleteModel(ctx, id)
}

func (s *ManagementService) CreateProxy(ctx context.Context, input CreateProxyInput) (*domain.Proxy, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("proxy name is required")
	}
	normalized, err := validateProxyURL(input.URL)
	if err != nil {
		return nil, err
	}
	id, err := security.GenerateID()
	if err != nil {
		return nil, err
	}
	sealed, err := s.cipher.Seal([]byte(normalized), []byte("proxy-url:"+id))
	if err != nil {
		return nil, err
	}
	proxy := &domain.Proxy{ID: id, Name: strings.TrimSpace(input.Name), URLCipher: sealed, Enabled: input.Enabled}
	if err := s.proxies.CreateProxy(ctx, proxy); err != nil {
		return nil, err
	}
	return sanitizedProxy(proxy), nil
}

func (s *ManagementService) GetProxy(ctx context.Context, id string) (*domain.Proxy, error) {
	proxy, err := s.proxies.GetProxy(ctx, id)
	if err != nil {
		return nil, err
	}
	return sanitizedProxy(proxy), nil
}

func (s *ManagementService) GetProxyURL(ctx context.Context, id string) (string, error) {
	proxy, err := s.proxies.GetProxy(ctx, id)
	if err != nil {
		return "", err
	}
	plaintext, err := s.cipher.Open(proxy.URLCipher, []byte("proxy-url:"+proxy.ID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *ManagementService) ListProxies(ctx context.Context, pagination store.Pagination) ([]domain.Proxy, error) {
	proxies, err := s.proxies.ListProxies(ctx, pagination)
	if err != nil {
		return nil, err
	}
	for index := range proxies {
		proxies[index].URLCipher = nil
	}
	return proxies, nil
}

func (s *ManagementService) CountProxies(ctx context.Context) (int64, error) {
	return s.proxies.CountProxies(ctx)
}

func (s *ManagementService) UpdateProxy(ctx context.Context, id string, input UpdateProxyInput) (*domain.Proxy, error) {
	proxy, err := s.proxies.GetProxy(ctx, id)
	if err != nil {
		return nil, err
	}
	if input.Name != nil {
		proxy.Name = strings.TrimSpace(*input.Name)
	}
	if input.URL != nil {
		normalized, err := validateProxyURL(*input.URL)
		if err != nil {
			return nil, err
		}
		proxy.URLCipher, err = s.cipher.Seal([]byte(normalized), []byte("proxy-url:"+proxy.ID))
		if err != nil {
			return nil, err
		}
	}
	if input.Enabled != nil {
		proxy.Enabled = *input.Enabled
	}
	if input.Healthy != nil {
		proxy.Healthy = *input.Healthy
	}
	if input.LastCheckedAt != nil {
		proxy.LastCheckedAt = *input.LastCheckedAt
	}
	if input.LastError != nil {
		proxy.LastError = strings.TrimSpace(*input.LastError)
	}
	if proxy.Name == "" {
		return nil, errors.New("proxy name is required")
	}
	if err := s.proxies.UpdateProxy(ctx, proxy); err != nil {
		return nil, err
	}
	return sanitizedProxy(proxy), nil
}

func (s *ManagementService) DeleteProxy(ctx context.Context, id string) error {
	return s.proxies.DeleteProxy(ctx, id)
}

func (s *ManagementService) CreateClientKey(ctx context.Context, input CreateClientKeyInput) (*IssuedClientKey, error) {
	if s.keys == nil || s.tokens == nil || s.cipher == nil {
		return nil, errors.New("client key management is not configured")
	}
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("client key name is required")
	}
	if input.RPM < 0 || input.ConcurrencyLimit < 0 || input.DailyRequestLimit < 0 || input.MonthlyTokenLimit < 0 {
		return nil, errors.New("client key limits must not be negative")
	}
	material, err := security.GenerateClientAPIKey(s.tokens)
	if err != nil {
		return nil, err
	}
	secretCipher, err := s.cipher.Seal([]byte(material.Plaintext), clientKeyAAD(material.Digest))
	if err != nil {
		return nil, fmt.Errorf("encrypt client key: %w", err)
	}
	key := domain.ClientKey{
		Name: strings.TrimSpace(input.Name), Prefix: material.Prefix, Digest: material.Digest,
		SecretCipher: secretCipher, SecretAvailable: true, Enabled: true,
		RPM: input.RPM, ConcurrencyLimit: input.ConcurrencyLimit,
		DailyRequestLimit: input.DailyRequestLimit, MonthlyTokenLimit: input.MonthlyTokenLimit,
		ExpiresAt: input.ExpiresAt,
	}
	if err := s.keys.CreateClientKey(ctx, &key); err != nil {
		return nil, err
	}
	return &IssuedClientKey{Key: key, Plaintext: material.Plaintext}, nil
}

func (s *ManagementService) AuthenticateClientKey(ctx context.Context, plaintext string) (*domain.ClientKey, error) {
	key, err := s.keys.GetClientKeyByDigest(ctx, s.tokens.Digest(strings.TrimSpace(plaintext)))
	if err != nil || !key.Enabled || key.ExpiresAt != nil && !key.ExpiresAt.After(s.now()) {
		return nil, ErrInvalidCredentials
	}
	_ = s.keys.TouchClientKey(ctx, key.ID, s.now().UTC())
	key.Digest = nil
	key.SecretCipher = nil
	return key, nil
}

func (s *ManagementService) RevealClientKey(ctx context.Context, id string) (string, error) {
	if s.keys == nil || s.tokens == nil || s.cipher == nil {
		return "", errors.New("client key management is not configured")
	}
	key, err := s.keys.GetClientKey(ctx, strings.TrimSpace(id))
	if err != nil {
		return "", err
	}
	if len(key.Digest) == 0 || len(key.SecretCipher) == 0 {
		return "", ErrClientKeySecretUnavailable
	}
	plaintext, err := s.cipher.Open(key.SecretCipher, clientKeyAAD(key.Digest))
	if err != nil || !s.tokens.Verify(string(plaintext), key.Digest) {
		return "", ErrClientKeySecretUnavailable
	}
	return string(plaintext), nil
}

func (s *ManagementService) GetClientKey(ctx context.Context, id string) (*domain.ClientKey, error) {
	key, err := s.keys.GetClientKey(ctx, id)
	if key != nil {
		key.SecretAvailable = len(key.SecretCipher) > 0
		key.Digest = nil
		key.SecretCipher = nil
	}
	return key, err
}

func (s *ManagementService) ListClientKeys(ctx context.Context, pagination store.Pagination) ([]domain.ClientKey, error) {
	keys, err := s.keys.ListClientKeys(ctx, pagination)
	if err != nil {
		return nil, err
	}
	for index := range keys {
		keys[index].SecretAvailable = len(keys[index].SecretCipher) > 0
		keys[index].Digest = nil
		keys[index].SecretCipher = nil
	}
	return keys, nil
}

func (s *ManagementService) CountClientKeys(ctx context.Context) (int64, error) {
	return s.keys.CountClientKeys(ctx)
}

func (s *ManagementService) CountActiveClientKeys(ctx context.Context, now time.Time) (int64, error) {
	return s.keys.CountActiveClientKeys(ctx, now)
}

func (s *ManagementService) UpdateClientKey(ctx context.Context, key *domain.ClientKey) error {
	if key == nil {
		return errors.New("client key is required")
	}
	key.Digest = nil
	return s.keys.UpdateClientKey(ctx, key)
}
func (s *ManagementService) DeleteClientKey(ctx context.Context, id string) error {
	return s.keys.DeleteClientKey(ctx, id)
}

func clientKeyAAD(digest []byte) []byte {
	aad := make([]byte, len("grok-go:client-key:")+len(digest))
	copy(aad, "grok-go:client-key:")
	copy(aad[len("grok-go:client-key:"):], digest)
	return aad
}

func (s *ManagementService) CreateRequestLog(ctx context.Context, log *domain.RequestLog) error {
	return s.logs.CreateRequestLog(ctx, log)
}
func (s *ManagementService) GetRequestLog(ctx context.Context, id string) (*domain.RequestLog, error) {
	return s.logs.GetRequestLog(ctx, id)
}
func (s *ManagementService) ListRequestLogs(ctx context.Context, filter store.RequestLogFilter) ([]domain.RequestLog, error) {
	return s.logs.ListRequestLogs(ctx, filter)
}
func (s *ManagementService) CountRequestLogs(ctx context.Context, filter store.RequestLogFilter) (int64, error) {
	return s.logs.CountRequestLogs(ctx, filter)
}
func (s *ManagementService) GetRequestLogStats(ctx context.Context, from, to time.Time) (*store.RequestLogStats, error) {
	return s.logs.GetRequestLogStats(ctx, from, to)
}
func (s *ManagementService) DeleteRequestLog(ctx context.Context, id string) error {
	return s.logs.DeleteRequestLog(ctx, id)
}
func (s *ManagementService) DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	return s.logs.DeleteRequestLogsBefore(ctx, before)
}

func (s *ManagementService) sealCredentials(accountID string, credentials domain.Credentials) ([]byte, error) {
	payload, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("encode credentials: %w", err)
	}
	return s.cipher.Seal(payload, []byte("account-credentials:"+accountID))
}

func (s *ManagementService) openCredentials(accountID string, ciphertext []byte) (domain.Credentials, error) {
	plaintext, err := s.cipher.Open(ciphertext, []byte("account-credentials:"+accountID))
	if err != nil {
		return domain.Credentials{}, fmt.Errorf("decrypt account credentials: %w", err)
	}
	var credentials domain.Credentials
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return domain.Credentials{}, fmt.Errorf("decode account credentials: %w", err)
	}
	return credentials, nil
}

func validateAccountInput(name string, kind domain.CredentialKind, credentials domain.Credentials, concurrency int) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("account name is required")
	}
	switch kind {
	case domain.CredentialCLIOAuth, domain.CredentialConsoleSSO, domain.CredentialGrokSSO:
	default:
		return errors.New("unsupported credential kind")
	}
	if concurrency < 0 {
		return errors.New("concurrency limit must be positive")
	}
	if strings.TrimSpace(credentials.AccessToken) == "" && strings.TrimSpace(credentials.RefreshToken) == "" && strings.TrimSpace(credentials.SSO) == "" && strings.TrimSpace(credentials.SSORW) == "" {
		return errors.New("account credentials are empty")
	}
	return nil
}

func normalizeAccountCredentials(kind domain.CredentialKind, credentials domain.Credentials) (domain.Credentials, error) {
	credentials.AccessToken = strings.TrimSpace(credentials.AccessToken)
	credentials.RefreshToken = strings.TrimSpace(credentials.RefreshToken)
	credentials.IDToken = strings.TrimSpace(credentials.IDToken)
	credentials.SSO = strings.TrimSpace(strings.TrimPrefix(credentials.SSO, "sso="))
	credentials.SSORW = strings.TrimSpace(strings.TrimPrefix(credentials.SSORW, "sso="))
	credentials.BaseURL = strings.TrimSpace(credentials.BaseURL)
	switch kind {
	case domain.CredentialCLIOAuth:
		if credentials.TokenType == "" {
			credentials.TokenType = "Bearer"
		} else if !strings.EqualFold(strings.TrimSpace(credentials.TokenType), "Bearer") {
			return domain.Credentials{}, errors.New("CLI OAuth token type must be Bearer")
		} else {
			credentials.TokenType = "Bearer"
		}
		if credentials.BaseURL == "" {
			credentials.BaseURL = "https://cli-chat-proxy.grok.com/v1"
		} else if err := requireOfficialCredentialURL(credentials.BaseURL, map[string]string{
			"cli-chat-proxy.grok.com": "/v1",
			"api.x.ai":                "/v1",
		}); err != nil {
			return domain.Credentials{}, errors.New("CLI OAuth base URL must use an official xAI endpoint")
		} else {
			credentials.BaseURL = "https://cli-chat-proxy.grok.com/v1"
		}
	case domain.CredentialConsoleSSO:
		if credentials.BaseURL != "" {
			if err := requireOfficialCredentialURL(credentials.BaseURL, map[string]string{"console.x.ai": "/v1"}); err != nil {
				return domain.Credentials{}, errors.New("Console SSO base URL must use the official xAI endpoint")
			}
			credentials.BaseURL = "https://console.x.ai/v1"
		}
	case domain.CredentialGrokSSO:
		if credentials.BaseURL != "" {
			if err := requireOfficialCredentialURL(credentials.BaseURL, map[string]string{"grok.com": ""}); err != nil {
				return domain.Credentials{}, errors.New("Grok SSO base URL must use the official Grok endpoint")
			}
			credentials.BaseURL = "https://grok.com"
		}
	}
	return credentials, nil
}

func requireOfficialCredentialURL(raw string, allowed map[string]string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("invalid credential URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Host, parsed.Hostname()) {
		return errors.New("invalid credential URL")
	}
	wantPath, ok := allowed[strings.ToLower(parsed.Hostname())]
	if !ok || strings.TrimRight(parsed.Path, "/") != wantPath {
		return errors.New("invalid credential URL")
	}
	return nil
}

func validateModel(model *domain.ModelSpec) error {
	if model == nil || strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.UpstreamModel) == "" || strings.TrimSpace(model.DisplayName) == "" {
		return errors.New("model ID, upstream model, and display name are required")
	}
	switch model.Capability {
	case domain.CapabilityChat, domain.CapabilityResponses, domain.CapabilityMessages, domain.CapabilityImage, domain.CapabilityImageEdit, domain.CapabilityVideo:
	default:
		return errors.New("unsupported model capability")
	}
	return nil
}

func validateProxyURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil && parsed.User.Username() == "" {
		return "", errors.New("invalid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", errors.New("proxy URL must use http, https, socks5, or socks5h")
	}
	return parsed.String(), nil
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sanitizedAccount(account *domain.Account) *domain.Account {
	copy := *account
	copy.CredentialCipher = nil
	copy.CredentialFingerprint = nil
	return &copy
}

func sanitizedProxy(proxy *domain.Proxy) *domain.Proxy {
	copy := *proxy
	copy.URLCipher = nil
	return &copy
}
