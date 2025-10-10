package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saaga/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type Ntfy struct {
	config config.NtfyConfig
	client *http.Client
	logger *logrus.Logger
}

type Notification struct {
	Topic    string   `json:"topic"`
	Message  string   `json:"message"`
	Title    string   `json:"title"`
	Priority string   `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Actions  []Action `json:"actions,omitempty"`
}

type Action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url,omitempty"`
	Clear  bool   `json:"clear,omitempty"`
}

// PipelineStats holds statistics for pipeline notification
type PipelineStats struct {
	ArticlesProcessed  int
	NewRecommendations int
	BooksAlreadyOwned  int
	TotalChecked       int
	Duration           time.Duration
	Articles           []ArticleSummary
	OwnedBooks         []OwnedBookSummary
	TopRecommendations []NotificationBook
}

type ArticleSummary struct {
	ID    int
	Title string
	URL   string
}

type OwnedBookSummary struct {
	Title   string
	Author  string
	KohaID  int
	CallNum string
}

type NotificationBook struct {
	ArticleID int
	Title     string
	Author    string
	Relevance float64
}

func NewNtfy(cfg config.NtfyConfig) *Ntfy {
	return &Ntfy{
		config: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logrus.New(),
	}
}

// SetLogger sets the logger instance
func (n *Ntfy) SetLogger(logger *logrus.Logger) {
	n.logger = logger
}

// SendPipelineComplete sends a formatted notification about pipeline completion
func (n *Ntfy) SendPipelineComplete(ctx context.Context, stats PipelineStats) error {
	if !n.config.Enabled {
		n.logger.Debug("Ntfy disabled, skipping notification")
		return nil
	}

	if n.config.Topic == "" {
		return fmt.Errorf("ntfy topic not configured")
	}

	// Build the notification message
	message := n.buildPipelineMessage(stats)
	title := n.buildPipelineTitle(stats)

	notification := Notification{
		Topic:    n.config.Topic,
		Title:    title,
		Message:  message,
		Priority: n.determinePriority(stats),
		Tags:     n.buildTags(stats),
	}

	// Add click action if we have articles
	if len(stats.Articles) > 0 {
		notification.Click = stats.Articles[0].URL
	}

	return n.Send(ctx, notification)
}

// Send sends a notification to Ntfy
func (n *Ntfy) Send(ctx context.Context, notification Notification) error {
	if !n.config.Enabled {
		return nil
	}

	// Build URL: baseURL/topic
	notifyURL := fmt.Sprintf("%s/%s", n.config.BaseURL, notification.Topic)

	// Add query parameters
	params := url.Values{}
	if notification.Title != "" {
		params.Set("title", notification.Title)
	}
	if notification.Priority != "" {
		params.Set("priority", notification.Priority)
	}
	if notification.Click != "" {
		params.Set("click", notification.Click)
	}
	if len(notification.Tags) > 0 {
		params.Set("tags", strings.Join(notification.Tags, ","))
	}

	if len(params) > 0 {
		notifyURL += "?" + params.Encode()
	}

	// Message body is plain text, not JSON
	req, err := http.NewRequestWithContext(ctx, "PUT", notifyURL, strings.NewReader(notification.Message))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	if n.config.Token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", n.config.Token))
	}

	n.logger.WithFields(logrus.Fields{
		"url":      notifyURL,
		"title":    notification.Title,
		"priority": notification.Priority,
	}).Debug("Sending Ntfy notification")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		n.logger.WithFields(logrus.Fields{
			"status": resp.StatusCode,
			"body":   string(body),
		}).Error("Ntfy request failed")
		return fmt.Errorf("ntfy returned status %d: %s", resp.StatusCode, string(body))
	}

	n.logger.Info("Ntfy notification sent successfully")
	return nil
}

func (n *Ntfy) buildPipelineTitle(stats PipelineStats) string {
	if stats.ArticlesProcessed == 0 {
		return "📚 Minerva - No new articles"
	}

	newBooks := stats.TotalChecked - stats.BooksAlreadyOwned
	if newBooks > 0 {
		return fmt.Sprintf("📚 Minerva - %d new books found!", newBooks)
	}

	return "📚 Minerva - Pipeline complete"
}

// In internal/services/ntfy.go
func (n *Ntfy) buildPipelineMessage(stats PipelineStats) string {
	var sb strings.Builder

	// Group books by article
	booksByArticle := make(map[int][]NotificationBook)
	for _, book := range stats.TopRecommendations {
		booksByArticle[book.ArticleID] = append(booksByArticle[book.ArticleID], book)
	}

	// Build article sections with their books
	for _, article := range stats.Articles {
		books := booksByArticle[article.ID]
		if len(books) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("📰 %s\n", article.Title))
		sb.WriteString(fmt.Sprintf("New books (%d):\n", len(books)))
		for _, book := range books {
			sb.WriteString(fmt.Sprintf("• %s - %s\n", book.Title, book.Author))
		}
		sb.WriteString("\n")
	}

	// Show owned books at the end
	if len(stats.OwnedBooks) > 0 {
		sb.WriteString(fmt.Sprintf("✅ Already in your library (%d):\n", len(stats.OwnedBooks)))
		for _, book := range stats.OwnedBooks {
			sb.WriteString(fmt.Sprintf("• %s\n", book.Title))
		}
	}

	return strings.TrimSpace(sb.String())
}

func (n *Ntfy) determinePriority(stats PipelineStats) string {
	// High priority if we have new recommendations
	if stats.NewRecommendations > 5 {
		return "high"
	}

	// Default priority from config
	if n.config.Priority != "" {
		return n.config.Priority
	}

	return "default"
}

func (n *Ntfy) buildTags(stats PipelineStats) []string {
	tags := []string{"minerva", "books"}

	if stats.ArticlesProcessed > 0 {
		tags = append(tags, "articles")
	}

	if stats.NewRecommendations > 0 {
		tags = append(tags, "recommendations")
	}

	if stats.BooksAlreadyOwned > 0 {
		tags = append(tags, "library")
	}

	return tags
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

func (n *Ntfy) NotifyPipelineComplete(ctx context.Context, articles []ArticleSummary, newBooks []NotificationBook, ownedBooks []OwnedBookSummary, duration time.Duration) error {
	if !n.config.Enabled {
		n.logger.Debug("Ntfy disabled, skipping notification")
		return nil
	}

	stats := PipelineStats{
		ArticlesProcessed:  len(articles),
		NewRecommendations: len(newBooks),
		BooksAlreadyOwned:  len(ownedBooks),
		Articles:           articles,
		TopRecommendations: newBooks,
		OwnedBooks:         ownedBooks,
		Duration:           duration,
	}

	return n.SendPipelineComplete(ctx, stats)
}
