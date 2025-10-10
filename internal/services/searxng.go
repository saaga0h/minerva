package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saaga/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type SearXNG struct {
	config config.SearXNGConfig
	client *http.Client
	logger *logrus.Logger
}

type SearXNGResult struct {
	Title   string  `json:"title"`
	Content string  `json:"content"`
	URL     string  `json:"url"`
	Score   float64 `json:"score"`
}

type SearXNGResponse struct {
	Results []SearXNGResult `json:"results"`
}

type RelatedReading struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Type      string  `json:"type"` // "article", "blog", "discussion", "paper"
	Relevance float64 `json:"relevance"`
}

func NewSearXNG(cfg config.SearXNGConfig) *SearXNG {
	return &SearXNG{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// SearchRelatedContent searches for related articles, blogs, and discussions
func (s *SearXNG) SearchRelatedContent(keywords []string, insights string) ([]RelatedReading, error) {
	s.logger.WithFields(logrus.Fields{
		"keywords": keywords,
		"insights": insights[:min(100, len(insights))],
	}).Debug("Searching for related content")

	query := s.buildSearchQuery(keywords, insights)

	results, err := s.search(query, "general")
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	readings := s.parseResults(results)

	s.logger.WithField("count", len(readings)).Info("Found related content")

	return readings, nil
}

// buildSearchQuery creates an optimized search query
func (s *SearXNG) buildSearchQuery(keywords []string, insights string) string {
	// Take first few words from insights for context
	insightWords := strings.Fields(insights)
	if len(insightWords) > 10 {
		insightWords = insightWords[:10]
	}

	// Combine keywords and insights
	var queryParts []string
	queryParts = append(queryParts, keywords...)
	queryParts = append(queryParts, insightWords...)

	// Remove duplicates and empty strings
	seen := make(map[string]bool)
	var uniqueParts []string
	for _, part := range queryParts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" && !seen[part] && len(part) > 2 {
			seen[part] = true
			uniqueParts = append(uniqueParts, part)
		}
	}

	// Limit query length
	if len(uniqueParts) > 8 {
		uniqueParts = uniqueParts[:8]
	}

	return strings.Join(uniqueParts, " ")
}

// search performs a search request to SearXNG
func (s *SearXNG) search(query, category string) ([]SearXNGResult, error) {
	baseURL, err := url.Parse(s.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	searchURL := *baseURL
	searchURL.Path = "/search"

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("categories", category)
	searchURL.RawQuery = params.Encode()

	req, err := http.NewRequest("GET", searchURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0")
	req.Header.Set("Accept", "application/json")

	s.logger.WithField("url", searchURL.String()).Debug("Performing SearXNG search")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var response SearXNGResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse search response: %w", err)
	}

	return response.Results, nil
}

// parseResults converts SearXNG results into related reading
func (s *SearXNG) parseResults(results []SearXNGResult) []RelatedReading {
	var readings []RelatedReading

	for _, result := range results {
		// Determine type based on URL
		contentType := s.determineType(result.URL)

		reading := RelatedReading{
			Title:     result.Title,
			URL:       result.URL,
			Type:      contentType,
			Relevance: result.Score,
		}

		readings = append(readings, reading)
	}

	// Sort by relevance (highest first)
	for i := 0; i < len(readings)-1; i++ {
		for j := i + 1; j < len(readings); j++ {
			if readings[j].Relevance > readings[i].Relevance {
				readings[i], readings[j] = readings[j], readings[i]
			}
		}
	}

	// Limit results
	if len(readings) > 10 {
		readings = readings[:10]
	}

	return readings
}

// determineType tries to identify the content type from URL
func (s *SearXNG) determineType(url string) string {
	url = strings.ToLower(url)

	if strings.Contains(url, "reddit.com") || strings.Contains(url, "stackoverflow.com") {
		return "discussion"
	}
	if strings.Contains(url, "blog") || strings.Contains(url, "medium.com") {
		return "blog"
	}
	if strings.Contains(url, "arxiv.org") || strings.Contains(url, ".pdf") {
		return "paper"
	}

	return "article"
}

// SetLogger sets the logger instance
func (s *SearXNG) SetLogger(logger *logrus.Logger) {
	s.logger = logger
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
