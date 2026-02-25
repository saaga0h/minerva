package mqtt

import "time"

// Envelope is the common header carried in every message.
type Envelope struct {
	MessageID string    `json:"message_id"` // UUID, unique per message
	ArticleID string    `json:"article_id"` // stable ID: SHA256 of URL
	Source    string    `json:"source"`     // e.g. "freshrss", "miniflux"
	Timestamp time.Time `json:"timestamp"`
}

// RawArticle is published by source primitives to TopicArticlesRaw.
// No content — the extractor fetches full content from the URL.
type RawArticle struct {
	Envelope
	URL   string `json:"url"`
	Title string `json:"title"`
}

// ExtractedArticle is published by the extractor to TopicArticlesExtracted.
type ExtractedArticle struct {
	Envelope
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"` // clean text, ready for LLM
}

// AnalyzedArticle is published by the analyzer to TopicArticlesAnalyzed.
// Content is intentionally dropped — it's large and not needed downstream.
type AnalyzedArticle struct {
	Envelope
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Domain   string   `json:"domain"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	Concepts []string `json:"concepts"`
	Insights string   `json:"insights"`
}

// BookCandidates is published by the book-search primitive to TopicBooksCandidates.
type BookCandidates struct {
	Envelope
	ArticleTitle string          `json:"article_title"`
	ArticleURL   string          `json:"article_url"`
	Books        []BookCandidate `json:"books"`
}

// BookCandidate represents a single book recommendation.
type BookCandidate struct {
	Title          string  `json:"title"`
	Author         string  `json:"author"`
	ISBN           string  `json:"isbn"`
	ISBN13         string  `json:"isbn13"`
	PublishYear    int     `json:"publish_year"`
	Publisher      string  `json:"publisher"`
	CoverURL       string  `json:"cover_url"`
	OpenLibraryKey string  `json:"openlibrary_key"`
	Relevance      float64 `json:"relevance"`
}

// CheckedBooks is published by the koha-check primitive to TopicBooksChecked.
type CheckedBooks struct {
	Envelope
	ArticleTitle string          `json:"article_title"`
	ArticleURL   string          `json:"article_url"`
	NewBooks     []BookCandidate `json:"new_books"`  // not in Koha catalog
	OwnedBooks   []OwnedBook     `json:"owned_books"` // already owned
}

// OwnedBook is a book found in the Koha library catalog.
type OwnedBook struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	KohaID string `json:"koha_id"`
}

// ArticleComplete is published by the notifier to TopicArticlesComplete.
// Source primitives subscribe to this to mark articles as done in their state DB.
type ArticleComplete struct {
	Envelope
	CompletedAt time.Time `json:"completed_at"`
}
