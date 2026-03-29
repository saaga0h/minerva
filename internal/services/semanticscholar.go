package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/saaga0h/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type s2SearchResponse struct {
	Data []s2Paper `json:"data"`
}

type s2Paper struct {
	PaperID     string                     `json:"paperId"`
	Title       string                     `json:"title"`
	Authors     []s2Author                 `json:"authors"`
	Year        int                        `json:"year"`
	Abstract    string                     `json:"abstract"`
	URL         string                     `json:"url"`
	ExternalIDs map[string]json.RawMessage `json:"externalIds"`
}

type s2Author struct {
	Name string `json:"name"`
}

// SemanticScholar searches the Semantic Scholar API for papers relevant to given keywords.
type SemanticScholar struct {
	config    config.SemanticScholarConfig
	client    *http.Client
	logger    *logrus.Logger
	mu        sync.Mutex
	lastCall  time.Time
}

func NewSemanticScholar(cfg config.SemanticScholarConfig) *SemanticScholar {
	return &SemanticScholar{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// throttle enforces a minimum gap between requests: 1s without API key, 100ms with one.
func (s *SemanticScholar) throttle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	gap := 5 * time.Second // conservative without API key
	if s.config.APIKey != "" {
		gap = 200 * time.Millisecond
	}
	if since := time.Since(s.lastCall); since < gap {
		time.Sleep(gap - since)
	}
	s.lastCall = time.Now()
}

func (s *SemanticScholar) SetLogger(logger *logrus.Logger) {
	s.logger = logger
}

// SearchPapers queries Semantic Scholar for papers matching the given keywords.
func (s *SemanticScholar) SearchPapers(keywords []string, insights string) ([]PaperRecommendation, error) {
	concepts := selectSearchConcepts(keywords, 4)
	if len(concepts) == 0 {
		s.logger.Warn("No suitable search concepts for Semantic Scholar query")
		return []PaperRecommendation{}, nil
	}

	query := strings.Join(concepts, " ")
	return s.executeSearch(query, 10)
}

func (s *SemanticScholar) doWithRetry(searchURL string, apiKey string) (*http.Response, error) {
	backoff := 30 * time.Second
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest("GET", searchURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create Semantic Scholar request: %w", err)
		}
		req.Header.Set("User-Agent", "Minerva/1.0 (Knowledge Curation System)")
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Semantic Scholar request failed: %w", err)
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		resp.Body.Close()
		s.logger.WithField("backoff", backoff).Warn("Semantic Scholar 429 — backing off")
		time.Sleep(backoff)
		backoff *= 2
	}
	return nil, fmt.Errorf("Semantic Scholar rate limited after retries")
}

func (s *SemanticScholar) executeSearch(query string, limit int) ([]PaperRecommendation, error) {
	searchURL := fmt.Sprintf(
		"https://api.semanticscholar.org/graph/v1/paper/search?query=%s&fields=title,authors,year,externalIds,abstract,url&limit=%d",
		url.QueryEscape(query), limit,
	)

	s.throttle()
	s.logger.WithField("url", searchURL).Debug("Querying Semantic Scholar API")

	resp, err := s.doWithRetry(searchURL, s.config.APIKey)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Semantic Scholar unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Semantic Scholar response: %w", err)
	}

	var response s2SearchResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Semantic Scholar response: %w", err)
	}

	results := make([]PaperRecommendation, 0, len(response.Data))
	for i, paper := range response.Data {
		if paper.Title == "" {
			continue
		}

		authors := joinS2Authors(paper.Authors, 3)
		relevance := 1.0 - float64(i)*0.08

		// Prefer arXiv ID if available, fall back to Semantic Scholar paperId.
		// ExternalIDs values are RawMessage — ArXiv ID is a JSON string, CorpusId is a number.
		arxivID := rawStringValue(paper.ExternalIDs["ArXiv"])
		paperID := paper.PaperID
		if arxivID != "" {
			paperID = arxivID
		}

		paperURL := paper.URL
		if paperURL == "" && arxivID != "" {
			paperURL = "https://arxiv.org/abs/" + arxivID
		}

		results = append(results, PaperRecommendation{
			Title:       strings.TrimSpace(paper.Title),
			Authors:     authors,
			ArXivID:     arxivID,
			PaperID:     paperID,
			PublishYear: paper.Year,
			Abstract:    strings.TrimSpace(paper.Abstract),
			URL:         paperURL,
			Relevance:   relevance,
		})
	}

	s.logger.WithFields(logrus.Fields{
		"query":   query,
		"results": len(results),
	}).Info("Found papers from Semantic Scholar")

	return results, nil
}

// rawStringValue extracts a plain string from a JSON-quoted RawMessage.
// Returns "" for null, numbers, or missing keys.
func rawStringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func joinS2Authors(authors []s2Author, max int) string {
	var names []string
	for i, a := range authors {
		if i >= max {
			break
		}
		names = append(names, a.Name)
	}
	result := strings.Join(names, ", ")
	if len(authors) > max {
		result += " et al."
	}
	return result
}
