package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// WorkInput mirrors the fields of a WorkCandidate for storage.
type WorkInput struct {
	ReferenceID  string
	SearchSource string
	WorkType     string
	Title        string
	Authors      []string
	ISBN         string
	ISBN13       string
	ISSN         string
	DOI          string
	ArXivID      string
	PublishYear  int
	Publisher    string
	CoverURL     string
	Relevance    float64
}

// CanonicalID computes the dedup key for a work:
//
//	isbn13:{value}  — if ISBN-13 is known (cross-source dedup for books)
//	doi:{value}     — if DOI is known (cross-source dedup for papers)
//	arxiv:{value}   — if arXiv ID is known without DOI
//	ref:{id}        — fallback when no canonical identifier is available
func CanonicalID(w WorkInput) string {
	if w.ISBN13 != "" {
		return "isbn13:" + w.ISBN13
	}
	if w.DOI != "" {
		return "doi:" + w.DOI
	}
	if w.ArXivID != "" {
		return "arxiv:" + w.ArXivID
	}
	return "ref:" + w.ReferenceID
}

// UpsertWork inserts or updates a work by canonical_id.
// When the same canonical_id already exists, it merges reference_ids and sources arrays.
// Returns the internal work_id.
func (db *DB) UpsertWork(ctx context.Context, w WorkInput) (int64, error) {
	canonical := CanonicalID(w)

	authorsJSON, err := marshalStringSlice(w.Authors)
	if err != nil {
		return 0, fmt.Errorf("marshal authors: %w", err)
	}

	refIDsJSON, err := json.Marshal([]string{w.ReferenceID})
	if err != nil {
		return 0, fmt.Errorf("marshal reference_ids: %w", err)
	}
	sourcesJSON, err := json.Marshal([]string{w.SearchSource})
	if err != nil {
		return 0, fmt.Errorf("marshal sources: %w", err)
	}

	const q = `
		INSERT INTO works (
			canonical_id, reference_ids, sources, work_type, title, authors,
			publisher, publish_year, isbn, isbn13, issn, doi, arxiv_id, cover_url
		) VALUES ($1, $2::jsonb, $3::jsonb, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (canonical_id) DO UPDATE SET
			reference_ids = (
				SELECT jsonb_agg(DISTINCT v)
				FROM jsonb_array_elements(works.reference_ids || EXCLUDED.reference_ids) v
			),
			sources = (
				SELECT jsonb_agg(DISTINCT v)
				FROM jsonb_array_elements(works.sources || EXCLUDED.sources) v
			)
		RETURNING work_id
	`

	var workID int64
	err = db.pool.QueryRow(ctx, q,
		canonical,
		refIDsJSON,
		sourcesJSON,
		w.WorkType,
		w.Title,
		authorsJSON,
		nilIfEmpty(w.Publisher),
		nilIfZero(w.PublishYear),
		nilIfEmpty(w.ISBN),
		nilIfEmpty(w.ISBN13),
		nilIfEmpty(w.ISSN),
		nilIfEmpty(w.DOI),
		nilIfEmpty(w.ArXivID),
		nilIfEmpty(w.CoverURL),
	).Scan(&workID)
	if err != nil {
		return 0, fmt.Errorf("upsert work: %w", err)
	}

	return workID, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
