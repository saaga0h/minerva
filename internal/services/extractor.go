package services

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/saaga/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type ContentExtractor struct {
	config config.ExtractorConfig
	client *http.Client
	logger *logrus.Logger
}

type ExtractedContent struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url"`
}

func NewContentExtractor(cfg config.ExtractorConfig) *ContentExtractor {
	return &ContentExtractor{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// ExtractContent fetches and extracts clean content from a URL
func (e *ContentExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	e.logger.WithField("url", url).Debug("Extracting content from URL")

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", e.config.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	// Limit response size to prevent memory issues
	limitedReader := io.LimitReader(resp.Body, e.config.MaxSize)

	doc, err := goquery.NewDocumentFromReader(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	content := &ExtractedContent{
		URL: url,
	}

	// Extract title
	content.Title = e.extractTitle(doc)

	// Extract main content
	content.Content = e.extractMainContent(doc)

	// Clean and validate content for Ollama
	content.Content = e.cleanForOllama(content.Content)

	e.logger.WithFields(logrus.Fields{
		"url":            url,
		"title_length":   len(content.Title),
		"content_length": len(content.Content),
	}).Debug("Content extracted successfully")

	return content, nil
}

// extractTitle tries to find the best title for the article
func (e *ContentExtractor) extractTitle(doc *goquery.Document) string {
	// Try various title selectors in order of preference
	selectors := []string{
		"h1",
		"title",
		"h2",
		".title",
		".headline",
		"[property='og:title']",
	}

	for _, selector := range selectors {
		if title := strings.TrimSpace(doc.Find(selector).First().Text()); title != "" {
			return title
		}
	}

	return "No title found"
}

// extractMainContent tries to find and extract the main article content
func (e *ContentExtractor) extractMainContent(doc *goquery.Document) string {
	// Remove unwanted elements
	doc.Find("script, style, nav, header, footer, aside, .advertisement, .ads, .social-share").Remove()

	// Try various content selectors in order of preference
	contentSelectors := []string{
		"article",
		".content",
		".article-content",
		".post-content",
		".entry-content",
		"main",
		".main-content",
		"#content",
		".article-body",
		".story-body",
	}

	var content string
	for _, selector := range contentSelectors {
		if element := doc.Find(selector).First(); element.Length() > 0 {
			content = element.Text()
			break
		}
	}

	// Fallback: try to get content from body, excluding navigation and sidebar
	if content == "" {
		doc.Find("nav, .nav, .navigation, .sidebar, .menu, header, footer").Remove()
		content = doc.Find("body").Text()
	}

	return strings.TrimSpace(content)
}

// cleanForOllama cleans and formats content to be valid for Ollama JSON payload
func (e *ContentExtractor) cleanForOllama(content string) string {
	// Remove excessive whitespace
	content = regexp.MustCompile(`\s+`).ReplaceAllString(content, " ")

	// Remove or escape problematic characters for JSON
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.ReplaceAll(content, "\r", " ")
	content = strings.ReplaceAll(content, "\t", " ")

	// Remove control characters
	content = regexp.MustCompile(`[\x00-\x1f\x7f]`).ReplaceAllString(content, "")

	// Escape quotes for JSON safety
	content = strings.ReplaceAll(content, `"`, `\"`)
	content = strings.ReplaceAll(content, `\`, `\\`)

	// Trim and limit length to reasonable size for LLM processing
	content = strings.TrimSpace(content)
	if len(content) > 10000 {
		content = content[:10000] + "..."
	}

	return content
}

// SetLogger sets the logger instance
func (e *ContentExtractor) SetLogger(logger *logrus.Logger) {
	e.logger = logger
}
