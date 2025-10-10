package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/saaga/minerva/internal/database"
	"github.com/saaga/minerva/internal/services"
	"github.com/sirupsen/logrus"
)

type Pipeline struct {
	freshRSS    *services.FreshRSS
	extractor   *services.ContentExtractor
	ollama      *services.Ollama
	searxng     *services.SearXNG
	openLibrary *services.OpenLibrary
	koha        *services.Koha
	ntfy        *services.Ntfy
	db          *database.DB
	logger      *logrus.Logger
	dryRun      bool
}

type Config struct {
	FreshRSS    *services.FreshRSS
	Extractor   *services.ContentExtractor
	Ollama      *services.Ollama
	SearXNG     *services.SearXNG
	OpenLibrary *services.OpenLibrary
	Koha        *services.Koha
	Ntfy        *services.Ntfy
	Database    *database.DB
	Logger      *logrus.Logger
	DryRun      bool
}

func New(cfg Config) *Pipeline {
	// Set logger for all services
	cfg.FreshRSS.SetLogger(cfg.Logger)
	cfg.Extractor.SetLogger(cfg.Logger)
	cfg.Ollama.SetLogger(cfg.Logger)
	cfg.SearXNG.SetLogger(cfg.Logger)
	cfg.OpenLibrary.SetLogger(cfg.Logger)
	cfg.Koha.SetLogger(cfg.Logger)
	cfg.Ntfy.SetLogger(cfg.Logger)

	return &Pipeline{
		freshRSS:    cfg.FreshRSS,
		extractor:   cfg.Extractor,
		ollama:      cfg.Ollama,
		searxng:     cfg.SearXNG,
		openLibrary: cfg.OpenLibrary,
		koha:        cfg.Koha,
		ntfy:        cfg.Ntfy,
		db:          cfg.Database,
		logger:      cfg.Logger,
		dryRun:      cfg.DryRun,
	}
}
func (p *Pipeline) Run(ctx context.Context) error {
	p.logger.Info("Starting Minerva pipeline")
	startTime := time.Now()

	// Step 1: Fetch starred RSS items from FreshRSS
	p.logger.Info("Step 1: Fetching starred RSS items from FreshRSS")
	rssItems, err := p.fetchStarredItems(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch RSS items: %w", err)
	}

	// Step 2: Extract content from new articles
	p.logger.Info("Step 2: Extracting content from articles")
	articles, err := p.extractContent(ctx, rssItems)
	if err != nil {
		return fmt.Errorf("failed to extract content: %w", err)
	}

	// Step 3: Process content with Ollama
	p.logger.Info("Step 3: Processing content with Ollama")
	if err := p.processWithOllama(ctx, articles); err != nil {
		return fmt.Errorf("failed to process with Ollama: %w", err)
	}

	// Step 4: Generate book recommendations
	p.logger.Info("Step 4: Generating book recommendations")
	if err := p.generateBookRecommendations(ctx); err != nil {
		return fmt.Errorf("failed to generate book recommendations: %w", err)
	}

	// Step 5: Check ownership and find related books in Koha
	p.logger.Info("Step 5: Checking Koha for ownership and related books")
	newBooks, ownedBooks, err := p.checkKohaOwnership(ctx)
	if err != nil {
		return fmt.Errorf("failed to check Koha: %w", err)
	}

	var articleSummaries []services.ArticleSummary
	for _, article := range articles {
		articleSummaries = append(articleSummaries, services.ArticleSummary{
			ID:    article.ID,
			Title: article.Title,
			URL:   article.URL,
		})
	}

	if err := p.ntfy.NotifyPipelineComplete(ctx, articleSummaries, newBooks, ownedBooks, time.Since(startTime)); err != nil {
		p.logger.WithError(err).Warn("Failed to send Ntfy notification")
	}

	duration := time.Since(startTime)
	p.logger.WithFields(logrus.Fields{
		"duration":           duration,
		"articles_processed": len(articles),
	}).Info("Pipeline completed successfully")

	return nil
}

// fetchStarredItems gets starred RSS items from FreshRSS
func (p *Pipeline) fetchStarredItems(ctx context.Context) ([]services.FreshRSSItem, error) {
	if p.dryRun {
		p.logger.Info("DRY RUN: Skipping FreshRSS fetch")
		return []services.FreshRSSItem{
			{
				ID:    "dry-run-1",
				Title: "Example Article",
				Link:  "https://example.com/article",
			},
		}, nil
	}

	items, err := p.freshRSS.GetStarredItems()
	if err != nil {
		return nil, err
	}

	p.logger.WithField("count", len(items)).Info("Fetched starred RSS items")
	return items, nil
}

// extractContent extracts full content from RSS items
func (p *Pipeline) extractContent(ctx context.Context, rssItems []services.FreshRSSItem) ([]*database.Article, error) {
	var articles []*database.Article

	for i, item := range rssItems {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		p.logger.WithFields(logrus.Fields{
			"item":  i + 1,
			"total": len(rssItems),
			"url":   item.Link,
			"title": item.Title,
		}).Debug("Processing RSS item")

		// Check if article already exists
		exists, err := p.db.ArticleExists(item.Link)
		if err != nil {
			p.logger.WithError(err).Warn("Failed to check if article exists")
			continue
		}

		if exists {
			p.logger.WithField("url", item.Link).Debug("Article already exists, skipping")
			continue
		}

		// Extract content
		var content *services.ExtractedContent
		if p.dryRun {
			content = &services.ExtractedContent{
				Title:   item.Title,
				Content: "This is dry run content for " + item.Title,
				URL:     item.Link,
			}
		} else {
			var err error
			content, err = p.extractor.ExtractContent(item.Link)
			if err != nil {
				p.logger.WithError(err).WithField("url", item.Link).Warn("Failed to extract content")
				continue
			}
		}

		// Create article record
		article := &database.Article{
			URL:     item.Link,
			Title:   content.Title,
			Content: content.Content,
		}

		// Save to database
		if err := p.db.SaveArticle(article); err != nil {
			p.logger.WithError(err).Warn("Failed to save article")
			continue
		}

		articles = append(articles, article)

		p.logger.WithFields(logrus.Fields{
			"article_id": article.ID,
			"url":        article.URL,
		}).Debug("Article saved successfully")
	}

	p.logger.WithField("count", len(articles)).Info("Content extraction completed")
	return articles, nil
}

// processWithOllama processes articles with Ollama for metadata extraction
func (p *Pipeline) processWithOllama(ctx context.Context, articles []*database.Article) error {
	// Also process any previously unprocessed articles
	unprocessed, err := p.db.GetUnprocessedArticles()
	if err != nil {
		return fmt.Errorf("failed to get unprocessed articles: %w", err)
	}

	// Combine new and unprocessed articles
	allArticles := append(articles, unprocessed...)

	for i, article := range allArticles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		p.logger.WithFields(logrus.Fields{
			"article": i + 1,
			"total":   len(allArticles),
			"title":   article.Title,
		}).Debug("Processing article with Ollama")

		var processed *services.ProcessedContent
		if p.dryRun {
			processed = &services.ProcessedContent{
				Summary:  "This is a dry run summary for " + article.Title,
				Keywords: []string{"dry", "run", "test", "example", "article"},
				Insights: "This would contain insights about " + article.Title,
			}
		} else {
			var err error
			processed, err = p.ollama.ProcessContent(article.Title, article.Content)
			if err != nil {
				p.logger.WithError(err).WithField("article_id", article.ID).Warn("Failed to process with Ollama")
				continue
			}
		}

		// Update article with processed data
		if err := p.db.UpdateArticleProcessing(article.ID, processed.Summary, processed.Insights, processed.Keywords); err != nil {
			p.logger.WithError(err).Warn("Failed to update article processing")
			continue
		}

		p.logger.WithFields(logrus.Fields{
			"article_id":     article.ID,
			"keywords_count": len(processed.Keywords),
		}).Debug("Article processed successfully")
	}

	p.logger.WithField("count", len(allArticles)).Info("Ollama processing completed")
	return nil
}

// generateBookRecommendations searches for book recommendations based on processed articles
func (p *Pipeline) generateBookRecommendations(ctx context.Context) error {
	articles, err := p.db.GetProcessedArticlesWithoutRecommendations()
	if err != nil {
		return fmt.Errorf("failed to get articles for book recommendations: %w", err)
	}

	if len(articles) == 0 {
		p.logger.Info("No new articles to generate book recommendations for")
		return nil
	}

	for i, article := range articles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		p.logger.WithFields(logrus.Fields{
			"article": i + 1,
			"total":   len(articles),
			"title":   article.Title,
		}).Debug("Generating book recommendations")

		keywords := article.Keywords
		insights := article.Insights

		if len(keywords) == 0 {
			p.logger.WithField("article_id", article.ID).Warn("Article has no keywords, skipping")
			continue
		}

		if insights == "" {
			insights = article.Summary
		}

		var recommendations []services.BookRecommendation
		if p.dryRun {
			recommendations = []services.BookRecommendation{
				{
					Title:       "Example Book for: " + article.Title,
					Author:      "Example Author",
					ISBN:        "1234567890",
					ISBN13:      "9781234567890",
					PublishYear: 2024,
					Relevance:   0.9,
				},
			}
		} else {
			// Use OpenLibrary for actual book recommendations
			var err error
			recommendations, err = p.openLibrary.SearchBooks(keywords, insights)
			if err != nil {
				p.logger.WithError(err).WithField("article_id", article.ID).Warn("Failed to search books")
				continue
			}
		}

		// Save book recommendations
		for _, rec := range recommendations {
			dbRec := &database.BookRecommendation{
				ArticleID:      article.ID,
				Title:          rec.Title,
				Author:         rec.Author,
				ISBN:           rec.ISBN,
				ISBN13:         rec.ISBN13,
				PublishYear:    rec.PublishYear,
				Publisher:      rec.Publisher,
				CoverURL:       rec.CoverURL,
				OpenLibraryKey: rec.OpenLibraryKey,
				Relevance:      rec.Relevance,
			}

			if err := p.db.SaveBookRecommendation(dbRec); err != nil {
				p.logger.WithError(err).Warn("Failed to save book recommendation")
				continue
			}
		}

		p.logger.WithFields(logrus.Fields{
			"article_id":            article.ID,
			"recommendations_count": len(recommendations),
			"keywords_used":         keywords,
		}).Info("Book recommendations generated successfully")
	}

	p.logger.WithField("total_articles", len(articles)).Info("Book recommendation generation completed")
	return nil
}

func (p *Pipeline) checkKohaOwnership(ctx context.Context) ([]services.NotificationBook, []services.OwnedBookSummary, error) {
	recommendations, err := p.db.GetUncheckedRecommendations()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get unchecked recommendations: %w", err)
	}

	if len(recommendations) == 0 {
		p.logger.Info("No new recommendations to check against Koha")
		return nil, nil, nil
	}

	p.logger.WithField("count", len(recommendations)).Info("Checking recommendations against Koha catalog")

	var newBooks []services.NotificationBook
	var ownedBooks []services.OwnedBookSummary

	for _, rec := range recommendations {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		if p.dryRun {
			p.logger.WithField("title", rec.Title).Debug("DRY RUN: Skipping Koha check")
			continue
		}

		isbn := rec.ISBN13
		if isbn == "" {
			isbn = rec.ISBN
		}

		owned, kohaRecord, err := p.koha.CheckOwnership(isbn, rec.Title, rec.Author)
		if err != nil {
			p.logger.WithError(err).WithField("title", rec.Title).Warn("Failed to check Koha")
			continue
		}

		if owned && kohaRecord != nil {
			if err := p.db.MarkBookAsOwned(rec.ID); err != nil {
				p.logger.WithError(err).Warn("Failed to update ownership status")
				continue
			}

			ownedBooks = append(ownedBooks, services.OwnedBookSummary{
				Title:  kohaRecord.Title,
				Author: kohaRecord.Author,
				KohaID: kohaRecord.BiblioID,
			})

			p.logger.WithFields(logrus.Fields{
				"title":      rec.Title,
				"koha_id":    kohaRecord.BiblioID,
				"koha_title": kohaRecord.Title,
			}).Info("Book already owned in catalog")
		} else {
			newBooks = append(newBooks, services.NotificationBook{
				ArticleID: rec.ArticleID, // Add this
				Title:     rec.Title,
				Author:    rec.Author,
				Relevance: rec.Relevance,
			})
		}
	}

	p.logger.WithFields(logrus.Fields{
		"checked": len(recommendations),
		"owned":   len(ownedBooks),
		"new":     len(newBooks),
	}).Info("Koha ownership check completed")

	return newBooks, ownedBooks, nil
}
