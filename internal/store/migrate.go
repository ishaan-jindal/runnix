package store

import (
	"context"
	"embed"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies pending migrations in lexical order, tracking them in
// schema_migrations. It is idempotent and safe to run from every gateway
// replica at startup: each version is claimed with an INSERT that skips on
// conflict, so a concurrent replica that loses the race simply skips.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	sort.Strings(versions)

	for _, v := range versions {
		if err := applyMigration(ctx, pool, v); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration claims version v, then runs its SQL. The claim and the
// script run in one transaction; a conflicting replica rolls back and skips.
func applyMigration(ctx context.Context, pool *pgxpool.Pool, v string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", v, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, v)
	if err != nil {
		return fmt.Errorf("claim %s: %w", v, err)
	}
	if tag.RowsAffected() == 0 {
		return nil // already applied (possibly by a concurrent replica)
	}

	raw, err := migrationFS.ReadFile("migrations/" + v)
	if err != nil {
		return fmt.Errorf("read %s: %w", v, err)
	}
	if _, err := tx.Exec(ctx, string(raw)); err != nil {
		return fmt.Errorf("apply %s: %w", v, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", v, err)
	}
	return nil
}
