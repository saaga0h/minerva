package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// MinifluxConfig holds connection settings for the Miniflux API.
type MinifluxConfig struct {
	BaseURL string // e.g. "https://miniflux.example.com"
	APIKey  string // Miniflux API key
	Timeout int    // seconds
}

// Miniflux fetches starred/saved entries from a Miniflux instance via its REST API.
type Miniflux struct {
	config MinifluxConfig
	client *http.Client
	logger *logrus.Logger
}

// MinifuxItem represents a single entry returned by the Miniflux API.
type MinifuxItem struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	PublishedAt string `json:"published_at"`
	Content     string `json:"content"` // RSS item body (HTML)
}

// minifluxEntry mirrors the Miniflux /v1/entries API response.
type minifluxEntry struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	PublishedAt string `json:"published_at"`
	Content     string `json:"content"` // RSS item body (HTML)
	Starred     bool   `json:"starred"`
	Status      string `json:"status"`
}

type minifluxEntriesResponse struct {
	Total   int             `json:"total"`
	Entries []minifluxEntry `json:"entries"`
}

// NewMiniflux creates a new Miniflux service.
func NewMiniflux(cfg MinifluxConfig) *Miniflux {
	return &Miniflux{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// SetLogger sets the logger instance.
func (m *Miniflux) SetLogger(logger *logrus.Logger) {
	m.logger = logger
}

// GetStarredEntries fetches all starred entries from Miniflux.
func (m *Miniflux) GetStarredEntries() ([]MinifuxItem, error) {
	m.logger.WithField("base_url", m.config.BaseURL).Debug("Fetching starred entries from Miniflux")

	url := fmt.Sprintf("%s/v1/entries?starred=true&status=read&status=unread&limit=100", m.config.BaseURL)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Auth-Token", m.config.APIKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var response minifluxEntriesResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	items := make([]MinifuxItem, 0, len(response.Entries))
	for _, entry := range response.Entries {
		items = append(items, MinifuxItem{
			ID:          entry.ID,
			URL:         entry.URL,
			Title:       entry.Title,
			PublishedAt: entry.PublishedAt,
			Content:     entry.Content,
		})
	}

	m.logger.WithField("count", len(items)).Info("Fetched starred entries from Miniflux")
	return items, nil
}
