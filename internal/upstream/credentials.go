package upstream

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type CredentialAdapter interface {
	Apply(*http.Request, domain.Credentials) error
}

func AdapterFor(kind domain.CredentialKind) (CredentialAdapter, error) {
	switch kind {
	case domain.CredentialGrokSSO:
		return GrokSSOAdapter{}, nil
	case domain.CredentialConsoleSSO:
		return ConsoleSSOAdapter{}, nil
	case domain.CredentialCLIOAuth:
		return CLIOAuthAdapter{}, nil
	default:
		return nil, errors.New("unsupported credential kind")
	}
}

type GrokSSOAdapter struct{}

func (GrokSSOAdapter) Apply(request *http.Request, credentials domain.Credentials) error {
	if strings.TrimSpace(credentials.SSO) == "" {
		return errors.New("grok SSO credential is empty")
	}
	setBrowserHeaders(request, credentials, "https://grok.com", "https://grok.com/")
	request.Header.Set("Cookie", ssoCookie(credentials))
	request.Header.Set("x-xai-request-id", randomID())
	request.Header.Set("x-statsig-id", statsigID())
	return nil
}

type ConsoleSSOAdapter struct{}

func (ConsoleSSOAdapter) Apply(request *http.Request, credentials domain.Credentials) error {
	if strings.TrimSpace(credentials.SSO) == "" {
		return errors.New("console SSO credential is empty")
	}
	setBrowserHeaders(request, credentials, "https://console.x.ai", "https://console.x.ai/")
	request.Header.Set("Authorization", "Bearer anonymous")
	request.Header.Set("Cookie", ssoCookie(credentials))
	request.Header.Set("x-cluster", "https://us-east-1.api.x.ai")
	return nil
}

type CLIOAuthAdapter struct{}

func (CLIOAuthAdapter) Apply(request *http.Request, credentials domain.Credentials) error {
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return errors.New("CLI OAuth access token is empty")
	}
	tokenType := strings.TrimSpace(credentials.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	request.Header.Set("Authorization", tokenType+" "+credentials.AccessToken)
	request.Header.Set("Accept", "text/event-stream, application/json")
	request.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	request.Header.Set("X-Grok-Client-Version", "0.2.93")
	request.Header.Set("X-Grok-Client-Identifier", "grok-shell")
	userAgent := strings.TrimSpace(credentials.UserAgent)
	if userAgent == "" {
		userAgent = "xai-grok-workspace/0.2.93"
	}
	request.Header.Set("User-Agent", userAgent)
	return nil
}

func setBrowserHeaders(request *http.Request, credentials domain.Credentials, origin, referer string) {
	userAgent := strings.TrimSpace(credentials.UserAgent)
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/136.0.0.0 Safari/537.36"
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Origin", origin)
	request.Header.Set("Referer", referer)
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
}

func ssoCookie(credentials domain.Credentials) string {
	sso := sanitizeCookieValue(credentials.SSO)
	ssoRW := sanitizeCookieValue(credentials.SSORW)
	if ssoRW == "" {
		ssoRW = sso
	}
	parts := []string{"sso=" + sso, "sso-rw=" + ssoRW}
	if userID := credentialUserID(credentials); userID != "" {
		parts = append(parts, "x-userid="+sanitizeCookieValue(userID))
	}
	if clearance := sanitizeCookieValue(credentials.CFClearance); clearance != "" {
		parts = append(parts, "cf_clearance="+clearance)
	}
	return strings.Join(parts, "; ")
}

func credentialUserID(credentials domain.Credentials) string {
	if userID := sanitizeCookieValue(credentials.UserID); userID != "" {
		return userID
	}
	for _, raw := range []string{credentials.SSO, credentials.SSORW, credentials.CFClearance} {
		request := &http.Request{Header: http.Header{"Cookie": []string{raw}}}
		for _, cookie := range request.Cookies() {
			if strings.EqualFold(cookie.Name, "x-userid") {
				return sanitizeCookieValue(cookie.Value)
			}
		}
	}
	return ""
}

func sanitizeCookieValue(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "sso="))
	value = strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e || r == ';' || r == ',' {
			return -1
		}
		return r
	}, value)
	return value
}

func randomID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func statsigID() string {
	return base64.StdEncoding.EncodeToString([]byte("x1:TypeError: Cannot read properties of undefined"))
}
