package database

import (
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedMigrations(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	if len(migrations) == 0 || migrations[0].Version != "001_initial.sql" {
		t.Fatalf("migrations = %+v", migrations)
	}
	for _, table := range []string{"admins", "admin_sessions", "accounts", "models", "proxies", "client_keys", "request_logs"} {
		if !strings.Contains(migrations[0].SQL, "CREATE TABLE "+table) {
			t.Fatalf("migration does not create %s", table)
		}
	}
}

func TestGrokModelCatalogMigration(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var catalogSQL string
	for _, migration := range migrations {
		if migration.Version == "005_grok_model_catalog.sql" {
			catalogSQL = migration.SQL
			break
		}
	}
	if catalogSQL == "" {
		t.Fatal("Grok model catalog migration is missing")
	}
	for _, fragment := range []string{"catalog_managed", "prefer_best", "'image_edit'", "grok-420-computer-use-sa", "grok-4.5-cli", "grok-build-console"} {
		if !strings.Contains(catalogSQL, fragment) {
			t.Fatalf("catalog migration does not contain %q", fragment)
		}
	}
	parts := strings.SplitN(catalogSQL, "INSERT INTO models (", 2)
	if len(parts) != 2 {
		t.Fatal("catalog preset INSERT is missing")
	}
	ids := regexp.MustCompile(`(?m)^\s+\('([^']+)'`).FindAllStringSubmatch(parts[1], -1)
	if len(ids) != 35 {
		t.Fatalf("catalog preset count = %d, want 35", len(ids))
	}
	for _, match := range ids {
		if !strings.HasPrefix(match[1], "grok-") {
			t.Fatalf("catalog contains a non-Grok model ID %q", match[1])
		}
	}
}

func TestSplitSQLStatementsHonorsQuotedAndDollarQuotedSemicolons(t *testing.T) {
	input := `
		CREATE TABLE test (value text DEFAULT ';');
		DO $$ BEGIN RAISE NOTICE 'x;y'; END $$;
		-- comment ;
		INSERT INTO test VALUES ('ok');
	`
	statements := splitSQLStatements(input)
	if len(statements) != 3 {
		t.Fatalf("len(statements) = %d: %#v", len(statements), statements)
	}
}

func TestRedisKey(t *testing.T) {
	r := &Redis{prefix: "grok-go:"}
	if got := r.Key("session", "123"); got != "grok-go:session:123" {
		t.Fatalf("Key() = %q", got)
	}
}
