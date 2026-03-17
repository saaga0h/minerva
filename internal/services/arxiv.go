package services

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/saaga0h/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

// arXiv Atom feed structs
type arXivFeed struct {
	XMLName xml.Name      `xml:"feed"`
	Entries []arXivEntry  `xml:"entry"`
}

type arXivEntry struct {
	ID        string        `xml:"id"`
	Title     string        `xml:"title"`
	Published string        `xml:"published"`
	Summary   string        `xml:"summary"`
	Authors   []arXivAuthor `xml:"author"`
}

type arXivAuthor struct {
	Name string `xml:"name"`
}

var arXivVersionSuffix = regexp.MustCompile(`v\d+$`)

// ArXiv searches the arXiv API for papers relevant to given keywords.
type ArXiv struct {
	config config.ArXivConfig
	client *http.Client
	logger *logrus.Logger
}

func NewArXiv(cfg config.ArXivConfig) *ArXiv {
	return &ArXiv{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

func (a *ArXiv) SetLogger(logger *logrus.Logger) {
	a.logger = logger
}

// SearchPapers queries arXiv for papers matching the given keywords.
func (a *ArXiv) SearchPapers(keywords []string, insights string) ([]PaperRecommendation, error) {
	concepts := selectSearchConcepts(keywords, 4)
	if len(concepts) == 0 {
		a.logger.Warn("No suitable search concepts for arXiv query")
		return []PaperRecommendation{}, nil
	}

	// Try AND first, fall back to OR
	query := buildArXivQuery(concepts, "AND")
	results, err := a.executeSearch(query, 10)
	if err == nil && len(results) >= 3 {
		return results, nil
	}

	a.logger.Debug("arXiv AND query returned few results, trying OR")
	query = buildArXivQuery(concepts, "OR")
	results, err = a.executeSearch(query, 10)
	if err != nil {
		return []PaperRecommendation{}, err
	}
	return results, nil
}

func buildArXivQuery(concepts []string, operator string) string {
	var terms []string
	for _, c := range concepts {
		terms = append(terms, fmt.Sprintf("all:%s", url.QueryEscape(c)))
	}
	sep := "+"
	if operator == "OR" {
		sep = "+OR+"
	} else {
		sep = "+AND+"
	}
	return strings.Join(terms, sep)
}

func (a *ArXiv) executeSearch(query string, maxResults int) ([]PaperRecommendation, error) {
	searchURL := fmt.Sprintf(
		"https://export.arxiv.org/api/query?search_query=%s&max_results=%d&sortBy=relevance",
		query, maxResults,
	)

	a.logger.WithField("url", searchURL).Debug("Querying arXiv API")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create arXiv request: %w", err)
	}
	req.Header.Set("User-Agent", "Minerva/1.0 (Knowledge Curation System)")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arXiv request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("arXiv unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read arXiv response: %w", err)
	}

	var feed arXivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse arXiv Atom feed: %w", err)
	}

	results := make([]PaperRecommendation, 0, len(feed.Entries))
	for i, entry := range feed.Entries {
		arxivID := extractArXivID(entry.ID)
		if arxivID == "" {
			continue
		}

		authors := joinAuthors(entry.Authors, 3)
		year := parseYear(entry.Published)
		relevance := 1.0 - float64(i)*0.08

		results = append(results, PaperRecommendation{
			Title:       strings.TrimSpace(entry.Title),
			Authors:     authors,
			ArXivID:     arxivID,
			PaperID:     arxivID,
			PublishYear: year,
			Abstract:    strings.TrimSpace(entry.Summary),
			URL:         "https://arxiv.org/abs/" + arxivID,
			Relevance:   relevance,
		})
	}

	a.logger.WithFields(logrus.Fields{
		"query":   query,
		"results": len(results),
	}).Info("Found papers from arXiv")

	return results, nil
}

// extractArXivID strips the base URL and version suffix from an arXiv entry ID.
// Input: "http://arxiv.org/abs/2301.00001v2" → "2301.00001"
func extractArXivID(entryID string) string {
	entryID = strings.TrimSpace(entryID)
	// Strip base URL
	for _, prefix := range []string{
		"https://arxiv.org/abs/",
		"http://arxiv.org/abs/",
	} {
		if strings.HasPrefix(entryID, prefix) {
			entryID = strings.TrimPrefix(entryID, prefix)
			break
		}
	}
	// Strip version suffix (v1, v2, ...)
	entryID = arXivVersionSuffix.ReplaceAllString(entryID, "")
	return strings.TrimSpace(entryID)
}

func joinAuthors(authors []arXivAuthor, max int) string {
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

func parseYear(published string) int {
	if len(published) >= 4 {
		if y, err := strconv.Atoi(published[:4]); err == nil {
			return y
		}
	}
	return 0
}

// selectSearchConcepts is a shared helper used by both arXiv and Semantic Scholar services.
// It filters keywords down to the most specific, non-generic terms.
func selectSearchConcepts(keywords []string, max int) []string {
	generic := map[string]bool{
		"physics": true, "science": true, "biology": true,
		"chemistry": true, "mathematics": true, "astronomy": true,
		"research": true, "study": true, "analysis": true, "theory": true,
	}

	var filtered []string
	for _, kw := range keywords {
		lower := strings.ToLower(strings.TrimSpace(kw))
		if len(lower) < 4 {
			continue
		}
		if generic[lower] {
			continue
		}
		// Skip likely single-word proper names
		words := strings.Fields(kw)
		if len(words) == 1 && kw[0] >= 'A' && kw[0] <= 'Z' {
			continue
		}
		filtered = append(filtered, kw)
	}

	if len(filtered) > max {
		filtered = filtered[:max]
	}
	return filtered
}
