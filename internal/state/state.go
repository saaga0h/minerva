package state

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DB tracks which article URLs have been published to the MQTT bus and which have
// completed the full pipeline. Used only by source primitives for dedup.
type DB struct {
	conn *sql.DB
}

// ArticleState holds the state for a single article URL.
type ArticleState struct {
	URL         string
	ArticleID   string
	Title       string
	PublishedAt time.Time
	CompletedAt *time.Time // nil means pipeline not yet complete
}

// New opens (or creates) the state SQLite database at the given path.
func New(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create state DB directory: %w", err)
	}

	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open state DB: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to migrate state DB: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	_, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS article_state (
			url          TEXT PRIMARY KEY,
			article_id   TEXT NOT NULL,
			title        TEXT NOT NULL DEFAULT '',
			published_at TIMESTAMP NOT NULL,
			completed_at TIMESTAMP
		)
	`)
	return err
}

// ArticleID returns a stable, short ID for a URL: the first 16 hex chars of its SHA256.
func ArticleID(url string) string {
	sum := sha256.Sum256([]byte(url))
	return fmt.Sprintf("%x", sum[:8])
}

// IsComplete returns true if the full pipeline has finished for this URL.
func (db *DB) IsComplete(url string) (bool, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM article_state WHERE url = ? AND completed_at IS NOT NULL", url,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("IsComplete query failed: %w", err)
	}
	return count > 0, nil
}

// MarkPublished records that this URL has been published to the bus.
func (db *DB) MarkPublished(url, articleID, title string) error {
	_, err := db.conn.Exec(
		`INSERT OR IGNORE INTO article_state (url, article_id, title, published_at) VALUES (?, ?, ?, ?)`,
		url, articleID, title, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("MarkPublished failed: %w", err)
	}
	return nil
}

// MarkComplete records that the full pipeline has finished for this URL.
func (db *DB) MarkComplete(url string) error {
	_, err := db.conn.Exec(
		`UPDATE article_state SET completed_at = ? WHERE url = ?`,
		time.Now(), url,
	)
	if err != nil {
		return fmt.Errorf("MarkComplete failed: %w", err)
	}
	return nil
}

// MarkCompleteByArticleID marks an article as complete using its stable article ID.
func (db *DB) MarkCompleteByArticleID(articleID string) error {
	_, err := db.conn.Exec(
		`UPDATE article_state SET completed_at = ? WHERE article_id = ?`,
		time.Now(), articleID,
	)
	if err != nil {
		return fmt.Errorf("MarkCompleteByArticleID failed: %w", err)
	}
	return nil
}

// PendingArticles returns articles that were published but never completed.
// These are candidates for re-publishing on the next pipeline run.
func (db *DB) PendingArticles() ([]ArticleState, error) {
	rows, err := db.conn.Query(
		`SELECT url, article_id, title, published_at FROM article_state WHERE completed_at IS NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("PendingArticles query failed: %w", err)
	}
	defer rows.Close()

	var result []ArticleState
	for rows.Next() {
		var a ArticleState
		if err := rows.Scan(&a.URL, &a.ArticleID, &a.Title, &a.PublishedAt); err != nil {
			return nil, fmt.Errorf("PendingArticles scan failed: %w", err)
		}
		result = append(result, a)
	}
	return result, nil
}
