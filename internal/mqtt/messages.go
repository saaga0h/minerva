package mqtt

import "time"

// Envelope is the common header carried in every message.
type Envelope struct {
	MessageID string    `json:"message_id"`           // UUID, unique per message
	ArticleID string    `json:"article_id"`           // stable ID: SHA256 of URL
	Source    string    `json:"source"`               // e.g. "freshrss", "miniflux"
	SourceID  string    `json:"source_id,omitempty"`  // source-native ID, e.g. "miniflux:12345"
	Timestamp time.Time `json:"timestamp"`
}

// RawArticle is published by source primitives to TopicArticlesRaw.
// Content is optional — when non-empty the extractor skips the URL fetch and
// uses this directly (e.g. RSS item body from Miniflux). May be HTML or plain text.
type RawArticle struct {
	Envelope
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content,omitempty"`
}

// ExtractedArticle is published by the extractor to TopicArticlesExtracted.
type ExtractedArticle struct {
	Envelope
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"` // clean text, ready for LLM
}

// ArticleEntities carries the named entity extraction from analyzer Pass 2.
type ArticleEntities struct {
	Facilities []string `json:"facilities"`
	People     []string `json:"people"`
	Locations  []string `json:"locations"`
	Phenomena  []string `json:"phenomena"`
}

// AnalyzedArticle is published by the analyzer to TopicArticlesAnalyzed.
// Content is intentionally dropped — it's large and not needed downstream.
type AnalyzedArticle struct {
	Envelope
	URL           string          `json:"url"`
	Title         string          `json:"title"`
	Domain        string          `json:"domain"`
	ArticleType   string          `json:"article_type"`   // Pass1: discovery|review|tutorial|opinion|news
	Summary       string          `json:"summary"`
	Keywords      []string        `json:"keywords"`
	Concepts      []string        `json:"concepts"`
	RelatedTopics []string        `json:"related_topics"` // Pass3 related topics, separate from keywords
	Entities      ArticleEntities `json:"entities"`       // Full Pass2 entity extraction
	Insights      string          `json:"insights"`
	Embedding     []float32       `json:"embedding,omitempty"` // semantic embedding from Ollama /api/embed
}

// WorkCandidate represents a single discovered book or paper from any search source.
type WorkCandidate struct {
	ReferenceID  string    `json:"reference_id"`  // source:item-id, e.g. "openlibrary:/works/OL123W"
	SearchSource string    `json:"search_source"` // "openlibrary" | "arxiv" | etc.
	WorkType     string    `json:"work_type"`     // "book" | "paper"
	Title        string    `json:"title"`
	Authors      []string  `json:"authors"`
	ISBN         string    `json:"isbn"`
	ISBN13       string    `json:"isbn13"`
	ISSN         string    `json:"issn"`
	DOI          string    `json:"doi"`
	ArXivID      string    `json:"arxiv_id"`
	PublishYear  int       `json:"publish_year"`
	Publisher    string    `json:"publisher"`
	CoverURL     string    `json:"cover_url"`
	Relevance    float64   `json:"relevance"`
	Embedding    []float32 `json:"embedding,omitempty"` // semantic embedding from Ollama /api/embed
}

// WorkCandidates is published by any search primitive to TopicWorksCandidates.
type WorkCandidates struct {
	Envelope
	ArticleTitle string          `json:"article_title"`
	ArticleURL   string          `json:"article_url"`
	Works        []WorkCandidate `json:"works"`
}

// CheckedWorks is published by the koha-check primitive to TopicWorksChecked.
type CheckedWorks struct {
	Envelope
	ArticleTitle string          `json:"article_title"`
	ArticleURL   string          `json:"article_url"`
	NewWorks     []WorkCandidate `json:"new_works"`   // not in Koha (or non-book type)
	OwnedWorks   []OwnedWork     `json:"owned_works"` // found in Koha catalog
}

// OwnedWork is a book confirmed present in the Koha library catalog.
type OwnedWork struct {
	Title  string `json:"title"`
	Author string `json:"author"` // primary author from Koha record
	KohaID string `json:"koha_id"`
}

// ArticleComplete is published by the notifier to TopicArticlesComplete.
// Source primitives subscribe to this to mark articles as done in their state DB.
type ArticleComplete struct {
	Envelope
	CompletedAt time.Time `json:"completed_at"`
}

// BriefQuery is published by Journal to TopicQueryBrief.
// cmd/brief subscribes and returns a BriefResponse to msg.ResponseTopic.
type BriefQuery struct {
	SessionID            string             `json:"session_id"`
	ManifoldProfile      map[string]float32 `json:"manifold_profile"`
	TrendEmbeddings      [][]float32        `json:"trend_embeddings,omitempty"`
	UnexpectedEmbeddings [][]float32        `json:"unexpected_embeddings,omitempty"`
	SoulSpeed            float32            `json:"soul_speed"`
	TopK                 int                `json:"top_k"`
	ResponseTopic        string             `json:"response_topic"`
}

// BriefArticle is a single article result in a BriefResponse.
type BriefArticle struct {
	ArticleID string  `json:"article_id"`
	URL       string  `json:"url"`
	Title     string  `json:"title"`
	Score     float32 `json:"score"`
}

// BriefWork is a single work (book or paper) result in a BriefResponse.
type BriefWork struct {
	WorkID      int     `json:"work_id"`
	WorkType    string  `json:"work_type"` // "book" or "paper"
	Title       string  `json:"title"`
	Authors     string  `json:"authors,omitempty"`
	DOI         string  `json:"doi,omitempty"`
	ArXivID     string  `json:"arxiv_id,omitempty"`
	ISBN13      string  `json:"isbn13,omitempty"`
	PublishYear int     `json:"publish_year,omitempty"`
	Score       float32 `json:"score"`
}

// BriefResponse is published by cmd/brief to msg.ResponseTopic (Journal-facing).
type BriefResponse struct {
	SessionID string         `json:"session_id"`
	Articles  []BriefArticle `json:"articles"`
	Works     []BriefWork    `json:"works"`
}

// ConsolidatorDigest is published by cmd/consolidator to TopicConsolidatorDigest.
// cmd/notifier subscribes and delivers via ntfy.
type ConsolidatorDigest struct {
	SessionID    string    `json:"session_id"`
	WorkID       int       `json:"work_id"`       // 0 if article-only fallback
	WorkType     string    `json:"work_type"`
	Title        string    `json:"title"`
	Authors      string    `json:"authors"`
	DOI          string    `json:"doi"`
	ArXivID      string    `json:"arxiv_id"`
	ISBN13       string    `json:"isbn13"`
	PublishYear  int       `json:"publish_year"`
	ArticleID    string    `json:"article_id"`
	ArticleURL   string    `json:"article_url"`
	ArticleTitle string    `json:"article_title"`
	Score        float32   `json:"score"`
	SurfacedAt   time.Time `json:"surfaced_at"`
}

// BriefResult is published by cmd/brief to TopicBriefResult (Minerva-internal).
// cmd/store subscribes to persist the full session — articles and works ranked by score.
type BriefResult struct {
	SessionID string         `json:"session_id"`
	Articles  []BriefArticle `json:"articles"`
	Works     []BriefWork    `json:"works"`
	QueriedAt time.Time      `json:"queried_at"`
}
