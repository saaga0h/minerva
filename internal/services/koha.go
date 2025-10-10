package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/saaga/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type Koha struct {
	config config.KohaConfig
	client *http.Client
	logger *logrus.Logger
}

type KohaRecord struct {
	BiblioID int    `json:"biblio_id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	// Add more fields as we discover them
}

func NewKoha(cfg config.KohaConfig) *Koha {
	return &Koha{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// CheckOwnership checks if you own a book by ISBN or title/author
func (k *Koha) CheckOwnership(isbn, title, author string) (bool, *KohaRecord, error) {
	k.logger.WithFields(logrus.Fields{
		"isbn":   isbn,
		"title":  title,
		"author": author,
	}).Debug("Checking book ownership in Koha")

	// Try ISBN first (most reliable)
	if isbn != "" {
		records, err := k.searchByField("isbn", isbn)
		if err == nil && len(records) > 0 {
			k.logger.WithField("title", records[0].Title).Info("Found book in catalog by ISBN")
			return true, &records[0], nil
		}
	}

	// Fallback to title search
	if title != "" {
		records, err := k.searchByTitle(title)
		if err == nil && len(records) > 0 {
			k.logger.WithField("title", records[0].Title).Info("Found book in catalog by title")
			return true, &records[0], nil
		}
	}

	k.logger.Debug("Book not found in catalog")
	return false, nil, nil
}

// SearchRelated finds books in your catalog related to keywords
func (k *Koha) SearchRelated(keywords []string) ([]KohaRecord, error) {
	k.logger.WithField("keywords", keywords).Debug("Searching Koha catalog for related books")

	var allRecords []KohaRecord

	// Search by each keyword
	for _, keyword := range keywords {
		records, err := k.searchByTitle(keyword)
		if err != nil {
			k.logger.WithError(err).WithField("keyword", keyword).Warn("Failed to search by keyword")
			continue
		}
		allRecords = append(allRecords, records...)
	}

	// Deduplicate by biblio_id
	seen := make(map[int]bool)
	var uniqueRecords []KohaRecord
	for _, record := range allRecords {
		if !seen[record.BiblioID] {
			seen[record.BiblioID] = true
			uniqueRecords = append(uniqueRecords, record)
		}
	}

	// Limit to 20 results
	if len(uniqueRecords) > 20 {
		uniqueRecords = uniqueRecords[:20]
	}

	k.logger.WithField("count", len(uniqueRecords)).Info("Found related books in catalog")
	return uniqueRecords, nil
}

// searchByField searches for a book by a specific field
func (k *Koha) searchByField(field, value string) ([]KohaRecord, error) {
	// Build JSON query: {"field": "value"}
	query := fmt.Sprintf(`{"%s":"%s"}`, field, value)

	return k.doSearch(query, 10)
}

// searchByTitle searches for books with title containing the keyword
func (k *Koha) searchByTitle(keyword string) ([]KohaRecord, error) {
	// Build JSON query: {"title": {"-like": "%keyword%"}}
	query := fmt.Sprintf(`{"title":{"-like":"%%%s%%"}}`, keyword)

	return k.doSearch(query, 20)
}

// doSearch executes the search query
func (k *Koha) doSearch(jsonQuery string, perPage int) ([]KohaRecord, error) {
	// Build URL with encoded query
	baseURL := fmt.Sprintf("%s/api/v1/biblios", k.config.BaseURL)

	params := url.Values{}
	params.Set("q", jsonQuery)
	params.Set("_per_page", fmt.Sprintf("%d", perPage))

	searchURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	k.logger.WithField("url", searchURL).Debug("Querying Koha API")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set Basic Auth
	req.SetBasicAuth(k.config.Username, k.config.Password)

	// Set required headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		k.logger.WithFields(logrus.Fields{
			"status": resp.StatusCode,
			"body":   string(body),
		}).Error("Koha API error")
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	// Parse response
	var records []KohaRecord
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	k.logger.WithField("count", len(records)).Debug("Koha search completed")
	return records, nil
}

// SetLogger sets the logger instance
func (k *Koha) SetLogger(logger *logrus.Logger) {
	k.logger = logger
}
