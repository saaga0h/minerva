package store

import (
	"context"
	"fmt"
)

// LinkArticleWork creates an article_works junction record.
// On conflict (same article + same work already linked) it does nothing.
func (db *DB) LinkArticleWork(ctx context.Context, articleID string, workID int64, searchSource string, relevance float64) error {
	const q = `
		INSERT INTO article_works (article_id, work_id, search_source, relevance)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (article_id, work_id) DO NOTHING
	`
	if _, err := db.pool.Exec(ctx, q, articleID, workID, searchSource, relevance); err != nil {
		return fmt.Errorf("link article work: %w", err)
	}
	return nil
}

// MarkOwnedInKoha sets owned_in_koha=true for all links between the given article
// and the work identified by its canonical_id.
func (db *DB) MarkOwnedInKoha(ctx context.Context, articleID, canonicalID string) error {
	const q = `
		UPDATE article_works aw
		SET owned_in_koha = TRUE
		FROM works w
		WHERE aw.work_id    = w.work_id
		  AND aw.article_id = $1
		  AND w.canonical_id = $2
	`
	if _, err := db.pool.Exec(ctx, q, articleID, canonicalID); err != nil {
		return fmt.Errorf("mark owned in koha: %w", err)
	}
	return nil
}
