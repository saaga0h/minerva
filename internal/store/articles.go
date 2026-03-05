package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

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

	const q = `
		INSERT INTO articles (article_id, url, title, source, domain, article_type,
		                      summary, keywords, concepts, related_topics, entities, insights, analyzed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (article_id) DO UPDATE SET
			domain         = EXCLUDED.domain,
			article_type   = EXCLUDED.article_type,
			summary        = EXCLUDED.summary,
			keywords       = EXCLUDED.keywords,
			concepts       = EXCLUDED.concepts,
			related_topics = EXCLUDED.related_topics,
			entities       = EXCLUDED.entities,
			insights       = EXCLUDED.insights,
			analyzed_at    = EXCLUDED.analyzed_at
	`
	_, err = db.pool.Exec(ctx, q,
		a.ArticleID, a.URL, a.Title, a.Source,
		a.Domain, a.ArticleType, a.Summary,
		keywordsJSON, conceptsJSON, relatedJSON, entitiesJSON,
		a.Insights, a.AnalyzedAt,
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
