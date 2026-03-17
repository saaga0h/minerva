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

	// Subscribe to checked works — persist, notify, and signal completion
	if err := mqttClient.Subscribe(mqttclient.TopicWorksChecked, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.CheckedWorks
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal CheckedWorks")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"new_works":  len(msg.NewWorks),
				"owned":      len(msg.OwnedWorks),
			}).Debug("Persisting and notifying")

			// Persist final book recommendations to SQLite — serialized to avoid write conflicts
			dbMu.Lock()
			persistRecommendations(log, db, msg)
			dbMu.Unlock()

			// Build notification data
			articleSummaries := []services.ArticleSummary{
				{Title: msg.ArticleTitle, URL: msg.ArticleURL},
			}

			newBooks := make([]services.NotificationBook, 0, len(msg.NewWorks))
			for _, w := range msg.NewWorks {
				author := ""
				if len(w.Authors) > 0 {
					author = w.Authors[0]
				}
				newBooks = append(newBooks, services.NotificationBook{
					Title:     w.Title,
					Author:    author,
					Relevance: w.Relevance,
				})
			}

			ownedBooks := make([]services.OwnedBookSummary, 0, len(msg.OwnedWorks))
			for _, w := range msg.OwnedWorks {
				ownedBooks = append(ownedBooks, services.OwnedBookSummary{
					Title:  w.Title,
					Author: w.Author,
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
		log.WithError(err).Fatal("Failed to subscribe to works/checked")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Notifier primitive ready — listening for checked works")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down notifier primitive")
}

func persistRecommendations(log *logrus.Logger, db *database.DB, msg mqttclient.CheckedWorks) {
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

	// Save new work recommendations (books and papers)
	for _, w := range msg.NewWorks {
		author := ""
		if len(w.Authors) > 0 {
			author = w.Authors[0]
		}
		rec := &database.BookRecommendation{
			ArticleID:   article.ID,
			Title:       w.Title,
			Author:      author,
			ISBN:        w.ISBN,
			ISBN13:      w.ISBN13,
			PublishYear: w.PublishYear,
			Publisher:   w.Publisher,
			CoverURL:    w.CoverURL,
			SourceKey:   w.ReferenceID,
			OwnedInKoha: false,
			Relevance:   w.Relevance,
		}
		if err := db.SaveBookRecommendation(rec); err != nil {
			log.WithError(err).WithField("title", w.Title).Warn("Failed to save work recommendation")
		}
	}

	// Save owned works with owned_in_koha = true
	for _, w := range msg.OwnedWorks {
		rec := &database.BookRecommendation{
			ArticleID:   article.ID,
			Title:       w.Title,
			Author:      w.Author,
			OwnedInKoha: true,
		}
		if err := db.SaveBookRecommendation(rec); err != nil {
			log.WithError(err).WithField("title", w.Title).Warn("Failed to save owned work")
		}
	}

	log.WithFields(logrus.Fields{
		"article_id": article.ID,
		"new_works":  len(msg.NewWorks),
		"owned":      len(msg.OwnedWorks),
	}).Debug("Persisted work recommendations")
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
