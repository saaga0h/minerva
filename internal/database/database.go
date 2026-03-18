package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

// Article represents an RSS article with processing metadata
type Article struct {
	ID          int       `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	Summary     string    `json:"summary"`
	Keywords    []string  `json:"keywords"`
	Insights    string    `json:"insights"`
	ProcessedAt time.Time `json:"processed_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type RelatedReading struct {
	ID        int       `json:"id"`
	ArticleID int       `json:"article_id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Type      string    `json:"type"` // "article", "blog", "discussion", "paper"
	Relevance float64   `json:"relevance"`
	CreatedAt time.Time `json:"created_at"`
}

// AnalyzedArticleRecord holds LLM-extracted metadata for an article.
// ArticleID is the hex Envelope ArticleID (TEXT), not the integer PK from the articles table.
type AnalyzedArticleRecord struct {
	ArticleID string
	SourceID  string
	URL       string
	Title     string
	Domain    string
	Summary   string
	Keywords  []string
	Concepts  []string
	Insights  string
}

// DigestEntry is returned by GetRecommendationsSince for the notifier digest.
type DigestEntry struct {
	ArticleTitle string
	ArticleURL   string
	SourceID     string
	BookTitle    string
	BookAuthor   string
	SourceKey    string
	OwnedInKoha  bool
	Relevance    float64
	CreatedAt    time.Time
}

/* New proper book recommendations */
type BookRecommendation struct {
	ID          int       `json:"id"`
	ArticleID   int       `json:"article_id"`
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	ISBN        string    `json:"isbn"`
	ISBN13      string    `json:"isbn13"`
	PublishYear int       `json:"publish_year"`
	Publisher   string    `json:"publisher"`
	CoverURL    string    `json:"cover_url"`
	SourceKey   string    `json:"source_key"`
	OwnedInKoha bool      `json:"owned_in_koha"`
	Relevance   float64   `json:"relevance"`
	CreatedAt   time.Time `json:"created_at"`
}

type OwnedBook struct {
	ID        int       `json:"id"`
	ArticleID int       `json:"article_id"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	ISBN      string    `json:"isbn"`
	CallNum   string    `json:"call_number"` // From Koha
	KohaID    string    `json:"koha_id"`
	MatchType string    `json:"match_type"` // "keyword" or "exact"
	CreatedAt time.Time `json:"created_at"`
}

// New creates a new database connection and initializes tables
func New(dbPath string) (*DB, error) {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db := &DB{conn: conn}

	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.conn.Close()
}

// migrate creates or updates database schema
func (db *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS articles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			summary TEXT,
			keywords TEXT,
			insights TEXT,
			processed_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS book_recommendations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			article_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			author TEXT,
			isbn TEXT,
			isbn13 TEXT,
			publish_year INTEGER,
			publisher TEXT,
			cover_url TEXT,
			openlibrary_key TEXT,
			owned_in_koha BOOLEAN DEFAULT 0,
			relevance REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (article_id) REFERENCES articles (id)
		)`,

		`CREATE TABLE IF NOT EXISTS related_reading (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			article_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			url TEXT,
			type TEXT,
			relevance REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (article_id) REFERENCES articles (id)
		)`,

		`CREATE INDEX IF NOT EXISTS idx_articles_url ON articles(url)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_processed_at ON articles(processed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_book_recommendations_article_id ON book_recommendations(article_id)`,
		`CREATE INDEX IF NOT EXISTS idx_book_recommendations_isbn ON book_recommendations(isbn)`,
		`CREATE INDEX IF NOT EXISTS idx_book_recommendations_isbn13 ON book_recommendations(isbn13)`,
		`CREATE INDEX IF NOT EXISTS idx_related_reading_article_id ON related_reading(article_id)`,

		`CREATE TABLE IF NOT EXISTS analyzed_articles (
			article_id  TEXT PRIMARY KEY,
			source_id   TEXT,
			url         TEXT NOT NULL,
			title       TEXT,
			domain      TEXT,
			summary     TEXT,
			keywords    TEXT,
			concepts    TEXT,
			insights    TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE INDEX IF NOT EXISTS idx_analyzed_articles_article_id ON analyzed_articles(article_id)`,
	}

	for _, migration := range migrations {
		if _, err := db.conn.Exec(migration); err != nil {
			return fmt.Errorf("failed to execute migration: %w", err)
		}
	}

	// Rename openlibrary_key → source_key. Silently ignored if already renamed
	// (SQLite errors when the old column doesn't exist).
	db.conn.Exec(`ALTER TABLE book_recommendations RENAME COLUMN openlibrary_key TO source_key`)

	// Deduplicate book_recommendations before creating the unique index.
	// Keeps the row with the lowest id for each (article_id, source_key) pair.
	// Silent — safe to run repeatedly.
	db.conn.Exec(`DELETE FROM book_recommendations
		WHERE id NOT IN (
			SELECT MIN(id) FROM book_recommendations GROUP BY article_id, source_key
		)`)

	// Create unique index for upsert support. Silent — fails harmlessly if already exists.
	db.conn.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_book_recs_article_source
		ON book_recommendations(article_id, source_key)`)

	// Add koha_id column to book_recommendations (silent — fails if already exists).
	db.conn.Exec(`ALTER TABLE book_recommendations ADD COLUMN koha_id TEXT`)

	// Add envelope_article_id column to book_recommendations (silent — fails if already exists).
	db.conn.Exec(`ALTER TABLE book_recommendations ADD COLUMN envelope_article_id TEXT`)

	return nil
}

// GetArticleIDByURL returns the database ID for an article with the given URL.
func (db *DB) GetArticleIDByURL(url string) (int, error) {
	var id int
	err := db.conn.QueryRow("SELECT id FROM articles WHERE url = ?", url).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to get article ID by URL: %w", err)
	}
	return id, nil
}

// ArticleExists checks if an article with the given URL already exists
func (db *DB) ArticleExists(url string) (bool, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM articles WHERE url = ?", url).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check article existence: %w", err)
	}
	return count > 0, nil
}

// SaveArticle saves an article to the database
func (db *DB) SaveArticle(article *Article) error {
	// Convert keywords to JSON string
	keywordsJSON := ""
	if len(article.Keywords) > 0 {
		// Simple JSON encoding for keywords
		keywordsJSON = fmt.Sprintf(`["%s"]`, joinStrings(article.Keywords, `","`))
	}

	query := `INSERT INTO articles (url, title, content, summary, keywords, insights, processed_at)
			  VALUES (?, ?, ?, ?, ?, ?, ?)`

	result, err := db.conn.Exec(query,
		article.URL,
		article.Title,
		article.Content,
		article.Summary,
		keywordsJSON,
		article.Insights,
		article.ProcessedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save article: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	article.ID = int(id)
	return nil
}

// GetUnprocessedArticles returns articles that haven't been processed by Ollama yet
func (db *DB) GetUnprocessedArticles() ([]*Article, error) {
	query := `SELECT id, url, title, content, created_at
			  FROM articles 
			  WHERE processed_at IS NULL
			  ORDER BY created_at ASC`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query unprocessed articles: %w", err)
	}
	defer rows.Close()

	var articles []*Article
	for rows.Next() {
		article := &Article{}
		err := rows.Scan(&article.ID, &article.URL, &article.Title, &article.Content, &article.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan article: %w", err)
		}
		articles = append(articles, article)
	}

	return articles, nil
}

// UpdateArticleProcessing updates an article with Ollama processing results
func (db *DB) UpdateArticleProcessing(articleID int, summary, insights string, keywords []string) error {
	keywordsJSON := ""
	if len(keywords) > 0 {
		keywordsJSON = fmt.Sprintf(`["%s"]`, joinStrings(keywords, `","`))
	}

	query := `UPDATE articles 
			  SET summary = ?, keywords = ?, insights = ?, processed_at = CURRENT_TIMESTAMP
			  WHERE id = ?`

	_, err := db.conn.Exec(query, summary, keywordsJSON, insights, articleID)
	if err != nil {
		return fmt.Errorf("failed to update article processing: %w", err)
	}

	return nil
}

// SaveBookRecommendation saves a book recommendation
func (db *DB) SaveBookRecommendation(rec *BookRecommendation) error {
	query := `INSERT INTO book_recommendations
			  (article_id, title, author, isbn, isbn13, publish_year, publisher,
			   cover_url, source_key, owned_in_koha, relevance)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	result, err := db.conn.Exec(query,
		rec.ArticleID,
		rec.Title,
		rec.Author,
		rec.ISBN,
		rec.ISBN13,
		rec.PublishYear,
		rec.Publisher,
		rec.CoverURL,
		rec.SourceKey,
		rec.OwnedInKoha,
		rec.Relevance,
	)
	if err != nil {
		return fmt.Errorf("failed to save book recommendation: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	rec.ID = int(id)
	return nil
}

func (db *DB) SaveRelatedReading(reading *RelatedReading) error {
	query := `INSERT INTO related_reading (article_id, title, url, type, relevance)
			  VALUES (?, ?, ?, ?, ?)`

	result, err := db.conn.Exec(query,
		reading.ArticleID,
		reading.Title,
		reading.URL,
		reading.Type,
		reading.Relevance,
	)
	if err != nil {
		return fmt.Errorf("failed to save related reading: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	reading.ID = int(id)
	return nil
}

// GetProcessedArticlesWithoutRecommendations returns articles that have been processed by Ollama
// but don't have book recommendations yet
func (db *DB) GetProcessedArticlesWithoutRecommendations() ([]*Article, error) {
	query := `SELECT DISTINCT a.id, a.url, a.title, a.content, a.summary, a.keywords, a.insights, a.processed_at, a.created_at
			  FROM articles a
			  LEFT JOIN book_recommendations br ON a.id = br.article_id
			  WHERE a.processed_at IS NOT NULL
			  AND br.id IS NULL
			  ORDER BY a.processed_at DESC`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query processed articles: %w", err)
	}
	defer rows.Close()

	var articles []*Article
	for rows.Next() {
		article := &Article{}
		var keywordsJSON string
		err := rows.Scan(
			&article.ID,
			&article.URL,
			&article.Title,
			&article.Content,
			&article.Summary,
			&keywordsJSON,
			&article.Insights,
			&article.ProcessedAt,
			&article.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan article: %w", err)
		}

		// Parse keywords JSON
		if keywordsJSON != "" {
			article.Keywords = parseKeywordsJSON(keywordsJSON)
		}

		articles = append(articles, article)
	}

	return articles, nil
}

func (db *DB) GetUncheckedRecommendations() ([]BookRecommendation, error) {
	query := `SELECT id, article_id, title, author, isbn, isbn13, publish_year,
			  publisher, cover_url, source_key, relevance
			  FROM book_recommendations
			  WHERE owned_in_koha = 0`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query unchecked recommendations: %w", err)
	}
	defer rows.Close()

	var recommendations []BookRecommendation
	for rows.Next() {
		var rec BookRecommendation
		err := rows.Scan(
			&rec.ID,
			&rec.ArticleID,
			&rec.Title,
			&rec.Author,
			&rec.ISBN,
			&rec.ISBN13,
			&rec.PublishYear,
			&rec.Publisher,
			&rec.CoverURL,
			&rec.SourceKey,
			&rec.Relevance,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan recommendation: %w", err)
		}
		recommendations = append(recommendations, rec)
	}

	return recommendations, nil
}

// MarkBookAsOwned marks a book recommendation as owned in Koha
func (db *DB) MarkBookAsOwned(recommendationID int) error {
	query := `UPDATE book_recommendations SET owned_in_koha = 1 WHERE id = ?`

	_, err := db.conn.Exec(query, recommendationID)
	if err != nil {
		return fmt.Errorf("failed to mark book as owned: %w", err)
	}

	return nil
}

// SaveAnalyzedArticle stores LLM-extracted metadata for an article.
// Uses INSERT OR REPLACE so re-analysis cleanly overwrites the previous record.
func (db *DB) SaveAnalyzedArticle(a AnalyzedArticleRecord) error {
	keywordsJSON, _ := json.Marshal(a.Keywords)
	conceptsJSON, _ := json.Marshal(a.Concepts)

	_, err := db.conn.Exec(
		`INSERT OR REPLACE INTO analyzed_articles
			(article_id, source_id, url, title, domain, summary, keywords, concepts, insights)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ArticleID, a.SourceID, a.URL, a.Title, a.Domain, a.Summary,
		string(keywordsJSON), string(conceptsJSON), a.Insights,
	)
	if err != nil {
		return fmt.Errorf("failed to save analyzed article: %w", err)
	}
	return nil
}

// UpsertBookCandidate inserts or updates a book recommendation.
// Matches on (article_id, source_key); updates all fields except owned_in_koha and koha_id.
func (db *DB) UpsertBookCandidate(articleDBID int, envelopeArticleID string, rec BookRecommendation) error {
	_, err := db.conn.Exec(
		`INSERT INTO book_recommendations
			(article_id, envelope_article_id, title, author, isbn, isbn13, publish_year,
			 publisher, cover_url, source_key, owned_in_koha, relevance)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(article_id, source_key) DO UPDATE SET
			title=excluded.title,
			author=excluded.author,
			isbn=excluded.isbn,
			isbn13=excluded.isbn13,
			publish_year=excluded.publish_year,
			publisher=excluded.publisher,
			cover_url=excluded.cover_url,
			relevance=excluded.relevance,
			envelope_article_id=excluded.envelope_article_id`,
		articleDBID, envelopeArticleID,
		rec.Title, rec.Author, rec.ISBN, rec.ISBN13, rec.PublishYear,
		rec.Publisher, rec.CoverURL, rec.SourceKey, rec.Relevance,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert book candidate: %w", err)
	}
	return nil
}

// UpdateKohaOwnershipByTitle sets owned_in_koha and koha_id for a book matched by title
// within a given article. Title+article is a reliable enough match since title collisions
// within a single article are extremely unlikely.
func (db *DB) UpdateKohaOwnershipByTitle(articleDBID int, title string, kohaID string) error {
	_, err := db.conn.Exec(
		`UPDATE book_recommendations SET owned_in_koha=1, koha_id=?
		 WHERE article_id=? AND title=?`,
		kohaID, articleDBID, title,
	)
	if err != nil {
		return fmt.Errorf("failed to update koha ownership: %w", err)
	}
	return nil
}

// GetRecommendationsSince returns book recommendations created after the given time,
// joined with analyzed_articles for article metadata.
func (db *DB) GetRecommendationsSince(since time.Time) ([]DigestEntry, error) {
	rows, err := db.conn.Query(
		`SELECT
			COALESCE(aa.title, a.title), a.url,
			COALESCE(aa.source_id, ''),
			br.title, br.author, br.source_key,
			br.owned_in_koha, br.relevance, br.created_at
		FROM book_recommendations br
		JOIN articles a ON a.id = br.article_id
		LEFT JOIN analyzed_articles aa ON aa.article_id = br.envelope_article_id
		WHERE br.created_at >= ?
		ORDER BY br.created_at DESC`,
		since,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query recommendations: %w", err)
	}
	defer rows.Close()

	var entries []DigestEntry
	for rows.Next() {
		var e DigestEntry
		var ownedInt int
		if err := rows.Scan(
			&e.ArticleTitle, &e.ArticleURL, &e.SourceID,
			&e.BookTitle, &e.BookAuthor, &e.SourceKey,
			&ownedInt, &e.Relevance, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan digest entry: %w", err)
		}
		e.OwnedInKoha = ownedInt != 0
		entries = append(entries, e)
	}
	return entries, nil
}

// parseKeywordsJSON converts JSON string to string slice
func parseKeywordsJSON(jsonStr string) []string {
	// Remove brackets and quotes: ["keyword1","keyword2"] -> keyword1,keyword2
	jsonStr = strings.Trim(jsonStr, "[]")
	if jsonStr == "" {
		return []string{}
	}

	parts := strings.Split(jsonStr, ",")
	var keywords []string
	for _, part := range parts {
		keyword := strings.Trim(part, `"' `)
		if keyword != "" {
			keywords = append(keywords, keyword)
		}
	}
	return keywords
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
