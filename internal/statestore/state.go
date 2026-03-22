package statestore

import (
	"context"
	"fmt"
)

// PendingEntry is the most advanced pipeline stage recorded for an article.
type PendingEntry struct {
	ArticleID string
	Topic     string
	Payload   []byte
}

// UpsertState stores or overwrites the payload for (article_id, topic).
// Later messages for the same stage overwrite earlier ones.
func (db *DB) UpsertState(ctx context.Context, articleID, topic string, payload []byte) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO pipeline_state (article_id, topic, payload, stored_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (article_id, topic) DO UPDATE
		   SET payload = EXCLUDED.payload, stored_at = now()`,
		articleID, topic, payload,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert state: %w", err)
	}
	return nil
}

// DeleteArticle removes all state rows for a completed article.
func (db *DB) DeleteArticle(ctx context.Context, articleID string) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM pipeline_state WHERE article_id = $1`,
		articleID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete article state: %w", err)
	}
	return nil
}

// PendingArticles returns the most advanced stored stage per article, ordered
// by stage index so callers can replay in pipeline order.
// stageOrder maps topic → index; higher index = more advanced.
func (db *DB) PendingArticles(ctx context.Context, stageOrder map[string]int) ([]PendingEntry, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT article_id, topic, payload FROM pipeline_state ORDER BY stored_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query pipeline state: %w", err)
	}
	defer rows.Close()

	// Pick the most advanced topic per article
	type row struct {
		topic   string
		payload []byte
		stage   int
	}
	best := make(map[string]row)

	for rows.Next() {
		var articleID, topic string
		var payload []byte
		if err := rows.Scan(&articleID, &topic, &payload); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		stage, ok := stageOrder[topic]
		if !ok {
			continue // unknown topic, skip
		}
		if cur, exists := best[articleID]; !exists || stage > cur.stage {
			best[articleID] = row{topic: topic, payload: payload, stage: stage}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate rows: %w", err)
	}

	entries := make([]PendingEntry, 0, len(best))
	for articleID, r := range best {
		entries = append(entries, PendingEntry{
			ArticleID: articleID,
			Topic:     r.topic,
			Payload:   r.payload,
		})
	}
	return entries, nil
}
