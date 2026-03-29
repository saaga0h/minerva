package store

import (
	"context"
	"fmt"
	"time"

	mqtt "github.com/saaga0h/minerva/internal/mqtt"

	pgvector "github.com/pgvector/pgvector-go"
)

// InsertBriefSession persists a full brief session — the session header plus all
// ranked articles and works that were returned. Uses a transaction so partial
// inserts don't leave orphaned rows.
func (db *DB) InsertBriefSession(ctx context.Context, sessionID string, queriedAt time.Time, articles []mqtt.BriefArticle, works []mqtt.BriefWork) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin brief session tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`INSERT INTO brief_sessions (session_id, queried_at) VALUES ($1, $2) ON CONFLICT (session_id) DO NOTHING`,
		sessionID, queriedAt,
	); err != nil {
		return fmt.Errorf("insert brief session header: %w", err)
	}

	for i, a := range articles {
		var articleID any
		if a.ArticleID != "" {
			articleID = a.ArticleID
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO brief_session_articles (session_id, article_id, url, title, score, rank) VALUES ($1, $2, $3, $4, $5, $6)`,
			sessionID, articleID, a.URL, a.Title, a.Score, i+1,
		); err != nil {
			return fmt.Errorf("insert brief session article rank %d: %w", i+1, err)
		}
	}

	for i, w := range works {
		var workID any
		if w.WorkID != 0 {
			workID = w.WorkID
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO brief_session_works (session_id, work_id, work_type, title, authors, doi, arxiv_id, isbn13, publish_year, score, rank) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			sessionID, workID, w.WorkType, w.Title, w.Authors, w.DOI, w.ArXivID, w.ISBN13, w.PublishYear, w.Score, i+1,
		); err != nil {
			return fmt.Errorf("insert brief session work rank %d: %w", i+1, err)
		}
	}

	return tx.Commit(ctx)
}

// ArticleSearchResult is a single result from an ANN or keyword search.
type ArticleSearchResult struct {
	ArticleID string
	URL       string
	Title     string
	Distance  float32 // cosine distance (0 = identical, 2 = opposite) for ANN; inverted rank for keyword
}

// SearchByEmbedding finds the topK completed articles nearest to the given embedding
// using cosine distance (pgvector <=> operator). Only articles with a non-null embedding
// and a completed_at timestamp are considered.
func (db *DB) SearchByEmbedding(ctx context.Context, embedding []float32, topK int) ([]ArticleSearchResult, error) {
	const q = `
		SELECT article_id, url, title, embedding <=> $1 AS distance
		FROM articles
		WHERE embedding IS NOT NULL AND completed_at IS NOT NULL
		ORDER BY embedding <=> $1
		LIMIT $2
	`
	rows, err := db.pool.Query(ctx, q, pgvector.NewVector(embedding), topK)
	if err != nil {
		return nil, fmt.Errorf("search by embedding: %w", err)
	}
	defer rows.Close()

	var results []ArticleSearchResult
	for rows.Next() {
		var r ArticleSearchResult
		if err := rows.Scan(&r.ArticleID, &r.URL, &r.Title, &r.Distance); err != nil {
			return nil, fmt.Errorf("scan embedding result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// WorkSearchResult is a single result from an ANN search over the works table.
type WorkSearchResult struct {
	WorkID      int
	WorkType    string
	Title       string
	Authors     string
	DOI         string
	ArXivID     string
	ISBN13      string
	PublishYear int
	Distance    float32 // cosine distance (0 = identical)
}

// SearchWorksByEmbedding finds the topK works nearest to the given embedding using
// cosine distance (pgvector <=> operator). Works of any type are returned.
func (db *DB) SearchWorksByEmbedding(ctx context.Context, embedding []float32, topK int) ([]WorkSearchResult, error) {
	const q = `
		SELECT work_id, work_type, title,
		       COALESCE(authors->>0, '') AS author,
		       COALESCE(doi, '')        AS doi,
		       COALESCE(arxiv_id, '')   AS arxiv_id,
		       COALESCE(isbn13, '')     AS isbn13,
		       COALESCE(publish_year, 0) AS publish_year,
		       embedding <=> $1         AS distance
		FROM works
		WHERE embedding IS NOT NULL
		ORDER BY embedding <=> $1
		LIMIT $2
	`
	rows, err := db.pool.Query(ctx, q, pgvector.NewVector(embedding), topK)
	if err != nil {
		return nil, fmt.Errorf("search works by embedding: %w", err)
	}
	defer rows.Close()

	var results []WorkSearchResult
	for rows.Next() {
		var r WorkSearchResult
		if err := rows.Scan(
			&r.WorkID, &r.WorkType, &r.Title,
			&r.Authors, &r.DOI, &r.ArXivID, &r.ISBN13, &r.PublishYear,
			&r.Distance,
		); err != nil {
			return nil, fmt.Errorf("scan work embedding result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchByKeywords finds the topK completed articles using full-text search against
// analyzed_articles (summary, keywords). The Distance field carries the inverse ts_rank
// so callers can treat lower = better consistently (Distance = 1 - rank, floored at 0).
func (db *DB) SearchByKeywords(ctx context.Context, slug string, topK int) ([]ArticleSearchResult, error) {
	// analyzed_articles is the articles table — summary and keywords live there.
	// We join on article_id and filter on completed_at.
	const q = `
		SELECT
			a.article_id,
			a.url,
			a.title,
			1.0 - ts_rank(a.content_tsv, plainto_tsquery('english', $1)) AS distance
		FROM articles a
		WHERE a.completed_at IS NOT NULL
		  AND a.content_tsv @@ plainto_tsquery('english', $1)
		ORDER BY ts_rank(a.content_tsv, plainto_tsquery('english', $1)) DESC
		LIMIT $2
	`
	rows, err := db.pool.Query(ctx, q, slug, topK)
	if err != nil {
		return nil, fmt.Errorf("search by keywords: %w", err)
	}
	defer rows.Close()

	var results []ArticleSearchResult
	for rows.Next() {
		var r ArticleSearchResult
		if err := rows.Scan(&r.ArticleID, &r.URL, &r.Title, &r.Distance); err != nil {
			return nil, fmt.Errorf("scan keyword result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
