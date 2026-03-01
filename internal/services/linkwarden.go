package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/saaga0h/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

// Linkwarden fetches bookmarks from a Linkwarden instance via its REST API.
type Linkwarden struct {
	config config.LinkwardenConfig
	client *http.Client
	logger *logrus.Logger
}

// LinkwardenItem represents a single bookmark returned by the Linkwarden API.
type LinkwardenItem struct {
	ID    int    // Linkwarden link ID — used as pagination cursor
	URL   string
	Title string // mapped from "name" field in JSON
}

// linkwardenLink mirrors the Linkwarden /api/v1/links API link object.
type linkwardenLink struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// linkwardenResponse mirrors the Linkwarden API response envelope.
type linkwardenResponse struct {
	Response []linkwardenLink `json:"response"`
}

// NewLinkwarden creates a new Linkwarden service.
func NewLinkwarden(cfg config.LinkwardenConfig) *Linkwarden {
	return &Linkwarden{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// SetLogger sets the logger instance.
func (l *Linkwarden) SetLogger(logger *logrus.Logger) {
	l.logger = logger
}

// GetAllLinks fetches all bookmarks from Linkwarden using cursor-based pagination.
func (l *Linkwarden) GetAllLinks() ([]LinkwardenItem, error) {
	l.logger.WithField("base_url", l.config.BaseURL).Debug("Fetching all links from Linkwarden")

	var all []LinkwardenItem
	cursor := 0

	for {
		url := fmt.Sprintf("%s/api/v1/links?cursor=%d&limit=25", l.config.BaseURL, cursor)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+l.config.APIKey)

		resp, err := l.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		var response linkwardenResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		if len(response.Response) == 0 {
			break
		}

		for _, link := range response.Response {
			title := link.Name
			if title == "" {
				title = link.URL
			}
			all = append(all, LinkwardenItem{
				ID:    link.ID,
				URL:   link.URL,
				Title: title,
			})
		}

		// Next cursor is the ID of the last item in this page
		cursor = response.Response[len(response.Response)-1].ID
	}

	l.logger.WithField("count", len(all)).Info("Fetched all links from Linkwarden")
	return all, nil
}
