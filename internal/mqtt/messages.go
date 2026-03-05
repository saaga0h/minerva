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
}

// WorkCandidate represents a single discovered book or paper from any search source.
type WorkCandidate struct {
	ReferenceID  string   `json:"reference_id"`  // source:item-id, e.g. "openlibrary:/works/OL123W"
	SearchSource string   `json:"search_source"` // "openlibrary" | "arxiv" | etc.
	WorkType     string   `json:"work_type"`     // "book" | "paper"
	Title        string   `json:"title"`
	Authors      []string `json:"authors"`
	ISBN         string   `json:"isbn"`
	ISBN13       string   `json:"isbn13"`
	ISSN         string   `json:"issn"`
	DOI          string   `json:"doi"`
	ArXivID      string   `json:"arxiv_id"`
	PublishYear  int      `json:"publish_year"`
	Publisher    string   `json:"publisher"`
	CoverURL     string   `json:"cover_url"`
	Relevance    float64  `json:"relevance"`
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
