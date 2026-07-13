package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentTest        = "test"
	EnvironmentProduction  = "production"
)

type Config struct {
	Environment string
	InstanceID  string
	HTTP        HTTPConfig
	Database    DatabaseConfig
	Redis       RedisConfig
	Security    SecurityConfig
	Admin       AdminConfig
	OAuth       OAuthConfig
	Media       MediaConfig
}

type HTTPConfig struct {
	Address           string
	PublicURL         string
	TrustProxyHeaders bool
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type DatabaseConfig struct {
	URL             string
	MaxConnections  int32
	MinConnections  int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	HealthTimeout   time.Duration
}

type RedisConfig struct {
	URL           string
	KeyPrefix     string
	DialTimeout   time.Duration
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	HealthTimeout time.Duration
}

func (c RedisConfig) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

type SecurityConfig struct {
	EncryptionKey []byte
	TokenPepper   []byte
	SessionTTL    time.Duration
	CookieName    string
	CookieSecure  bool
	CookieDomain  string
	CSRFHeader    string
	TrustedOrigin string
}

type AdminConfig struct {
	BootstrapEmail    string
	BootstrapPassword string
	BootstrapToken    string
	TOTPIssuer        string
}

func (c AdminConfig) BootstrapEnabled() bool {
	return c.BootstrapToken != "" || c.BootstrapEmail != "" || c.BootstrapPassword != ""
}

type OAuthConfig struct {
	AuthorizationURL string
	TokenURL         string
	ClientID         string
	Scope            string
	RedirectURL      string
}

type MediaConfig struct {
	DataDir       string
	MaxBytes      int64
	ImageTTL      time.Duration
	VideoTTL      time.Duration
	SignedURLTTL  time.Duration
	MaxFetchBytes int64
}

type LookupFunc func(string) (string, bool)

func Load(lookup LookupFunc) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	environment := strings.ToLower(valueOr(lookup, "GROK_GO_ENV", EnvironmentDevelopment))
	secureCookiesDefault := environment == EnvironmentProduction
	cfg := Config{
		Environment: environment,
		InstanceID:  valueOr(lookup, "GROK_GO_INSTANCE_ID", ""),
		HTTP: HTTPConfig{
			Address:           firstValue(lookup, []string{"GROK_GO_LISTEN_ADDR", "GROK_GO_ADDR"}, ":8080"),
			PublicURL:         strings.TrimRight(valueOr(lookup, "GROK_GO_PUBLIC_URL", "http://127.0.0.1:8080"), "/"),
			TrustProxyHeaders: boolOr(lookup, "GROK_GO_TRUST_PROXY_HEADERS", false),
			ReadTimeout:       durationOr(lookup, "GROK_GO_HTTP_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:      durationOr(lookup, "GROK_GO_HTTP_WRITE_TIMEOUT", 2*time.Minute),
			IdleTimeout:       durationOr(lookup, "GROK_GO_HTTP_IDLE_TIMEOUT", 2*time.Minute),
		},
		Database: DatabaseConfig{
			URL:             firstValue(lookup, []string{"GROK_GO_DATABASE_URL", "DATABASE_URL"}, ""),
			MaxConnections:  int32Or(lookup, "GROK_GO_DB_MAX_CONNS", 20),
			MinConnections:  int32Or(lookup, "GROK_GO_DB_MIN_CONNS", 2),
			MaxConnLifetime: durationOr(lookup, "GROK_GO_DB_CONN_LIFETIME", 30*time.Minute),
			MaxConnIdleTime: durationOr(lookup, "GROK_GO_DB_CONN_IDLE_TIME", 5*time.Minute),
			HealthTimeout:   durationOr(lookup, "GROK_GO_DB_HEALTH_TIMEOUT", 5*time.Second),
		},
		Redis: RedisConfig{
			URL:           firstValue(lookup, []string{"GROK_GO_REDIS_URL", "REDIS_URL"}, ""),
			KeyPrefix:     valueOr(lookup, "GROK_GO_REDIS_PREFIX", "grok-go:"),
			DialTimeout:   durationOr(lookup, "GROK_GO_REDIS_DIAL_TIMEOUT", 5*time.Second),
			ReadTimeout:   durationOr(lookup, "GROK_GO_REDIS_READ_TIMEOUT", 3*time.Second),
			WriteTimeout:  durationOr(lookup, "GROK_GO_REDIS_WRITE_TIMEOUT", 3*time.Second),
			HealthTimeout: durationOr(lookup, "GROK_GO_REDIS_HEALTH_TIMEOUT", 3*time.Second),
		},
		Security: SecurityConfig{
			SessionTTL:    durationOr(lookup, "GROK_GO_SESSION_TTL", 24*time.Hour),
			CookieName:    valueOr(lookup, "GROK_GO_SESSION_COOKIE", "grok_go_admin"),
			CookieSecure:  boolOr(lookup, "GROK_GO_COOKIE_SECURE", secureCookiesDefault),
			CookieDomain:  valueOr(lookup, "GROK_GO_COOKIE_DOMAIN", ""),
			CSRFHeader:    valueOr(lookup, "GROK_GO_CSRF_HEADER", "X-CSRF-Token"),
			TrustedOrigin: strings.TrimRight(valueOr(lookup, "GROK_GO_TRUSTED_ORIGIN", ""), "/"),
		},
		Admin: AdminConfig{
			BootstrapEmail:    strings.ToLower(strings.TrimSpace(valueOr(lookup, "GROK_GO_ADMIN_EMAIL", ""))),
			BootstrapPassword: valueOr(lookup, "GROK_GO_ADMIN_PASSWORD", ""),
			BootstrapToken:    valueOr(lookup, "GROK_GO_BOOTSTRAP_TOKEN", ""),
			TOTPIssuer:        valueOr(lookup, "GROK_GO_TOTP_ISSUER", "GROK-GO"),
		},
		OAuth: OAuthConfig{
			AuthorizationURL: valueOr(lookup, "GROK_GO_XAI_OAUTH_AUTHORIZE_URL", "https://auth.x.ai/oauth2/authorize"),
			TokenURL:         valueOr(lookup, "GROK_GO_XAI_OAUTH_TOKEN_URL", "https://auth.x.ai/oauth2/token"),
			ClientID:         valueOr(lookup, "GROK_GO_XAI_OAUTH_CLIENT_ID", "b1a00492-073a-47ea-816f-4c329264a828"),
			Scope:            valueOr(lookup, "GROK_GO_XAI_OAUTH_SCOPE", "openid profile email offline_access grok-cli:access api:access"),
			RedirectURL:      valueOr(lookup, "GROK_GO_XAI_OAUTH_REDIRECT_URL", "http://127.0.0.1:56121/callback"),
		},
		Media: MediaConfig{
			DataDir:       valueOr(lookup, "GROK_GO_DATA_DIR", "./data"),
			MaxBytes:      int64Or(lookup, "GROK_GO_MEDIA_MAX_BYTES", 20<<30),
			ImageTTL:      durationOr(lookup, "GROK_GO_IMAGE_TTL", 72*time.Hour),
			VideoTTL:      durationOr(lookup, "GROK_GO_VIDEO_TTL", 7*24*time.Hour),
			SignedURLTTL:  durationOr(lookup, "GROK_GO_MEDIA_SIGNED_URL_TTL", time.Hour),
			MaxFetchBytes: int64Or(lookup, "GROK_GO_MEDIA_MAX_FETCH_BYTES", 25<<20),
		},
	}

	key, err := decodeSecretKey(valueOr(lookup, "GROK_GO_MASTER_KEY", ""))
	if err != nil {
		return Config{}, fmt.Errorf("GROK_GO_MASTER_KEY: %w", err)
	}
	cfg.Security.EncryptionKey = key
	pepperRaw := valueOr(lookup, "GROK_GO_TOKEN_PEPPER", "")
	if pepperRaw == "" {
		cfg.Security.TokenPepper = append([]byte(nil), key...)
	} else {
		pepper, err := decodeSecretKey(pepperRaw)
		if err != nil {
			return Config{}, fmt.Errorf("GROK_GO_TOKEN_PEPPER: %w", err)
		}
		cfg.Security.TokenPepper = pepper
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var problems []string
	switch c.Environment {
	case EnvironmentDevelopment, EnvironmentTest, EnvironmentProduction:
	default:
		problems = append(problems, "GROK_GO_ENV must be development, test, or production")
	}
	if len(c.InstanceID) > 128 || strings.ContainsAny(c.InstanceID, " \t\r\n/\\") {
		problems = append(problems, "GROK_GO_INSTANCE_ID must be a single safe identifier of at most 128 characters")
	}
	if strings.TrimSpace(c.HTTP.Address) == "" {
		problems = append(problems, "GROK_GO_LISTEN_ADDR is required")
	}
	if err := validateAbsoluteURL(c.HTTP.PublicURL, false); err != nil {
		problems = append(problems, "GROK_GO_PUBLIC_URL must be an absolute http(s) URL")
	}
	if c.HTTP.ReadTimeout <= 0 || c.HTTP.WriteTimeout <= 0 || c.HTTP.IdleTimeout <= 0 {
		problems = append(problems, "HTTP timeout values must be positive durations")
	}
	if strings.TrimSpace(c.Database.URL) == "" {
		problems = append(problems, "GROK_GO_DATABASE_URL is required")
	} else if parsed, err := url.Parse(c.Database.URL); err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		problems = append(problems, "GROK_GO_DATABASE_URL must use postgres or postgresql scheme")
	}
	if c.Database.MaxConnections <= 0 || c.Database.MinConnections < 0 || c.Database.MinConnections > c.Database.MaxConnections {
		problems = append(problems, "database connection limits are invalid")
	}
	if c.Database.MaxConnLifetime <= 0 || c.Database.MaxConnIdleTime <= 0 || c.Database.HealthTimeout <= 0 {
		problems = append(problems, "database timeout values must be positive durations")
	}
	if !c.Redis.Enabled() {
		problems = append(problems, "GROK_GO_REDIS_URL is required")
	} else {
		parsed, err := url.Parse(c.Redis.URL)
		if err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
			problems = append(problems, "GROK_GO_REDIS_URL must use redis or rediss scheme")
		}
	}
	if c.Redis.DialTimeout <= 0 || c.Redis.ReadTimeout <= 0 || c.Redis.WriteTimeout <= 0 || c.Redis.HealthTimeout <= 0 {
		problems = append(problems, "Redis timeout values must be positive durations")
	}
	if len(c.Security.EncryptionKey) != 32 {
		problems = append(problems, "GROK_GO_MASTER_KEY must decode to exactly 32 bytes")
	}
	if len(c.Security.TokenPepper) != 32 {
		problems = append(problems, "GROK_GO_TOKEN_PEPPER must decode to exactly 32 bytes")
	}
	if c.Security.SessionTTL < 5*time.Minute {
		problems = append(problems, "GROK_GO_SESSION_TTL must be at least 5m")
	}
	if strings.TrimSpace(c.Security.CookieName) == "" || strings.ContainsAny(c.Security.CookieName, " ;,\t\r\n") {
		problems = append(problems, "GROK_GO_SESSION_COOKIE is invalid")
	}
	if c.Security.TrustedOrigin != "" {
		if err := validateAbsoluteURL(c.Security.TrustedOrigin, true); err != nil {
			problems = append(problems, "GROK_GO_TRUSTED_ORIGIN must be an absolute http(s) origin without path")
		}
	}
	if c.Admin.BootstrapEnabled() {
		if (c.Admin.BootstrapEmail == "") != (c.Admin.BootstrapPassword == "") {
			problems = append(problems, "GROK_GO_ADMIN_EMAIL and GROK_GO_ADMIN_PASSWORD must be set together")
		}
		if c.Admin.BootstrapPassword != "" && len(c.Admin.BootstrapPassword) < 12 {
			problems = append(problems, "GROK_GO_ADMIN_PASSWORD must contain at least 12 characters")
		}
	}
	if err := validateAbsoluteURL(c.OAuth.AuthorizationURL, false); err != nil {
		problems = append(problems, "GROK_GO_XAI_OAUTH_AUTHORIZE_URL must be an absolute http(s) URL")
	}
	if err := validateAbsoluteURL(c.OAuth.TokenURL, false); err != nil {
		problems = append(problems, "GROK_GO_XAI_OAUTH_TOKEN_URL must be an absolute http(s) URL")
	}
	if c.OAuth.ClientID == "" || c.OAuth.Scope == "" {
		problems = append(problems, "xAI OAuth client ID and scope are required")
	}
	if c.Media.DataDir == "" || c.Media.MaxBytes <= 0 || c.Media.MaxFetchBytes <= 0 || c.Media.ImageTTL <= 0 || c.Media.VideoTTL <= 0 || c.Media.SignedURLTTL <= 0 {
		problems = append(problems, "media configuration values must be positive")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateAbsoluteURL(raw string, originOnly bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("invalid URL")
	}
	if originOnly && (parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "") {
		return errors.New("URL must be an origin")
	}
	return nil
}

func decodeSecretKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("value is required")
	}
	if strings.HasPrefix(raw, "hex:") {
		decoded, err := hex.DecodeString(strings.TrimPrefix(raw, "hex:"))
		if err != nil {
			return nil, errors.New("invalid hex encoding")
		}
		return decoded, nil
	}
	if strings.HasPrefix(raw, "base64:") {
		raw = strings.TrimPrefix(raw, "base64:")
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(raw); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("value must be base64 or hex:<hex>")
}

func valueOr(lookup LookupFunc, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func durationOr(lookup LookupFunc, key string, fallback time.Duration) time.Duration {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return -1
	}
	return parsed
}

func int32Or(lookup LookupFunc, key string, fallback int32) int32 {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return -1
	}
	return int32(parsed)
}

func int64Or(lookup LookupFunc, key string, fallback int64) int64 {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return -1
	}
	return parsed
}

func firstValue(lookup LookupFunc, keys []string, fallback string) string {
	for _, key := range keys {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return fallback
}

func boolOr(lookup LookupFunc, key string, fallback bool) bool {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}
