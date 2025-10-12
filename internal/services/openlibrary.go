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
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	ISBN             []string `json:"isbn"`
	Publisher        []string `json:"publisher"`
	Subject          []string `json:"subject"`
	CoverI           int      `json:"cover_i"`
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

type bookQualityFilter struct {
	logger *logrus.Logger
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

func newBookQualityFilter(logger *logrus.Logger) *bookQualityFilter {
	return &bookQualityFilter{logger: logger}
}

func (ol *OpenLibrary) SearchBooks(keywords []string, insights string) ([]BookRecommendation, error) {
	ol.logger.WithFields(logrus.Fields{
		"keywords": keywords,
	}).Debug("Searching OpenLibrary for books")

	// Select best concepts for search
	searchConcepts := ol.selectBestConcepts(keywords, 5)

	if len(searchConcepts) == 0 {
		ol.logger.Warn("No suitable search concepts after filtering")
		return []BookRecommendation{}, nil
	}

	ol.logger.WithField("selected_concepts", searchConcepts).Debug("Selected concepts for search")

	// Try subject-based AND search first (most precise)
	query := ol.buildSubjectQuery(searchConcepts, "AND")
	recommendations, err := ol.executeSearch(query, 20) // Request more to account for filtering

	if err == nil && len(recommendations) >= 5 {
		ol.logger.WithField("method", "subject_AND").Info("Found sufficient results with subject AND")
		return recommendations, nil
	}

	// Fallback to subject OR (broader)
	ol.logger.Debug("Subject AND returned few results, trying OR")
	query = ol.buildSubjectQuery(searchConcepts, "OR")
	recommendations, err = ol.executeSearch(query, 20)

	if err == nil && len(recommendations) >= 3 {
		ol.logger.WithField("method", "subject_OR").Info("Found sufficient results with subject OR")
		return recommendations, nil
	}

	// Final fallback to general search
	ol.logger.Debug("Subject search insufficient, trying general search")
	query = ol.buildGeneralQuery(searchConcepts, "AND")
	recommendations, err = ol.executeSearch(query, 20)

	if err == nil {
		ol.logger.WithField("method", "general").Info("Using general search results")
		return recommendations, nil
	}

	return []BookRecommendation{}, err
}

// buildSubjectQuery creates a query using subject: field
func (ol *OpenLibrary) buildSubjectQuery(concepts []string, operator string) string {
	var terms []string
	for _, concept := range concepts {
		terms = append(terms, fmt.Sprintf(`subject:"%s"`, concept))
	}
	return strings.Join(terms, " "+operator+" ")
}

// buildGeneralQuery creates a general query as fallback
func (ol *OpenLibrary) buildGeneralQuery(concepts []string, operator string) string {
	var terms []string
	for _, concept := range concepts {
		if strings.Contains(concept, " ") {
			terms = append(terms, fmt.Sprintf(`"%s"`, concept))
		} else {
			terms = append(terms, concept)
		}
	}
	return strings.Join(terms, " "+operator+" ")
}

// selectBestConcepts picks the most specific concepts for searching
func (ol *OpenLibrary) selectBestConcepts(concepts []string, max int) []string {
	// Filter out overly generic terms
	generic := map[string]bool{
		"physics":     true,
		"science":     true,
		"biology":     true,
		"chemistry":   true,
		"mathematics": true,
		"astronomy":   true,
		"research":    true,
		"study":       true,
		"analysis":    true,
		"theory":      true,
	}

	var filtered []string
	for _, concept := range concepts {
		lower := strings.ToLower(strings.TrimSpace(concept))

		// Skip empty or very short terms
		if len(lower) < 4 {
			continue
		}

		// Skip generic terms
		if generic[lower] {
			ol.logger.WithField("skipped", concept).Debug("Skipping generic concept")
			continue
		}

		// Skip person names (heuristic: single word capitalized)
		words := strings.Fields(concept)
		if len(words) == 1 && concept[0] >= 'A' && concept[0] <= 'Z' {
			ol.logger.WithField("skipped", concept).Debug("Skipping potential person name")
			continue
		}

		filtered = append(filtered, concept)
	}

	// Limit to max concepts
	if len(filtered) > max {
		filtered = filtered[:max]
	}

	return filtered
}

// executeSearch performs the actual OpenLibrary API call
func (ol *OpenLibrary) executeSearch(query string, limit int) ([]BookRecommendation, error) {
	searchURL := fmt.Sprintf("https://openlibrary.org/search.json?q=%s&limit=%d",
		url.QueryEscape(query), limit)

	ol.logger.WithFields(logrus.Fields{
		"query": query,
		"url":   searchURL,
	}).Debug("Querying OpenLibrary API")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Minerva/1.0 (Knowledge Curation System)")
	req.Header.Set("Accept", "application/json")

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

	recommendations := ol.parseBooks(response.Docs)

	ol.logger.WithFields(logrus.Fields{
		"total_found":     response.NumFound,
		"returned":        len(response.Docs),
		"after_filtering": len(recommendations),
	}).Info("Found book recommendations from OpenLibrary")

	return recommendations, nil
}

// parseBooks converts OpenLibrary docs to BookRecommendations with quality filtering
func (ol *OpenLibrary) parseBooks(docs []OpenLibraryBookDoc) []BookRecommendation {
	var recommendations []BookRecommendation

	// Create quality filter
	filter := newBookQualityFilter(ol.logger)

	for i, doc := range docs {
		// Apply quality filter (only rejects obvious junk)
		if !filter.shouldInclude(doc) {
			continue
		}

		// Position-based relevance: earlier = more relevant
		relevance := 1.0 - (float64(i) * 0.05)

		// Stop after collecting 10 books
		if len(recommendations) >= 10 {
			break
		}

		author := "Unknown Author"
		if len(doc.AuthorName) > 0 {
			author = doc.AuthorName[0]
		}

		publisher := ""
		if len(doc.Publisher) > 0 {
			publisher = doc.Publisher[0]
		}

		isbn13, isbn10 := "", ""
		if len(doc.ISBN) > 0 {
			isbn13, isbn10 = ol.extractISBNs(doc.ISBN)
		}

		if isbn13 == "" && isbn10 == "" {
			ol.logger.WithFields(logrus.Fields{
				"title":  doc.Title,
				"author": author,
			}).Debug("Book has no ISBN, will match by title/author")
		}

		coverURL := ""
		if doc.CoverI > 0 {
			coverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", doc.CoverI)
		}

		// Boost relevance for quality indicators (not used for filtering)
		if filter.isQualityPublisher(doc.Publisher) {
			relevance += 0.3
			ol.logger.WithFields(logrus.Fields{
				"title":     doc.Title,
				"publisher": doc.Publisher[0],
			}).Debug("Quality publisher boost")
		}

		_, hasGoodSubjects := filter.hasGoodSubjects(doc.Subject)
		if hasGoodSubjects {
			relevance += 0.2
			ol.logger.WithField("title", doc.Title).Debug("Good subjects boost")
		}

		if doc.EditionCount > 10 {
			relevance += 0.1
		}

		// Older books for fundamentals
		if doc.FirstPublishYear > 1990 && doc.FirstPublishYear < 2020 {
			relevance += 0.05
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
		cleaned := strings.ReplaceAll(strings.ReplaceAll(isbn, "-", ""), " ", "")

		if len(cleaned) == 13 && isbn13 == "" {
			isbn13 = cleaned
		} else if len(cleaned) == 10 && isbn10 == "" {
			isbn10 = cleaned
		}

		if isbn13 != "" && isbn10 != "" {
			break
		}
	}
	return isbn13, isbn10
}

// SetLogger sets the logger instance
func (ol *OpenLibrary) SetLogger(logger *logrus.Logger) {
	ol.logger = logger
}

// Quality filter methods

// isQualityPublisher checks if publisher is known for substantial books
func (f *bookQualityFilter) isQualityPublisher(publishers []string) bool {
	academic := map[string]bool{
		// University Presses
		"MIT Press":                   true,
		"Cambridge University Press":  true,
		"Oxford University Press":     true,
		"Princeton University Press":  true,
		"Harvard University Press":    true,
		"Yale University Press":       true,
		"Stanford University Press":   true,
		"University of Chicago Press": true,
		"Columbia University Press":   true,

		// Major Academic Publishers
		"Springer":         true,
		"Springer-Verlag":  true,
		"Springer Nature":  true,
		"Wiley":            true,
		"Wiley-Blackwell":  true,
		"Elsevier":         true,
		"Academic Press":   true,
		"CRC Press":        true,
		"Taylor & Francis": true,
		"World Scientific": true,
		"IOP Publishing":   true,
		"AIP Publishing":   true,

		// CS/Math/Physics Specific
		"Morgan Kaufmann":               true,
		"Addison-Wesley":                true,
		"Pearson":                       true,
		"Dover Publications":            true,
		"American Mathematical Society": true,
		"Society for Industrial and Applied Mathematics": true,
		"Chapman and Hall": true,
		"North Holland":    true,

		// Technical (good quality)
		"O'Reilly Media":       true,
		"Manning Publications": true,
		"Pragmatic Bookshelf":  true,
		"No Starch Press":      true,
	}

	for _, pub := range publishers {
		// Normalize publisher name
		normalized := strings.TrimSpace(pub)
		if academic[normalized] {
			return true
		}

		// Also check if publisher contains known name (handles variations)
		for acadPub := range academic {
			if strings.Contains(normalized, acadPub) {
				return true
			}
		}
	}
	return false
}

// hasShallowTitle checks for obvious shallow content indicators
func (f *bookQualityFilter) hasShallowTitle(title string) bool {
	lower := strings.ToLower(title)

	// Fiction indicators (series, character names)
	fictionPatterns := []string{
		"star trek",
		"star wars",
		"harry potter",
		"game of thrones",
		"lord of the rings",
		"hobbit",
		"a novel",
		"a romance",
		"a thriller",
	}

	for _, pattern := range fictionPatterns {
		if strings.Contains(lower, pattern) {
			f.logger.WithFields(logrus.Fields{
				"title":   title,
				"pattern": pattern,
			}).Debug("Rejected: fiction title pattern")
			return true
		}
	}

	// Shallow/pop-sci patterns
	shallowPatterns := []string{
		"for dummies",
		"for idiots",
		"in 24 hours",
		"in 21 days",
		"crash course",
		"bible",
		"complete guide",
		"ultimate guide",
		"mastery",
		"no-code",
		"beginner's guide",
		"for the rest of us", // Catches "Rocket science for the rest of us"

		// Model-specific (AI domain)
		"chatgpt",
		"gpt-3",
		"gpt-4",
		"prompts",
		"prompt engineering",
		"stable diffusion",
		"midjourney",
		"dall-e",
		"llama 2",
		"llama 3",
	}

	for _, pattern := range shallowPatterns {
		if strings.Contains(lower, pattern) {
			f.logger.WithFields(logrus.Fields{
				"title":   title,
				"pattern": pattern,
			}).Debug("Rejected: shallow title pattern")
			return true
		}
	}

	return false
}

// hasGoodSubjects checks for quality subject classifications
func (f *bookQualityFilter) hasGoodSubjects(subjects []string) (bool, bool) {
	if len(subjects) == 0 {
		return true, false // No subjects - neutral
	}

	hasGood := false
	hasBad := false

	// Bad subjects (immediate rejection) - expanded fiction detection
	badSubjects := []string{
		"fiction",
		"science fiction",
		"fantasy",
		"romance",
		"thriller",
		"mystery",
		"novel",
		"popular works",
		"self-help",
		"business applications",
		"guidebooks",
		"handbooks, manuals",
		"juvenile",
		"juvenile literature",
		"children's literature",
		"young adult",
		"comic books",
		"graphic novels",
		"biography",
		"autobiography",
	}

	// Good subjects (boost score)
	goodSubjects := []string{
		"mathematical theory",
		"algorithms",
		"mathematical optimization",
		"computational complexity",
		"game theory",
		"statistical methods",
		"foundations",
		"philosophy",
		"textbooks",
		"research",
		"physics",
		"astronomy",
		"cosmology",
	}

	for _, subject := range subjects {
		lower := strings.ToLower(subject)

		// Check for bad subjects
		for _, bad := range badSubjects {
			if strings.Contains(lower, bad) {
				hasBad = true
				f.logger.WithFields(logrus.Fields{
					"subject": subject,
					"pattern": bad,
				}).Debug("Found bad subject")
				break
			}
		}

		// Check for good subjects
		for _, good := range goodSubjects {
			if strings.Contains(lower, good) {
				hasGood = true
				f.logger.WithField("subject", subject).Debug("Found good subject")
			}
		}
	}

	return !hasBad, hasGood
}

// shouldInclude determines if a book meets quality standards
func (f *bookQualityFilter) shouldInclude(doc OpenLibraryBookDoc) bool {
	// Immediate rejection: shallow title patterns
	if f.hasShallowTitle(doc.Title) {
		f.logger.WithField("title", doc.Title).Debug("Rejected: shallow title")
		return false
	}

	// Immediate rejection: bad subjects (fiction, self-help, etc)
	subjectsOK, _ := f.hasGoodSubjects(doc.Subject)
	if !subjectsOK {
		f.logger.WithField("title", doc.Title).Debug("Rejected: bad subjects")
		return false
	}

	// Everything else passes
	// We'll use quality signals to BOOST relevance instead of filtering
	return true
}
