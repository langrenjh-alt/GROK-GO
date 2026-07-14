package domain

import (
	"encoding/json"
	"time"
)

type CredentialKind string

const (
	CredentialCLIOAuth   CredentialKind = "cli_oauth"
	CredentialConsoleSSO CredentialKind = "console_sso"
	CredentialGrokSSO    CredentialKind = "grok_sso"
)

type AccountStatus string

const (
	AccountActive   AccountStatus = "active"
	AccountCooldown AccountStatus = "cooldown"
	AccountExpired  AccountStatus = "expired"
	AccountDisabled AccountStatus = "disabled"
	AccountError    AccountStatus = "error"
)

type Capability string

const (
	CapabilityChat      Capability = "chat"
	CapabilityResponses Capability = "responses"
	CapabilityMessages  Capability = "messages"
	CapabilityImage     Capability = "image"
	CapabilityImageEdit Capability = "image_edit"
	CapabilityVideo     Capability = "video"
)

type Credentials struct {
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	SSO          string    `json:"sso,omitempty"`
	SSORW        string    `json:"sso_rw,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	CFClearance  string    `json:"cf_clearance,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`
	BaseURL      string    `json:"base_url,omitempty"`
	Email        string    `json:"email,omitempty"`
	Subscription string    `json:"subscription_tier,omitempty"`
	Entitlement  string    `json:"entitlement_status,omitempty"`
}

type QuotaSnapshot struct {
	RequestsLimit     int64      `json:"requests_limit,omitempty"`
	RequestsRemaining int64      `json:"requests_remaining,omitempty"`
	RequestsUnlimited bool       `json:"requests_unlimited,omitempty"`
	TokensLimit       int64      `json:"tokens_limit,omitempty"`
	TokensRemaining   int64      `json:"tokens_remaining,omitempty"`
	TokensUnlimited   bool       `json:"tokens_unlimited,omitempty"`
	ResetAt           *time.Time `json:"reset_at,omitempty"`
	ObservedAt        time.Time  `json:"observed_at"`
}

type Account struct {
	ID                    string         `json:"id"`
	Name                  string         `json:"name"`
	Kind                  CredentialKind `json:"kind"`
	Tier                  string         `json:"tier"`
	Status                AccountStatus  `json:"status"`
	Email                 string         `json:"email,omitempty"`
	CredentialExpiresAt   *time.Time     `json:"credential_expires_at,omitempty"`
	CredentialCipher      []byte         `json:"-"`
	CredentialFingerprint []byte         `json:"-"`
	ProxyID               string         `json:"proxy_id,omitempty"`
	Models                []string       `json:"models,omitempty"`
	Tags                  []string       `json:"tags,omitempty"`
	Priority              int            `json:"priority"`
	ConcurrencyLimit      int            `json:"concurrency_limit"`
	HealthScore           float64        `json:"health_score"`
	FailureCount          int            `json:"failure_count"`
	Quota                 QuotaSnapshot  `json:"quota"`
	CooldownUntil         *time.Time     `json:"cooldown_until,omitempty"`
	LastUsedAt            *time.Time     `json:"last_used_at,omitempty"`
	LastError             string         `json:"last_error,omitempty"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

type ModelSpec struct {
	ID              string           `json:"id"`
	UpstreamModel   string           `json:"upstream_model"`
	DisplayName     string           `json:"display_name"`
	Capability      Capability       `json:"capability"`
	CredentialKinds []CredentialKind `json:"credential_kinds"`
	MinimumTier     string           `json:"minimum_tier,omitempty"`
	Aliases         []string         `json:"aliases,omitempty"`
	PreferBest      bool             `json:"prefer_best"`
	CatalogManaged  bool             `json:"catalog_managed"`
	Enabled         bool             `json:"enabled"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type ClientKey struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Prefix            string     `json:"prefix"`
	Digest            []byte     `json:"-"`
	SecretCipher      []byte     `json:"-"`
	SecretAvailable   bool       `json:"secret_available"`
	Enabled           bool       `json:"enabled"`
	RPM               int        `json:"rpm"`
	ConcurrencyLimit  int        `json:"concurrency_limit"`
	DailyRequestLimit int64      `json:"daily_request_limit"`
	MonthlyTokenLimit int64      `json:"monthly_token_limit"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Proxy struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	URLCipher     []byte     `json:"-"`
	Enabled       bool       `json:"enabled"`
	Healthy       bool       `json:"healthy"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type RequestLog struct {
	ID           string          `json:"id"`
	RequestID    string          `json:"request_id"`
	ClientKeyID  string          `json:"client_key_id,omitempty"`
	AccountID    string          `json:"account_id,omitempty"`
	Model        string          `json:"model,omitempty"`
	Endpoint     string          `json:"endpoint"`
	StatusCode   int             `json:"status_code"`
	DurationMS   int64           `json:"duration_ms"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	CachedTokens int64           `json:"cached_tokens"`
	UsageParsed  bool            `json:"usage_parsed"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorSummary string          `json:"error_summary,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type AuditLog struct {
	ID           string          `json:"id"`
	AdminID      string          `json:"admin_id,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id,omitempty"`
	IPAddress    string          `json:"ip_address,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
}

type MediaObject struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Path        string    `json:"-"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}
