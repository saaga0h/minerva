package services

// PaperRecommendation represents a scientific paper found via arXiv or Semantic Scholar.
type PaperRecommendation struct {
	Title       string
	Authors     string  // comma-joined, first 3 authors
	ArXivID     string  // bare arXiv ID, e.g. "2301.00001" (empty if not from arXiv)
	PaperID     string  // source-native ID (arXiv ID, S2 paperId, or openalex:W...)
	DOI         string  // bare DOI, e.g. "10.48550/arxiv.2301.00001"
	PublishYear int
	Abstract    string
	URL         string
	Relevance   float64
}
