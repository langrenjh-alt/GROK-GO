package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestAccountListCombinesFiltersWithPagination(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	ctx := context.Background()
	proxy, err := environment.management.CreateProxy(ctx, admin.CreateProxyInput{Name: "Primary proxy", URL: "http://127.0.0.1:8080", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	create := func(input admin.CreateAccountInput) *domain.Account {
		t.Helper()
		account, createErr := environment.management.CreateAccount(ctx, input)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return account
	}
	directOne := create(admin.CreateAccountInput{Name: "Alpha One", Kind: domain.CredentialGrokSSO, Tier: "super", Status: domain.AccountActive, Credentials: domain.Credentials{SSO: "direct-one"}, Tags: []string{"production"}, Priority: 200, ConcurrencyLimit: 1})
	directTwo := create(admin.CreateAccountInput{Name: "Alpha Two", Kind: domain.CredentialGrokSSO, Tier: "Super", Status: domain.AccountActive, Credentials: domain.Credentials{SSO: "direct-two"}, Tags: []string{"production"}, Priority: 100, ConcurrencyLimit: 1})
	proxiedAccount := create(admin.CreateAccountInput{Name: "Alpha Proxy", Kind: domain.CredentialGrokSSO, Tier: "super", Status: domain.AccountActive, Credentials: domain.Credentials{SSO: "proxied"}, ProxyID: proxy.ID, Priority: 100, ConcurrencyLimit: 1})
	wrongKind := create(admin.CreateAccountInput{Name: "Alpha Console", Kind: domain.CredentialConsoleSSO, Tier: "super", Status: domain.AccountActive, Credentials: domain.Credentials{AccessToken: "console-token"}, Priority: 100, ConcurrencyLimit: 1})

	direct := environment.request(t, http.MethodGet, "/accounts?page=2&page_size=1&q=alpha&status=active&kind=grok_sso&tier=SUPER&proxy_id=direct", nil, environment.cookie, "")
	if direct.Code != http.StatusOK || !strings.Contains(direct.Body.String(), `"id":"`+directTwo.ID+`"`) || !strings.Contains(direct.Body.String(), `"total":2`) {
		t.Fatalf("unexpected filtered direct accounts: %d %s", direct.Code, direct.Body.String())
	}
	for _, excluded := range []string{directOne.ID, proxiedAccount.ID, wrongKind.ID} {
		if strings.Contains(direct.Body.String(), `"id":"`+excluded+`"`) {
			t.Fatalf("filtered page contains %q: %s", excluded, direct.Body.String())
		}
	}

	proxied := environment.request(t, http.MethodGet, "/accounts?page=1&page_size=25&q=alpha&status=active&kind=grok_sso&tier=super&proxy_id="+proxy.ID, nil, environment.cookie, "")
	if proxied.Code != http.StatusOK || !strings.Contains(proxied.Body.String(), `"id":"`+proxiedAccount.ID+`"`) || !strings.Contains(proxied.Body.String(), `"total":1`) {
		t.Fatalf("unexpected filtered proxied accounts: %d %s", proxied.Code, proxied.Body.String())
	}
}
