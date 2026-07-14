package store

import (
	"strings"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestAccountFilterSQLCombinesTierAndBoundProxy(t *testing.T) {
	proxyID := "proxy-1"
	where, args := accountFilterSQL(AccountFilter{
		Kind:    domain.CredentialCLIOAuth,
		Status:  domain.AccountActive,
		Tier:    " Super ",
		ProxyID: &proxyID,
		Model:   "grok-4",
		Query:   "team alpha",
	})

	for _, clause := range []string{"kind = $1", "status = $2", "LOWER(tier) = LOWER($3)", "proxy_id = $4", "models @> $5::jsonb", "name ILIKE $6"} {
		if !strings.Contains(where, clause) {
			t.Fatalf("account filter %q does not contain %q", where, clause)
		}
	}
	if len(args) != 6 || args[2] != "Super" || args[3] != proxyID || args[5] != "%team alpha%" {
		t.Fatalf("account filter args = %#v", args)
	}
}

func TestAccountFilterSQLSelectsDirectAccountsWithoutAParameter(t *testing.T) {
	direct := ""
	where, args := accountFilterSQL(AccountFilter{Tier: "basic", ProxyID: &direct})
	if !strings.Contains(where, "LOWER(tier) = LOWER($1)") || !strings.Contains(where, "proxy_id IS NULL") {
		t.Fatalf("direct account filter = %q", where)
	}
	if len(args) != 1 || args[0] != "basic" {
		t.Fatalf("direct account filter args = %#v", args)
	}
}
