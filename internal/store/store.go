// Package store owns Postgres access (pgx + sqlc): connection helper,
// migrations, and pgtype conversions. Queries live in queries/*.sql and
// generate into storedb/ via `just generate`.
package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgx pool. Callers must Close when done.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
