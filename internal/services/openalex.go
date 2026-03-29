package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saaga0h/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type openAlexResponse struct {
	Results []openAlexWork `json:"results"`
}

type openAlexWork struct {
	ID              string                       `json:"id"`
	DOI             string                       `json:"doi"`
	Title           string                       `json:"title"`
	PublicationYear int                          `json:"publication_year"`
	Type            string                       `json:"type"`
	IDs             openAlexIDs                  `json:"ids"`
	Authorships     []openAlexAuthorship         `json:"authorships"`
	AbstractIndex   map[string][]int             `json:"abstract_inverted_index"`
}

type openAlexIDs struct {
	DOI string `json:"doi"`
}

type openAlexAuthorship struct {
	Author openAlexAuthor `json:"author"`
}

type openAlexAuthor struct {
	DisplayName string `json:"display_name"`
}

// OpenAlex searches the OpenAlex API for papers relevant to given keywords.
type OpenAlex struct {
	config config.OpenAlexConfig
	client *http.Client
	logger *logrus.Logger
}

func NewOpenAlex(cfg config.OpenAlexConfig) *OpenAlex {
	return &OpenAlex{
		config: cfg,
		client: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
		logger: logrus.New(),
	}
}

func (o *OpenAlex) SetLogger(logger *logrus.Logger) {
	o.logger = logger
}

// SearchPapers queries OpenAlex for papers matching the given keywords.
func (o *OpenAlex) SearchPapers(keywords []string, insights string) ([]PaperRecommendation, error) {
	concepts := selectSearchConcepts(keywords, 4)
	if len(concepts) == 0 {
		return []PaperRecommendation{}, nil
	}

	query := strings.Join(concepts, " ")
	params := url.Values{}
	params.Set("search", query)
	params.Set("sort", "relevance_score:desc")
	params.Set("per_page", "10")
	params.Set("select", "id,doi,title,publication_year,ids,authorships,abstract_inverted_index,type")
	if o.config.MailTo != "" {
		params.Set("mailto", o.config.MailTo)
	}

	searchURL := "https://api.openalex.org/works?" + params.Encode()
	o.logger.WithField("url", searchURL).Debug("Querying OpenAlex API")

	var resp *http.Response
	backoff := 30 * time.Second
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest("GET", searchURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create OpenAlex request: %w", err)
		}
		userAgent := "Minerva/1.0 (Knowledge Curation System)"
		if o.config.MailTo != "" {
			userAgent += "; mailto:" + o.config.MailTo
		}
		req.Header.Set("User-Agent", userAgent)

		var doErr error
		resp, doErr = o.client.Do(req)
		if doErr != nil {
			return nil, fmt.Errorf("OpenAlex request failed: %w", doErr)
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		resp.Body.Close()
		o.logger.WithField("backoff", backoff).Warn("OpenAlex 429 — backing off")
		time.Sleep(backoff)
		backoff *= 2
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAlex unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read OpenAlex response: %w", err)
	}

	var result openAlexResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAlex response: %w", err)
	}

	papers := make([]PaperRecommendation, 0, len(result.Results))
	for i, w := range result.Results {
		if w.Title == "" {
			continue
		}

		abstract := reconstructAbstract(w.AbstractIndex)
		doi := strings.TrimPrefix(w.DOI, "https://doi.org/")
		arxivID := extractOpenAlexArXivID(w.IDs.DOI)

		authors := make([]string, 0, 3)
		for j, a := range w.Authorships {
			if j >= 3 {
				break
			}
			if a.Author.DisplayName != "" {
				authors = append(authors, a.Author.DisplayName)
			}
		}
		authorStr := strings.Join(authors, ", ")
		if len(w.Authorships) > 3 {
			authorStr += " et al."
		}

		openAlexID := extractOpenAlexID(w.ID)
		papers = append(papers, PaperRecommendation{
			Title:       strings.TrimSpace(w.Title),
			Authors:     authorStr,
			ArXivID:     arxivID,
			PaperID:     "openalex:" + openAlexID,
			PublishYear: w.PublicationYear,
			Abstract:    abstract,
			URL:         w.ID, // OpenAlex ID URL is a stable landing page
			Relevance:   1.0 - float64(i)*0.08,
			DOI:         doi,
		})
	}

	o.logger.WithFields(logrus.Fields{
		"query":   query,
		"results": len(papers),
	}).Info("Found papers from OpenAlex")

	return papers, nil
}

// reconstructAbstract rebuilds the abstract string from OpenAlex's inverted index format.
func reconstructAbstract(index map[string][]int) string {
	if len(index) == 0 {
		return ""
	}
	maxPos := 0
	for _, positions := range index {
		for _, p := range positions {
			if p > maxPos {
				maxPos = p
			}
		}
	}
	words := make([]string, maxPos+1)
	for word, positions := range index {
		for _, p := range positions {
			words[p] = word
		}
	}
	// Remove empty slots (shouldn't happen but be safe)
	var parts []string
	for _, w := range words {
		if w != "" {
			parts = append(parts, w)
		}
	}
	return strings.Join(parts, " ")
}

// extractOpenAlexArXivID extracts the arXiv ID from an OpenAlex DOI URL when present.
// e.g. "https://doi.org/10.48550/arxiv.2301.00001" → "2301.00001"
func extractOpenAlexArXivID(doiURL string) string {
	const arxivPrefix = "https://doi.org/10.48550/arxiv."
	if strings.HasPrefix(strings.ToLower(doiURL), strings.ToLower(arxivPrefix)) {
		return doiURL[len(arxivPrefix):]
	}
	return ""
}

// extractOpenAlexID returns the bare work ID from an OpenAlex URI.
// e.g. "https://openalex.org/W2626778328" → "W2626778328"
func extractOpenAlexID(uri string) string {
	parts := strings.Split(strings.TrimRight(uri, "/"), "/")
	if len(parts) == 0 {
		return uri
	}
	return parts[len(parts)-1]
}

