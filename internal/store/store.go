// Package store owns Postgres access (pgx + sqlc).
// Scaffold: connection helper + hand-written types mirroring migrations.
// Run `just generate` (sqlc) in Phase 1 to replace types with generated code.
package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps the pgx pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Connect opens a pgx pool. Callers must Close when done.
func Connect(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{Pool: pool}, nil
}

// Close releases the pool.
func (db *DB) Close() {
	if db != nil && db.Pool != nil {
		db.Pool.Close()
	}
}
