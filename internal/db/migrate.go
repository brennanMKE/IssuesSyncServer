package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate creates the schema_migrations table if needed and runs any
// migration SQL files that have not yet been applied. Migration files
// must be named NNN_description.sql where NNN is a zero-padded integer;
// they are applied in lexicographic (alphabetical) order.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Ensure the tracking table exists.
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text        PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Collect all migration files in order.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var filenames []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			filenames = append(filenames, e.Name())
		}
	}
	sort.Strings(filenames)

	for _, name := range filenames {
		version := strings.TrimSuffix(name, filepath.Ext(name))

		// Check whether this version has already been applied.
		var count int
		err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = $1`, version,
		).Scan(&count)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if count > 0 {
			slog.Debug("migration already applied", "version", version)
			continue
		}

		// Read and execute the SQL.
		data, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		slog.Info("applying migration", "version", version)

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx, string(data)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("execute migration %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}

		slog.Info("migration applied", "version", version)
	}

	return nil
}
