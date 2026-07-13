package upstream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type OAuthConfig struct {
	AuthorizationURL string
	TokenURL         string
	ClientID         string
	RedirectURL      string
	Scope            string
	ExtraAuth        url.Values
	ExtraToken       url.Values
	StateTTL         time.Duration
}

type OAuthSession struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
	Verifier         string `json:"verifier,omitempty"`
	Challenge        string `json:"challenge"`
}

var (
	ErrOAuthStateInUse   = errors.New("OAuth state is already pending")
	ErrOAuthStateInvalid = errors.New("OAuth state is invalid or expired")
)

type OAuthStateStore interface {
	SetNX(context.Context, string, []byte, time.Duration) (bool, error)
	GetDelete(context.Context, string) ([]byte, bool, error)
}

type DistributedLockStore interface {
	SetNX(context.Context, string, []byte, time.Duration) (bool, error)
	CompareDelete(context.Context, string, []byte) (bool, error)
}

type OAuthService struct {
	config     OAuthConfig
	client     *http.Client
	stateStore OAuthStateStore
	now        func() time.Time
}

func NewOAuthService(config OAuthConfig, client *http.Client, stateStores ...OAuthStateStore) *OAuthService {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var stateStore OAuthStateStore
	if len(stateStores) > 0 {
		stateStore = stateStores[0]
	}
	return &OAuthService{config: config, client: client, stateStore: stateStore, now: time.Now}
}

func (s *OAuthService) Begin(state string) (OAuthSession, error) {
	return s.BeginContext(context.Background(), state)
}

func (s *OAuthService) BeginContext(ctx context.Context, state string) (OAuthSession, error) {
	if s.config.AuthorizationURL == "" || s.config.ClientID == "" || s.config.RedirectURL == "" {
		return OAuthSession{}, errors.New("OAuth authorization configuration is incomplete")
	}
	if state == "" {
		state = randomToken(24)
	}
	verifier := randomToken(64)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	parsed, err := url.Parse(s.config.AuthorizationURL)
	if err != nil {
		return OAuthSession{}, err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", s.config.ClientID)
	query.Set("redirect_uri", s.config.RedirectURL)
	query.Set("scope", s.config.Scope)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	copyValues(query, s.config.ExtraAuth)
	parsed.RawQuery = query.Encode()
	session := OAuthSession{AuthorizationURL: parsed.String(), State: state, Challenge: challenge}
	if s.stateStore == nil {
		// Preserve the original verifier-based contract for embedded users without a coordinator.
		session.Verifier = verifier
		return session, nil
	}
	ttl := s.config.StateTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	stored, err := s.stateStore.SetNX(ctx, oauthStateKey(state), []byte(verifier), ttl)
	if err != nil {
		return OAuthSession{}, fmt.Errorf("store OAuth state: %w", err)
	}
	if !stored {
		return OAuthSession{}, ErrOAuthStateInUse
	}
	return session, nil
}

func (s *OAuthService) Exchange(ctx context.Context, code, verifier string) (domain.Credentials, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(verifier) == "" {
		return domain.Credentials{}, errors.New("OAuth code and PKCE verifier are required")
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {s.config.ClientID},
		"redirect_uri":  {s.config.RedirectURL},
	}
	copyValues(values, s.config.ExtraToken)
	return s.tokenRequest(ctx, values, domain.Credentials{})
}

func (s *OAuthService) ExchangeState(ctx context.Context, code, state string) (domain.Credentials, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(state) == "" {
		return domain.Credentials{}, errors.New("OAuth code and state are required")
	}
	if s.stateStore == nil {
		return domain.Credentials{}, ErrOAuthStateInvalid
	}
	verifier, found, err := s.stateStore.GetDelete(ctx, oauthStateKey(state))
	if err != nil {
		return domain.Credentials{}, fmt.Errorf("consume OAuth state: %w", err)
	}
	if !found || len(verifier) == 0 {
		return domain.Credentials{}, ErrOAuthStateInvalid
	}
	return s.Exchange(ctx, code, string(verifier))
}

func (s *OAuthService) Refresh(ctx context.Context, credentials domain.Credentials) (domain.Credentials, error) {
	if credentials.RefreshToken == "" {
		return domain.Credentials{}, errors.New("OAuth refresh token is empty")
	}
	clientID := credentials.ClientID
	if clientID == "" {
		clientID = s.config.ClientID
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {credentials.RefreshToken},
		"client_id":     {clientID},
	}
	copyValues(values, s.config.ExtraToken)
	return s.tokenRequest(ctx, values, credentials)
}

func (s *OAuthService) tokenRequest(ctx context.Context, values url.Values, previous domain.Credentials) (domain.Credentials, error) {
	if s.config.TokenURL == "" {
		return domain.Credentials{}, errors.New("OAuth token URL is empty")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return domain.Credentials{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return domain.Credentials{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return domain.Credentials{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.Credentials{}, &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    any    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return domain.Credentials{}, err
	}
	if payload.AccessToken == "" {
		return domain.Credentials{}, errors.New("OAuth response did not contain an access token")
	}
	result := previous
	result.AccessToken = payload.AccessToken
	if payload.RefreshToken != "" {
		result.RefreshToken = payload.RefreshToken
	}
	if payload.IDToken != "" {
		result.IDToken = payload.IDToken
	}
	if payload.TokenType != "" {
		result.TokenType = payload.TokenType
	}
	if payload.Scope != "" {
		result.Scope = payload.Scope
	}
	if result.ClientID == "" {
		result.ClientID = s.config.ClientID
	}
	if expiresIn := number(payload.ExpiresIn); expiresIn > 0 {
		result.ExpiresAt = s.now().Add(time.Duration(expiresIn) * time.Second)
	}
	return result, nil
}

type OAuthCredentialStore interface {
	ListAccounts(context.Context) ([]domain.Account, error)
	Credentials(context.Context, string) (domain.Credentials, error)
	SaveOAuthRefresh(context.Context, string, OAuthRefreshUpdate) error
}

type OAuthRefreshUpdate struct {
	Credentials   *domain.Credentials
	Status        domain.AccountStatus
	CooldownUntil *time.Time
	LastError     string
}

type RefreshService struct {
	OAuth             *OAuthService
	Store             OAuthCredentialStore
	Locks             DistributedLockStore
	Before            time.Duration
	Interval          time.Duration
	Concurrency       int
	LockTTL           time.Duration
	FailureCooldown   time.Duration
	Now               func() time.Time
	OnAccountsChanged func(context.Context)
}

// RefreshAccount refreshes one account while serializing refresh-token rotation
// across manual requests, background workers, and service instances.
func (s *RefreshService) RefreshAccount(ctx context.Context, accountID string) (domain.Credentials, error) {
	if s.OAuth == nil || s.Store == nil {
		return domain.Credentials{}, errors.New("OAuth refresh service is incomplete")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return domain.Credentials{}, errors.New("OAuth account ID is required")
	}
	credentials, err := s.Store.Credentials(ctx, accountID)
	if err != nil {
		return domain.Credentials{}, err
	}
	result, _, err := s.refreshAccount(ctx, accountID, credentials, time.Time{})
	return result, err
}

func (s *RefreshService) Run(ctx context.Context) error {
	if s.Interval <= 0 {
		s.Interval = 15 * time.Minute
	}
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		if err := s.RefreshDue(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *RefreshService) RefreshDue(ctx context.Context) error {
	if s.OAuth == nil || s.Store == nil {
		return errors.New("OAuth refresh service is incomplete")
	}
	items, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	before := s.Before
	if before <= 0 {
		before = 30 * time.Minute
	}
	concurrency := s.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	now := s.currentTime()
	refreshBefore := now.Add(before)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	changed := false
	for _, account := range items {
		if account.Kind != domain.CredentialCLIOAuth || account.Status == domain.AccountDisabled || account.CooldownUntil != nil && account.CooldownUntil.After(now) {
			continue
		}
		credentials, loadErr := s.Store.Credentials(ctx, account.ID)
		if loadErr != nil {
			mu.Lock()
			failures = append(failures, fmt.Errorf("%s: %w", account.ID, loadErr))
			mu.Unlock()
			continue
		}
		if credentials.ExpiresAt.After(refreshBefore) {
			if account.Status == domain.AccountCooldown {
				if saveErr := s.Store.SaveOAuthRefresh(ctx, account.ID, OAuthRefreshUpdate{Status: domain.AccountActive}); saveErr != nil {
					mu.Lock()
					failures = append(failures, fmt.Errorf("%s: clear elapsed refresh cooldown: %w", account.ID, saveErr))
					mu.Unlock()
				} else {
					mu.Lock()
					changed = true
					mu.Unlock()
				}
			}
			continue
		}
		wg.Add(1)
		go func(accountID string, current domain.Credentials) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			_, accountChanged, refreshErr := s.refreshAccount(ctx, accountID, current, refreshBefore)
			if accountChanged {
				mu.Lock()
				changed = true
				mu.Unlock()
			}
			if refreshErr != nil {
				mu.Lock()
				failures = append(failures, fmt.Errorf("%s: %w", accountID, refreshErr))
				mu.Unlock()
			}
		}(account.ID, credentials)
	}
	wg.Wait()
	if changed {
		s.accountsChanged(ctx)
	}
	return errors.Join(failures...)
}

func (s *RefreshService) refreshAccount(ctx context.Context, accountID string, initial domain.Credentials, refreshBefore time.Time) (result domain.Credentials, changed bool, err error) {
	current := initial
	if s.Locks != nil {
		owner := []byte(randomToken(24))
		lockKey := refreshLockKey(accountID)
		for {
			acquired, lockErr := s.Locks.SetNX(ctx, lockKey, owner, s.refreshLockTTL())
			if lockErr != nil {
				return domain.Credentials{}, false, fmt.Errorf("acquire refresh lock: %w", lockErr)
			}
			if acquired {
				break
			}
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return domain.Credentials{}, false, ctx.Err()
			case <-timer.C:
			}
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			released, unlockErr := s.Locks.CompareDelete(unlockCtx, lockKey, owner)
			if unlockErr != nil {
				err = errors.Join(err, fmt.Errorf("release refresh lock: %w", unlockErr))
			} else if !released {
				err = errors.Join(err, errors.New("release refresh lock: lock ownership changed"))
			}
		}()

		latest, loadErr := s.Store.Credentials(ctx, accountID)
		if loadErr != nil {
			return domain.Credentials{}, false, fmt.Errorf("reload credentials after locking: %w", loadErr)
		}
		if !sameOAuthCredentials(initial, latest) {
			return latest, false, nil
		}
		current = latest
	}
	if !refreshBefore.IsZero() && current.ExpiresAt.After(refreshBefore) {
		return current, false, nil
	}
	if current.RefreshToken == "" {
		changed, failureErr := s.recordRefreshFailure(ctx, accountID, current, errors.New("OAuth refresh token is empty"))
		return domain.Credentials{}, changed, failureErr
	}
	updated, err := s.OAuth.Refresh(ctx, current)
	if err != nil {
		changed, failureErr := s.recordRefreshFailure(ctx, accountID, current, err)
		return domain.Credentials{}, changed, failureErr
	}
	if err := s.Store.SaveOAuthRefresh(ctx, accountID, OAuthRefreshUpdate{Credentials: &updated, Status: domain.AccountActive}); err != nil {
		return domain.Credentials{}, false, err
	}
	return updated, true, nil
}

func (s *RefreshService) recordRefreshFailure(ctx context.Context, accountID string, credentials domain.Credentials, cause error) (bool, error) {
	now := s.currentTime()
	summary := oauthRefreshFailureSummary(cause)
	update := OAuthRefreshUpdate{Status: domain.AccountCooldown, LastError: summary}
	if !credentials.ExpiresAt.IsZero() && !credentials.ExpiresAt.After(now) {
		update.Status = domain.AccountExpired
	} else {
		cooldown := s.FailureCooldown
		if cooldown <= 0 {
			cooldown = 15 * time.Minute
		}
		until := now.Add(cooldown)
		update.CooldownUntil = &until
	}
	if err := s.Store.SaveOAuthRefresh(ctx, accountID, update); err != nil {
		return false, errors.Join(errors.New(summary), fmt.Errorf("record OAuth refresh failure: %w", err))
	}
	return true, errors.New(summary)
}

func (s *RefreshService) accountsChanged(ctx context.Context) {
	if s.OnAccountsChanged == nil {
		return
	}
	changeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	s.OnAccountsChanged(changeCtx)
}

func (s *RefreshService) currentTime() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func oauthRefreshFailureSummary(cause error) string {
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return "OAuth credential refresh timed out"
	case errors.Is(cause, context.Canceled):
		return "OAuth credential refresh was canceled"
	}
	var httpErr *HTTPError
	if errors.As(cause, &httpErr) {
		switch {
		case httpErr.StatusCode == http.StatusTooManyRequests:
			return "OAuth credential refresh was rate limited (HTTP 429)"
		case httpErr.StatusCode >= 500:
			return fmt.Sprintf("OAuth credential provider was unavailable (HTTP %d)", httpErr.StatusCode)
		default:
			return fmt.Sprintf("OAuth credential refresh was rejected (HTTP %d)", httpErr.StatusCode)
		}
	}
	return "OAuth credential refresh failed"
}

func (s *RefreshService) refreshLockTTL() time.Duration {
	if s.LockTTL > 0 {
		return s.LockTTL
	}
	return 2 * time.Minute
}

func sameOAuthCredentials(left, right domain.Credentials) bool {
	return left.AccessToken == right.AccessToken &&
		left.RefreshToken == right.RefreshToken &&
		left.IDToken == right.IDToken &&
		left.TokenType == right.TokenType &&
		left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.ClientID == right.ClientID &&
		left.Scope == right.Scope &&
		left.SSO == right.SSO &&
		left.SSORW == right.SSORW &&
		left.UserID == right.UserID &&
		left.CFClearance == right.CFClearance &&
		left.UserAgent == right.UserAgent &&
		left.BaseURL == right.BaseURL &&
		left.Email == right.Email &&
		left.Subscription == right.Subscription &&
		left.Entitlement == right.Entitlement
}

func oauthStateKey(state string) string {
	digest := sha256.Sum256([]byte(state))
	return "oauth:pkce:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func refreshLockKey(accountID string) string {
	return "oauth:refresh:" + accountID
}

func randomToken(size int) string {
	value := make([]byte, size)
	_, _ = rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value)
}

func copyValues(destination, source url.Values) {
	for key, values := range source {
		destination[key] = append([]string(nil), values...)
	}
}

func number(value any) int64 {
	switch value := value.(type) {
	case float64:
		return int64(value)
	case json.Number:
		result, _ := value.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(value, 10, 64)
		return result
	default:
		return 0
	}
}
