package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

// Ensure pgvector.Vector is imported for use across the package.
var _ = pgvector.NewVector

// DB wraps a PostgreSQL connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// New creates a connection pool and runs schema migrations.
// It first ensures the pgvector extension exists using a plain connection,
// then recreates the pool with AfterConnect type registration.
func New(ctx context.Context, dsn string) (*DB, error) {
	// Phase 1: create pgvector extension on a plain connection so that
	// AfterConnect type registration (which queries for the vector OID) succeeds.
	plainConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect for pre-migration: %w", err)
	}
	if _, err := plainConn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		plainConn.Close(ctx)
		return nil, fmt.Errorf("failed to create vector extension: %w", err)
	}
	plainConn.Close(ctx)

	// Phase 2: build pool with pgvector codec registered on every connection.
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN: %w", err)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
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

		// Vector embedding columns (pgvector extension created in New() before pool init)
		`ALTER TABLE articles ADD COLUMN IF NOT EXISTS embedding vector(4096)`,
		`ALTER TABLE works    ADD COLUMN IF NOT EXISTS embedding vector(4096)`,

		`CREATE TABLE IF NOT EXISTS brief_sessions (
			id          SERIAL PRIMARY KEY,
			session_id  TEXT NOT NULL UNIQUE,
			queried_at  TIMESTAMPTZ DEFAULT now()
		)`,

		// Deduplicate brief_sessions keeping the earliest row per session_id, then add unique constraint.
		`DELETE FROM brief_sessions WHERE id NOT IN (
			SELECT MIN(id) FROM brief_sessions GROUP BY session_id
		)`,
		`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'brief_sessions'::regclass AND conname = 'brief_sessions_session_id_key'
			) THEN
				ALTER TABLE brief_sessions ADD CONSTRAINT brief_sessions_session_id_key UNIQUE (session_id);
			END IF;
		END $$`,

		// Drop old single-article columns if they exist (legacy schema).
		`ALTER TABLE brief_sessions DROP COLUMN IF EXISTS article_id`,
		`ALTER TABLE brief_sessions DROP COLUMN IF EXISTS article_url`,
		`ALTER TABLE brief_sessions DROP COLUMN IF EXISTS article_title`,
		`ALTER TABLE brief_sessions DROP COLUMN IF EXISTS score`,

		`CREATE INDEX IF NOT EXISTS idx_brief_sessions_session_id ON brief_sessions (session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_brief_sessions_queried_at ON brief_sessions (queried_at DESC)`,

		`CREATE TABLE IF NOT EXISTS brief_session_articles (
			id          SERIAL PRIMARY KEY,
			session_id  TEXT NOT NULL REFERENCES brief_sessions(session_id) ON DELETE CASCADE,
			article_id  TEXT REFERENCES articles(article_id) ON DELETE SET NULL,
			url         TEXT,
			title       TEXT,
			score       REAL,
			rank        INT
		)`,

		`CREATE INDEX IF NOT EXISTS idx_brief_session_articles_session ON brief_session_articles (session_id)`,

		`CREATE TABLE IF NOT EXISTS brief_session_works (
			id          SERIAL PRIMARY KEY,
			session_id  TEXT NOT NULL REFERENCES brief_sessions(session_id) ON DELETE CASCADE,
			work_id     INT REFERENCES works(work_id) ON DELETE SET NULL,
			work_type   TEXT,
			title       TEXT,
			authors     TEXT,
			doi         TEXT,
			arxiv_id    TEXT,
			isbn13      TEXT,
			publish_year INT,
			score       REAL,
			rank        INT
		)`,

		`CREATE INDEX IF NOT EXISTS idx_brief_session_works_session ON brief_session_works (session_id)`,

		`CREATE TABLE IF NOT EXISTS consolidator_surfaced (
			id          SERIAL PRIMARY KEY,
			work_id     INT  REFERENCES works(work_id) ON DELETE SET NULL,
			article_id  TEXT REFERENCES articles(article_id) ON DELETE SET NULL,
			session_id  TEXT,
			surfaced_at TIMESTAMPTZ DEFAULT now(),
			score       REAL
		)`,

		`CREATE INDEX IF NOT EXISTS idx_consolidator_surfaced_work    ON consolidator_surfaced (work_id)`,
		`CREATE INDEX IF NOT EXISTS idx_consolidator_surfaced_article ON consolidator_surfaced (article_id)`,
		`CREATE INDEX IF NOT EXISTS idx_consolidator_surfaced_at      ON consolidator_surfaced (surfaced_at DESC)`,
		// HNSW indexes on vector(4096) columns can be added manually once the corpus
		// is large enough to benefit from ANN indexing:
		//   CREATE INDEX idx_articles_embedding ON articles USING hnsw (embedding vector_cosine_ops);
		//   CREATE INDEX idx_works_embedding    ON works    USING hnsw (embedding vector_cosine_ops);
	}

	for _, stmt := range statements {
		if _, err := db.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}

	return nil
}
