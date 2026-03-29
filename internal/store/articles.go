package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	pgvector "github.com/pgvector/pgvector-go"
)

// ArticleID returns a stable, short ID for a URL: the first 16 hex chars of its SHA256.
// This is the canonical article identifier used across all pipeline stages.
func ArticleID(url string) string {
	sum := sha256.Sum256([]byte(url))
	return fmt.Sprintf("%x", sum[:8])
}

// ArticleState holds the state for a pending article (published but not yet completed).
type ArticleState struct {
	URL         string
	ArticleID   string
	Title       string
	PublishedAt time.Time
}

// IsComplete returns true if the full pipeline has finished for this URL.
func (db *DB) IsComplete(ctx context.Context, url string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM articles WHERE url = $1 AND completed_at IS NOT NULL)`,
		url,
	).Scan(&exists)
	return exists, err
}

// MarkPublished records that this URL has been published to the MQTT bus by the given source.
// Idempotent: preserves the original published_at if the record already exists.
func (db *DB) MarkPublished(ctx context.Context, url, articleID, title, source string) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO articles (article_id, url, title, source, published_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (url) DO UPDATE
			SET published_at = COALESCE(articles.published_at, EXCLUDED.published_at)
	`, articleID, url, title, source)
	return err
}

// MarkCompleteByArticleID records that the full pipeline has finished for this article.
func (db *DB) MarkCompleteByArticleID(ctx context.Context, articleID string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE articles SET completed_at = now() WHERE article_id = $1`,
		articleID,
	)
	return err
}

// PendingArticles returns articles published by the given source that never completed.
// Called on startup to re-publish articles from a previous incomplete run.
func (db *DB) PendingArticles(ctx context.Context, source string) ([]ArticleState, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT url, article_id, title, published_at
		FROM articles
		WHERE source = $1 AND published_at IS NOT NULL AND completed_at IS NULL
	`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ArticleState
	for rows.Next() {
		var a ArticleState
		if err := rows.Scan(&a.URL, &a.ArticleID, &a.Title, &a.PublishedAt); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// ArticleAnalysis holds the full AnalyzedArticle payload for storage.
type ArticleAnalysis struct {
	ArticleID     string
	URL           string
	Title         string
	Source        string
	Domain        string
	ArticleType   string
	Summary       string
	Keywords      []string
	Concepts      []string
	RelatedTopics []string
	Entities      map[string]any // serialised entities JSON
	Insights      string
	AnalyzedAt    time.Time
	Embedding     []float32 // semantic embedding vector; nil if unavailable
}

// UpsertArticleContent stores or updates the article record with extracted content.
// Called when the store receives a minerva/articles/extracted message.
func (db *DB) UpsertArticleContent(ctx context.Context, articleID, url, title, source, content string, extractedAt time.Time) error {
	const q = `
		INSERT INTO articles (article_id, url, title, source, content, extracted_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (article_id) DO UPDATE SET
			content      = EXCLUDED.content,
			extracted_at = EXCLUDED.extracted_at
	`
	if _, err := db.pool.Exec(ctx, q, articleID, url, title, source, content, extractedAt); err != nil {
		return fmt.Errorf("upsert article content: %w", err)
	}
	return nil
}

// UpsertArticleAnalysis stores or updates the article record with LLM analysis results.
// Called when the store receives a minerva/articles/analyzed message.
func (db *DB) UpsertArticleAnalysis(ctx context.Context, a ArticleAnalysis) error {
	keywordsJSON, err := marshalStringSlice(a.Keywords)
	if err != nil {
		return fmt.Errorf("marshal keywords: %w", err)
	}
	conceptsJSON, err := marshalStringSlice(a.Concepts)
	if err != nil {
		return fmt.Errorf("marshal concepts: %w", err)
	}
	relatedJSON, err := marshalStringSlice(a.RelatedTopics)
	if err != nil {
		return fmt.Errorf("marshal related_topics: %w", err)
	}
	entitiesJSON, err := json.Marshal(a.Entities)
	if err != nil {
		return fmt.Errorf("marshal entities: %w", err)
	}

	// Encode embedding: nil slice → NULL in Postgres (don't overwrite existing).
	var embeddingArg any
	if len(a.Embedding) > 0 {
		embeddingArg = pgvector.NewVector(a.Embedding)
	}

	const q = `
		INSERT INTO articles (article_id, url, title, source, domain, article_type,
		                      summary, keywords, concepts, related_topics, entities, insights, analyzed_at, embedding)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (article_id) DO UPDATE SET
			domain         = EXCLUDED.domain,
			article_type   = EXCLUDED.article_type,
			summary        = EXCLUDED.summary,
			keywords       = EXCLUDED.keywords,
			concepts       = EXCLUDED.concepts,
			related_topics = EXCLUDED.related_topics,
			entities       = EXCLUDED.entities,
			insights       = EXCLUDED.insights,
			analyzed_at    = EXCLUDED.analyzed_at,
			embedding      = COALESCE(EXCLUDED.embedding, articles.embedding)
	`
	_, err = db.pool.Exec(ctx, q,
		a.ArticleID, a.URL, a.Title, a.Source,
		a.Domain, a.ArticleType, a.Summary,
		keywordsJSON, conceptsJSON, relatedJSON, entitiesJSON,
		a.Insights, a.AnalyzedAt, embeddingArg,
	)
	if err != nil {
		return fmt.Errorf("upsert article analysis: %w", err)
	}
	return nil
}

func marshalStringSlice(ss []string) ([]byte, error) {
	if ss == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(ss)
}
