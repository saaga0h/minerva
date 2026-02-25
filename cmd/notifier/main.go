package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	"github.com/saaga0h/minerva/internal/database"
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/internal/services"
	"github.com/saaga0h/minerva/pkg/logger"
	"github.com/sirupsen/logrus"
)

func main() {
	configPath := flag.String("config", "", "Path to configuration file")
	flag.Parse()

	log := logger.New()

	if *configPath != "" {
		if err := godotenv.Load(*configPath); err != nil {
			log.WithError(err).Fatal("Failed to load config file")
		}
	} else {
		godotenv.Load()
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.WithError(err).Fatal("Failed to load configuration")
	}
	logger.SetLevel(cfg.Log.Level)

	// Final output DB — book recommendations storage
	db, err := database.New(cfg.Database.Path)
	if err != nil {
		log.WithError(err).Fatal("Failed to open recommendations DB")
	}
	defer db.Close()

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-notifier")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Ntfy service
	ntfy := services.NewNtfy(cfg.Ntfy)
	ntfy.SetLogger(log)

	var dbMu sync.Mutex

	// Subscribe to checked books — persist, notify, and signal completion
	if err := mqttClient.Subscribe(mqttclient.TopicBooksChecked, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.CheckedBooks
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal CheckedBooks")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"new_books":  len(msg.NewBooks),
				"owned":      len(msg.OwnedBooks),
			}).Debug("Persisting and notifying")

			// Persist final book recommendations to SQLite — serialized to avoid write conflicts
			dbMu.Lock()
			persistRecommendations(log, db, msg)
			dbMu.Unlock()

			// Build notification data
			articleSummaries := []services.ArticleSummary{
				{Title: msg.ArticleTitle, URL: msg.ArticleURL},
			}

			newBooks := make([]services.NotificationBook, 0, len(msg.NewBooks))
			for _, b := range msg.NewBooks {
				newBooks = append(newBooks, services.NotificationBook{
					Title:     b.Title,
					Author:    b.Author,
					Relevance: b.Relevance,
				})
			}

			ownedBooks := make([]services.OwnedBookSummary, 0, len(msg.OwnedBooks))
			for _, b := range msg.OwnedBooks {
				ownedBooks = append(ownedBooks, services.OwnedBookSummary{
					Title:  b.Title,
					Author: b.Author,
				})
			}

			ctx := context.Background()
			if err := ntfy.NotifyPipelineComplete(ctx, articleSummaries, newBooks, ownedBooks, 0); err != nil {
				log.WithError(err).Warn("Failed to send Ntfy notification")
			}

			// Publish completion event — source primitives listen for this
			complete := mqttclient.ArticleComplete{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    "notifier",
					Timestamp: time.Now(),
				},
				CompletedAt: time.Now(),
			}

			if err := mqttClient.Publish(mqttclient.TopicArticlesComplete, complete); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Error("Failed to publish ArticleComplete")
			} else {
				log.WithField("article_id", msg.ArticleID).Debug("Published ArticleComplete")
			}
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to books/checked")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Notifier primitive ready — listening for checked books")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down notifier primitive")
}

func persistRecommendations(log *logrus.Logger, db *database.DB, msg mqttclient.CheckedBooks) {
	// Save the article record first (notifier owns the final DB state)
	article := &database.Article{
		URL:   msg.ArticleURL,
		Title: msg.ArticleTitle,
	}

	// Check if article already exists; if not, save it
	exists, err := db.ArticleExists(msg.ArticleURL)
	if err != nil {
		log.WithError(err).Warn("Failed to check article existence")
	}

	if !exists {
		article.Content = "" // content not retained in final DB
		if err := db.SaveArticle(article); err != nil {
			log.WithError(err).Warn("Failed to save article to DB")
			return
		}
	} else {
		// Retrieve existing article ID for foreign key
		if id, err := db.GetArticleIDByURL(msg.ArticleURL); err == nil {
			article.ID = id
		}
	}

	// Save new book recommendations
	for _, b := range msg.NewBooks {
		rec := &database.BookRecommendation{
			ArticleID:      article.ID,
			Title:          b.Title,
			Author:         b.Author,
			ISBN:           b.ISBN,
			ISBN13:         b.ISBN13,
			PublishYear:    b.PublishYear,
			Publisher:      b.Publisher,
			CoverURL:       b.CoverURL,
			OpenLibraryKey: b.OpenLibraryKey,
			OwnedInKoha:    false,
			Relevance:      b.Relevance,
		}
		if err := db.SaveBookRecommendation(rec); err != nil {
			log.WithError(err).WithField("title", b.Title).Warn("Failed to save book recommendation")
		}
	}

	// Save owned books with owned_in_koha = true
	for _, b := range msg.OwnedBooks {
		rec := &database.BookRecommendation{
			ArticleID:   article.ID,
			Title:       b.Title,
			Author:      b.Author,
			OwnedInKoha: true,
		}
		if err := db.SaveBookRecommendation(rec); err != nil {
			log.WithError(err).WithField("title", b.Title).Warn("Failed to save owned book")
		}
	}

	log.WithFields(logrus.Fields{
		"article_id": article.ID,
		"new_books":  len(msg.NewBooks),
		"owned":      len(msg.OwnedBooks),
	}).Debug("Persisted book recommendations")
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func generateID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
