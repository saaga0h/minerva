package services

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/go-readability"
	"github.com/saaga0h/minerva/internal/config"
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

	// Try go-readability first
	article, err := e.extractWithReadability(url)
	if err == nil && len(article.Content) > 500 {
		e.logger.WithFields(logrus.Fields{
			"url":            url,
			"method":         "readability",
			"title_length":   len(article.Title),
			"content_length": len(article.Content),
		}).Debug("Content extracted successfully")
		return article, nil
	}

	e.logger.WithError(err).Debug("Readability extraction failed or insufficient content, falling back to manual extraction")

	// Fallback to manual extraction
	return e.extractManually(url)
}

// extractWithReadability uses go-readability for generic extraction
func (e *ContentExtractor) extractWithReadability(url string) (*ExtractedContent, error) {
	// FromURL is the correct method
	article, err := readability.FromURL(url, time.Duration(e.config.Timeout)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("readability parse failed: %w", err)
	}

	// Clean content for Ollama
	cleanContent := e.cleanForOllama(article.TextContent)

	return &ExtractedContent{
		Title:   article.Title,
		Content: cleanContent,
		URL:     url,
	}, nil
}

// extractManually is the fallback manual extraction method
func (e *ContentExtractor) extractManually(url string) (*ExtractedContent, error) {
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

	limitedReader := io.LimitReader(resp.Body, e.config.MaxSize)

	doc, err := goquery.NewDocumentFromReader(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	content := &ExtractedContent{
		URL: url,
	}

	content.Title = e.extractTitle(doc)
	content.Content = e.extractMainContent(doc)
	content.Content = e.cleanForOllama(content.Content)

	e.logger.WithFields(logrus.Fields{
		"url":            url,
		"method":         "manual",
		"title_length":   len(content.Title),
		"content_length": len(content.Content),
	}).Debug("Content extracted successfully")

	return content, nil
}

// extractTitle tries to find the best title for the article
func (e *ContentExtractor) extractTitle(doc *goquery.Document) string {
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

	// Try various content selectors
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

	if content == "" {
		doc.Find("nav, .nav, .navigation, .sidebar, .menu, header, footer").Remove()
		content = doc.Find("body").Text()
	}

	return strings.TrimSpace(content)
}

// cleanForOllama cleans and formats content for Ollama
func (e *ContentExtractor) cleanForOllama(content string) string {
	// Remove excessive whitespace
	content = regexp.MustCompile(`\s+`).ReplaceAllString(content, " ")

	// Remove control characters
	content = regexp.MustCompile(`[\x00-\x1f\x7f]`).ReplaceAllString(content, "")

	// Trim and limit length, trying to cut at sentence boundary
	content = strings.TrimSpace(content)
	if len(content) > 10000 {
		truncated := content[:10000]
		// Try to find last sentence before 10k
		lastPeriod := strings.LastIndex(truncated, ". ")
		if lastPeriod > 8000 {
			content = truncated[:lastPeriod+1]
		} else {
			content = truncated + "..."
		}
	}

	return content
}

// SetLogger sets the logger instance
func (e *ContentExtractor) SetLogger(logger *logrus.Logger) {
	e.logger = logger
}
