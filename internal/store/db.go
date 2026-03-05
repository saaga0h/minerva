package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a PostgreSQL connection pool.
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
		`CREATE TABLE IF NOT EXISTS articles (
			article_id      TEXT PRIMARY KEY,
			url             TEXT UNIQUE NOT NULL,
			title           TEXT NOT NULL,
			source          TEXT,
			content         TEXT,
			domain          TEXT,
			article_type    TEXT,
			summary         TEXT,
			keywords        JSONB,
			concepts        JSONB,
			related_topics  JSONB,
			entities        JSONB,
			insights        TEXT,
			content_tsv     tsvector GENERATED ALWAYS AS (
				to_tsvector('english',
					coalesce(title, '') || ' ' ||
					coalesce(content, '') || ' ' ||
					coalesce(summary, ''))
			) STORED,
			published_at    TIMESTAMPTZ,
			extracted_at    TIMESTAMPTZ,
			analyzed_at     TIMESTAMPTZ,
			completed_at    TIMESTAMPTZ,
			created_at      TIMESTAMPTZ DEFAULT now()
		)`,

		`CREATE INDEX IF NOT EXISTS idx_articles_content_tsv ON articles USING GIN (content_tsv)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_keywords    ON articles USING GIN (keywords)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_concepts    ON articles USING GIN (concepts)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_domain      ON articles (domain)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_created_at  ON articles (created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_pending     ON articles (source, published_at) WHERE completed_at IS NULL`,

		// Idempotent column additions for existing databases
		`ALTER TABLE articles ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ`,
		`ALTER TABLE articles ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ`,

		`CREATE TABLE IF NOT EXISTS works (
			work_id         SERIAL PRIMARY KEY,
			canonical_id    TEXT UNIQUE NOT NULL,
			reference_ids   JSONB DEFAULT '[]',
			sources         JSONB DEFAULT '[]',
			work_type       TEXT NOT NULL,
			title           TEXT NOT NULL,
			authors         JSONB,
			publisher       TEXT,
			publish_year    INT,
			isbn            TEXT,
			isbn13          TEXT,
			issn            TEXT,
			doi             TEXT,
			arxiv_id        TEXT,
			cover_url       TEXT,
			abstract        TEXT,
			subjects        JSONB,
			abstract_tsv    tsvector GENERATED ALWAYS AS (
				to_tsvector('english',
					coalesce(title, '') || ' ' ||
					coalesce(abstract, ''))
			) STORED,
			first_seen_at   TIMESTAMPTZ DEFAULT now(),
			enriched_at     TIMESTAMPTZ
		)`,

		`CREATE INDEX IF NOT EXISTS idx_works_abstract_tsv ON works USING GIN (abstract_tsv)`,
		`CREATE INDEX IF NOT EXISTS idx_works_subjects     ON works USING GIN (subjects)`,
		`CREATE INDEX IF NOT EXISTS idx_works_isbn13       ON works (isbn13)`,
		`CREATE INDEX IF NOT EXISTS idx_works_doi          ON works (doi)`,
		`CREATE INDEX IF NOT EXISTS idx_works_arxiv_id     ON works (arxiv_id)`,
		`CREATE INDEX IF NOT EXISTS idx_works_work_type    ON works (work_type)`,

		`CREATE TABLE IF NOT EXISTS article_works (
			id              SERIAL PRIMARY KEY,
			article_id      TEXT REFERENCES articles(article_id) ON DELETE CASCADE,
			work_id         INT  REFERENCES works(work_id) ON DELETE CASCADE,
			search_source   TEXT,
			relevance       REAL,
			owned_in_koha   BOOLEAN DEFAULT FALSE,
			created_at      TIMESTAMPTZ DEFAULT now(),
			UNIQUE (article_id, work_id)
		)`,

		`CREATE INDEX IF NOT EXISTS idx_article_works_article_id ON article_works (article_id)`,
		`CREATE INDEX IF NOT EXISTS idx_article_works_work_id    ON article_works (work_id)`,
		`CREATE INDEX IF NOT EXISTS idx_article_works_relevance  ON article_works (relevance DESC)`,
	}

	for _, stmt := range statements {
		if _, err := db.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}

	return nil
}
