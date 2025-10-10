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

type OpenLibrary struct {
	config config.OpenLibraryConfig
	client *http.Client
	logger *logrus.Logger
}

type OpenLibraryResponse struct {
	NumFound int                  `json:"numFound"`
	Docs     []OpenLibraryBookDoc `json:"docs"`
}

type OpenLibraryBookDoc struct {
	Key              string   `json:"key"` // "/works/OL45804W"
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	ISBN             []string `json:"isbn"`
	Publisher        []string `json:"publisher"`
	CoverI           int      `json:"cover_i"` // Cover ID
	EditionCount     int      `json:"edition_count"`
}

type BookRecommendation struct {
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

func NewOpenLibrary(cfg config.OpenLibraryConfig) *OpenLibrary {
	return &OpenLibrary{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

func (ol *OpenLibrary) SearchBooks(keywords []string, insights string) ([]BookRecommendation, error) {
	ol.logger.WithFields(logrus.Fields{
		"keywords": keywords,
		"insights": insights[:min(100, len(insights))],
	}).Debug("Searching OpenLibrary for books")

	// Limit keywords and prepare for search
	searchTerms := keywords
	if len(searchTerms) > 5 {
		searchTerms = searchTerms[:5]
	}

	// Build query with quoted multi-word terms
	query := ol.buildSmartQuery(searchTerms)

	searchURL := fmt.Sprintf("https://openlibrary.org/search.json?q=%s&limit=20",
		url.QueryEscape(query))

	ol.logger.WithFields(logrus.Fields{
		"keywords_count": len(keywords),
		"used_keywords":  searchTerms,
		"query":          query,
	}).Debug("Building OpenLibrary search query")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Minerva/1.0 (Knowledge Curation System)")
	req.Header.Set("Accept", "application/json")

	ol.logger.WithField("url", searchURL).Debug("Querying OpenLibrary API")

	resp, err := ol.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
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

	var response OpenLibraryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	recommendations := ol.parseBooks(response.Docs, searchTerms)

	ol.logger.WithFields(logrus.Fields{
		"total_found": response.NumFound,
		"returned":    len(recommendations),
	}).Info("Found book recommendations from OpenLibrary")

	return recommendations, nil
}

// buildSmartQuery creates a query with quoted multi-word terms
func (ol *OpenLibrary) buildSmartQuery(keywords []string) string {
	var terms []string
	for _, kw := range keywords {
		// Quote multi-word terms to keep them together
		if strings.Contains(kw, " ") {
			terms = append(terms, fmt.Sprintf(`"%s"`, kw))
		} else {
			terms = append(terms, kw)
		}
	}
	return strings.Join(terms, " OR ")
}

// parseBooks now takes keywords for filtering
func (ol *OpenLibrary) parseBooks(docs []OpenLibraryBookDoc, searchKeywords []string) []BookRecommendation {
	var recommendations []BookRecommendation

	for i, doc := range docs {
		// Position-based relevance: earlier = more relevant
		relevance := 1.0 - (float64(i) * 0.05)

		// Stop after top 10 results
		if i >= 10 {
			break
		}

		// Get primary author
		author := "Unknown Author"
		if len(doc.AuthorName) > 0 {
			author = doc.AuthorName[0]
		}

		// Get first publisher
		publisher := ""
		if len(doc.Publisher) > 0 {
			publisher = doc.Publisher[0]
		}

		// Find ISBN-13 and ISBN-10 (may be empty)
		isbn13, isbn10 := "", ""
		if len(doc.ISBN) > 0 {
			isbn13, isbn10 = ol.extractISBNs(doc.ISBN)
		}

		// Log if no ISBN found
		if isbn13 == "" && isbn10 == "" {
			ol.logger.WithFields(logrus.Fields{
				"title":  doc.Title,
				"author": author,
			}).Debug("Book has no ISBN, will match by title/author")
		}

		// Build cover URL if available
		coverURL := ""
		if doc.CoverI > 0 {
			coverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", doc.CoverI)
		}

		// Boost relevance if published more recently or has more editions
		if doc.EditionCount > 10 {
			relevance += 0.2
		}
		if doc.FirstPublishYear > 1990 {
			relevance += 0.1
		}

		rec := BookRecommendation{
			Title:          doc.Title,
			Author:         author,
			ISBN:           isbn10,
			ISBN13:         isbn13,
			PublishYear:    doc.FirstPublishYear,
			Publisher:      publisher,
			CoverURL:       coverURL,
			OpenLibraryKey: doc.Key,
			Relevance:      relevance,
		}

		recommendations = append(recommendations, rec)
	}

	return recommendations
}

// extractISBNs separates ISBN-13 and ISBN-10 from the list
func (ol *OpenLibrary) extractISBNs(isbns []string) (isbn13, isbn10 string) {
	for _, isbn := range isbns {
		// Remove hyphens and spaces
		cleaned := strings.ReplaceAll(strings.ReplaceAll(isbn, "-", ""), " ", "")

		if len(cleaned) == 13 && isbn13 == "" {
			isbn13 = cleaned
		} else if len(cleaned) == 10 && isbn10 == "" {
			isbn10 = cleaned
		}

		// Stop if we have both
		if isbn13 != "" && isbn10 != "" {
			break
		}
	}
	return isbn13, isbn10
}

// calculateRelevance provides a simple relevance score
func (ol *OpenLibrary) calculateRelevance(doc OpenLibraryBookDoc) float64 {
	relevance := 1.0

	// More editions = more popular/relevant
	if doc.EditionCount > 10 {
		relevance += 0.3
	} else if doc.EditionCount > 5 {
		relevance += 0.2
	} else if doc.EditionCount > 1 {
		relevance += 0.1
	}

	// More recent books get a slight boost
	currentYear := time.Now().Year()
	if doc.FirstPublishYear > 0 {
		age := currentYear - doc.FirstPublishYear
		if age < 5 {
			relevance += 0.2
		} else if age < 10 {
			relevance += 0.1
		}
	}

	return relevance
}

// SetLogger sets the logger instance
func (ol *OpenLibrary) SetLogger(logger *logrus.Logger) {
	ol.logger = logger
}
