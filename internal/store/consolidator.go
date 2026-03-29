package store

import (
	"context"
	"fmt"
	"time"
)

// ScoredWork is a work candidate from AggregateSessionScores, with context
// about which Journal session surfaced it and the article it was linked to.
type ScoredWork struct {
	WorkID       int
	WorkType     string
	Title        string
	Authors      string
	DOI          string
	ArXivID      string
	ISBN13       string
	PublishYear  int
	Score        float32
	SessionID    string
	ArticleID    string
	ArticleURL   string
	ArticleTitle string
}

// ScoredArticle is an article candidate from AggregateSessionScores.
type ScoredArticle struct {
	ArticleID string
	URL       string
	Title     string
	Score     float32
	SessionID string
}

// AggregateSessionScores returns the top topN works and articles ranked by their
// maximum score across brief sessions within the lookback window.
// If lookbackHours == 0, all sessions are considered regardless of age.
func (db *DB) AggregateSessionScores(ctx context.Context, lookbackHours int, topN int) ([]ScoredWork, []ScoredArticle, error) {
	works, err := db.aggregateWorks(ctx, lookbackHours, topN)
	if err != nil {
		return nil, nil, err
	}
	articles, err := db.aggregateArticles(ctx, lookbackHours, topN)
	if err != nil {
		return nil, nil, err
	}
	return works, articles, nil
}

func (db *DB) aggregateWorks(ctx context.Context, lookbackHours int, topN int) ([]ScoredWork, error) {
	var timeFilter string
	var args []any
	if lookbackHours > 0 {
		timeFilter = fmt.Sprintf("AND bs.queried_at > now() - interval '%d hours'", lookbackHours)
	}

	q := fmt.Sprintf(`
		WITH max_scores AS (
			SELECT
				bsw.work_id,
				MAX(bsw.score) AS max_score,
				(
					SELECT bsw2.session_id
					FROM brief_session_works bsw2
					WHERE bsw2.work_id = bsw.work_id
					ORDER BY bsw2.score DESC
					LIMIT 1
				) AS best_session_id
			FROM brief_session_works bsw
			JOIN brief_sessions bs ON bs.session_id = bsw.session_id
			WHERE bsw.work_id IS NOT NULL
			%s
			GROUP BY bsw.work_id
			ORDER BY max_score DESC
			LIMIT $1
		)
		SELECT
			ms.work_id,
			ms.max_score,
			ms.best_session_id,
			w.work_type,
			w.title,
			COALESCE(w.authors->>0, '') AS authors,
			COALESCE(w.doi, '')         AS doi,
			COALESCE(w.arxiv_id, '')    AS arxiv_id,
			COALESCE(w.isbn13, '')      AS isbn13,
			COALESCE(w.publish_year, 0) AS publish_year,
			COALESCE(bsa.article_id, '') AS article_id,
			COALESCE(bsa.url, '')        AS article_url,
			COALESCE(bsa.title, '')      AS article_title
		FROM max_scores ms
		JOIN works w ON w.work_id = ms.work_id
		LEFT JOIN LATERAL (
			SELECT article_id, url, title
			FROM brief_session_articles
			WHERE session_id = ms.best_session_id
			ORDER BY score DESC
			LIMIT 1
		) bsa ON true
		ORDER BY ms.max_score DESC
	`, timeFilter)

	args = append(args, topN)
	rows, err := db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate works: %w", err)
	}
	defer rows.Close()

	var results []ScoredWork
	for rows.Next() {
		var w ScoredWork
		if err := rows.Scan(
			&w.WorkID, &w.Score, &w.SessionID,
			&w.WorkType, &w.Title, &w.Authors,
			&w.DOI, &w.ArXivID, &w.ISBN13, &w.PublishYear,
			&w.ArticleID, &w.ArticleURL, &w.ArticleTitle,
		); err != nil {
			return nil, fmt.Errorf("scan scored work: %w", err)
		}
		results = append(results, w)
	}
	return results, rows.Err()
}

func (db *DB) aggregateArticles(ctx context.Context, lookbackHours int, topN int) ([]ScoredArticle, error) {
	var timeFilter string
	if lookbackHours > 0 {
		timeFilter = fmt.Sprintf("AND bs.queried_at > now() - interval '%d hours'", lookbackHours)
	}

	q := fmt.Sprintf(`
		SELECT
			bsa.article_id,
			COALESCE(bsa.url, '')   AS url,
			COALESCE(bsa.title, '') AS title,
			MAX(bsa.score)          AS max_score,
			(
				SELECT bsa2.session_id
				FROM brief_session_articles bsa2
				WHERE bsa2.article_id = bsa.article_id
				ORDER BY bsa2.score DESC
				LIMIT 1
			) AS best_session_id
		FROM brief_session_articles bsa
		JOIN brief_sessions bs ON bs.session_id = bsa.session_id
		WHERE bsa.article_id IS NOT NULL
		%s
		GROUP BY bsa.article_id, bsa.url, bsa.title
		ORDER BY max_score DESC
		LIMIT $1
	`, timeFilter)

	rows, err := db.pool.Query(ctx, q, topN)
	if err != nil {
		return nil, fmt.Errorf("aggregate articles: %w", err)
	}
	defer rows.Close()

	var results []ScoredArticle
	for rows.Next() {
		var a ScoredArticle
		if err := rows.Scan(&a.ArticleID, &a.URL, &a.Title, &a.Score, &a.SessionID); err != nil {
			return nil, fmt.Errorf("scan scored article: %w", err)
		}
		results = append(results, a)
	}
	return results, rows.Err()
}

// IsAlreadySurfaced returns true if the given work or article was surfaced
// within the last dedupHours hours. If dedupHours == 0, always returns false.
func (db *DB) IsAlreadySurfaced(ctx context.Context, workID int, articleID string, dedupHours int) (bool, error) {
	if dedupHours == 0 {
		return false, nil
	}

	q := fmt.Sprintf(`
		SELECT EXISTS (
			SELECT 1 FROM consolidator_surfaced
			WHERE surfaced_at > now() - interval '%d hours'
			  AND (($1::int > 0 AND work_id = $1) OR ($2 != '' AND article_id = $2))
		)
	`, dedupHours)
	var exists bool
	if err := db.pool.QueryRow(ctx, q, workID, articleID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check surfaced: %w", err)
	}
	return exists, nil
}

// RecordSurfaced logs that a work/article was surfaced in a notification.
func (db *DB) RecordSurfaced(ctx context.Context, workID int, articleID, sessionID string, score float32) error {
	var wid any
	if workID != 0 {
		wid = workID
	}
	var aid any
	if articleID != "" {
		aid = articleID
	}
	const q = `
		INSERT INTO consolidator_surfaced (work_id, article_id, session_id, surfaced_at, score)
		VALUES ($1, $2, $3, $4, $5)
	`
	if _, err := db.pool.Exec(ctx, q, wid, aid, sessionID, time.Now(), score); err != nil {
		return fmt.Errorf("record surfaced: %w", err)
	}
	return nil
}
