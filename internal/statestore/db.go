package statestore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a PostgreSQL connection pool for pipeline state storage.
type DB struct {
	pool *pgxpool.Pool
}

// New creates a connection pool and runs schema migrations.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to reach postgres: %w", err)
	}

	db := &DB{pool: pool}
	if err := db.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return db, nil
}

// Close releases the connection pool.
func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS pipeline_state (
			article_id  TEXT        NOT NULL,
			topic       TEXT        NOT NULL,
			payload     JSONB       NOT NULL,
			stored_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (article_id, topic)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_state_article_id ON pipeline_state (article_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_state_stored_at  ON pipeline_state (stored_at)`,
	}

	for _, stmt := range statements {
		if _, err := db.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration: %w", err)
		}
	}

	return nil
}
