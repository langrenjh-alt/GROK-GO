package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	env := map[string]string{
		"GROK_GO_ENV":                 "production",
		"DATABASE_URL":                "postgres://user:pass@localhost/grok_go",
		"REDIS_URL":                   "rediss://localhost:6380/1",
		"GROK_GO_MASTER_KEY":          key,
		"GROK_GO_ADMIN_EMAIL":         "ADMIN@example.com",
		"GROK_GO_ADMIN_PASSWORD":      "correct horse battery staple",
		"GROK_GO_SESSION_TTL":         "12h",
		"GROK_GO_TRUSTED_ORIGIN":      "https://admin.example.com",
		"GROK_GO_DB_MAX_CONNS":        "30",
		"GROK_GO_DB_MIN_CONNS":        "3",
		"GROK_GO_COOKIE_SECURE":       "true",
		"GROK_GO_REDIS_PREFIX":        "test:",
		"GROK_GO_TRUST_PROXY_HEADERS": "true",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	cfg, err := Load(lookup)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Admin.BootstrapEmail != "admin@example.com" {
		t.Fatalf("email = %q", cfg.Admin.BootstrapEmail)
	}
	if cfg.Security.SessionTTL != 12*time.Hour || !cfg.Security.CookieSecure {
		t.Fatalf("unexpected security config: %+v", cfg.Security)
	}
	if cfg.Database.MaxConnections != 30 || cfg.Database.MinConnections != 3 {
		t.Fatalf("unexpected database config: %+v", cfg.Database)
	}
	if !cfg.HTTP.TrustProxyHeaders {
		t.Fatal("proxy headers should be trusted when explicitly configured")
	}
	if string(cfg.Security.TokenPepper) != string(cfg.Security.EncryptionKey) {
		t.Fatal("token pepper should default to master key")
	}
}

func TestLoadRejectsIncompleteBootstrapAndShortKey(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":           "postgres://localhost/grok_go",
		"GROK_GO_MASTER_KEY":     base64.RawStdEncoding.EncodeToString([]byte("short")),
		"GROK_GO_ADMIN_EMAIL":    "admin@example.com",
		"GROK_GO_ADMIN_PASSWORD": "short",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	_, err := Load(lookup)
	if err == nil || !strings.Contains(err.Error(), "exactly 32 bytes") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestDecodeSecretKeyHex(t *testing.T) {
	key, err := decodeSecretKey("hex:3031323334353637383961626364656630313233343536373839616263646566")
	if err != nil {
		t.Fatalf("decodeSecretKey() error = %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("len(key) = %d", len(key))
	}
}

func TestLoadRejectsInvalidTimeout(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	env := map[string]string{
		"DATABASE_URL":               "postgres://localhost/grok_go",
		"REDIS_URL":                  "redis://localhost:6379/0",
		"GROK_GO_MASTER_KEY":         key,
		"GROK_GO_HTTP_WRITE_TIMEOUT": "not-a-duration",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	_, err := Load(lookup)
	if err == nil || !strings.Contains(err.Error(), "HTTP timeout values") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsUnsafeInstanceID(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/grok_go",
		"REDIS_URL":           "redis://localhost:6379/0",
		"GROK_GO_MASTER_KEY":  key,
		"GROK_GO_INSTANCE_ID": "node/a",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }

	_, err := Load(lookup)
	if err == nil || !strings.Contains(err.Error(), "GROK_GO_INSTANCE_ID") {
		t.Fatalf("Load() error = %v", err)
	}
}
