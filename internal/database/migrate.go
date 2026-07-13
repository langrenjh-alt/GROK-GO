package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Migration struct {
	Version string
	SQL     string
}

func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, Migration{Version: entry.Name(), SQL: string(contents)})
	}
	return migrations, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("PostgreSQL pool is required")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('grok-go:migrations'))`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('grok-go:migrations'))`)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations, err := Migrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, migration.Version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", migration.Version, err)
		}
		if applied {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", migration.Version, err)
		}
		for _, statement := range splitSQLStatements(migration.SQL) {
			if _, err := tx.Exec(ctx, statement); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("apply migration %s: %w", migration.Version, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, migration.Version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", migration.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", migration.Version, err)
		}
	}
	return nil
}

func splitSQLStatements(input string) []string {
	var statements []string
	var current strings.Builder
	inSingle, inDouble, inLineComment, inBlockComment := false, false, false, false
	dollarTag := ""
	for i := 0; i < len(input); {
		if inLineComment {
			current.WriteByte(input[i])
			if input[i] == '\n' {
				inLineComment = false
			}
			i++
			continue
		}
		if inBlockComment {
			current.WriteByte(input[i])
			if i+1 < len(input) && input[i] == '*' && input[i+1] == '/' {
				current.WriteByte(input[i+1])
				i += 2
				inBlockComment = false
				continue
			}
			i++
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(input[i:], dollarTag) {
				current.WriteString(dollarTag)
				i += len(dollarTag)
				dollarTag = ""
				continue
			}
			current.WriteByte(input[i])
			i++
			continue
		}
		if !inSingle && !inDouble && i+1 < len(input) && input[i] == '-' && input[i+1] == '-' {
			current.WriteString("--")
			i += 2
			inLineComment = true
			continue
		}
		if !inSingle && !inDouble && i+1 < len(input) && input[i] == '/' && input[i+1] == '*' {
			current.WriteString("/*")
			i += 2
			inBlockComment = true
			continue
		}
		if !inSingle && !inDouble && input[i] == '$' {
			if tag := readDollarTag(input[i:]); tag != "" {
				current.WriteString(tag)
				i += len(tag)
				dollarTag = tag
				continue
			}
		}
		switch input[i] {
		case '\'':
			current.WriteByte(input[i])
			if !inDouble {
				if inSingle && i+1 < len(input) && input[i+1] == '\'' {
					current.WriteByte(input[i+1])
					i += 2
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			current.WriteByte(input[i])
			if !inSingle {
				inDouble = !inDouble
			}
		case ';':
			if inSingle || inDouble {
				current.WriteByte(input[i])
			} else if statement := strings.TrimSpace(current.String()); statement != "" {
				statements = append(statements, statement)
				current.Reset()
			}
		default:
			current.WriteByte(input[i])
		}
		i++
	}
	if statement := strings.TrimSpace(current.String()); statement != "" {
		statements = append(statements, statement)
	}
	return statements
}

func readDollarTag(input string) string {
	if input == "" || input[0] != '$' {
		return ""
	}
	for i := 1; i < len(input); i++ {
		if input[i] == '$' {
			return input[:i+1]
		}
		if input[i] != '_' && !unicode.IsLetter(rune(input[i])) && !unicode.IsDigit(rune(input[i])) {
			return ""
		}
	}
	return ""
}
