package main

import (
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

	// Open recommendations DB — storage owns all SQLite writes
	db, err := database.New(cfg.Database.Path)
	if err != nil {
		log.WithError(err).Fatal("Failed to open recommendations DB")
	}
	defer db.Close()

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-storage")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	var dbMu sync.Mutex

	// Subscribe to analyzed articles — store LLM-extracted metadata
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesAnalyzed, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.AnalyzedArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal AnalyzedArticle")
				return
			}

			rec := database.AnalyzedArticleRecord{
				ArticleID: msg.ArticleID,
				SourceID:  msg.SourceID,
				URL:       msg.URL,
				Title:     msg.Title,
				Domain:    msg.Domain,
				Summary:   msg.Summary,
				Keywords:  msg.Keywords,
				Concepts:  msg.Concepts,
				Insights:  msg.Insights,
			}

			dbMu.Lock()
			err := db.SaveAnalyzedArticle(rec)
			dbMu.Unlock()

			if err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("Failed to save analyzed article")
			} else {
				log.WithField("article_id", msg.ArticleID).Debug("Saved analyzed article")
			}
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/analyzed")
	}

	// Subscribe to book candidates — upsert candidates and publish ArticleComplete
	if err := mqttClient.Subscribe(mqttclient.TopicBooksCandidates, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.BookCandidates
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal BookCandidates")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"books":      len(msg.Books),
			}).Debug("Storing book candidates")

			// Ensure article row exists
			dbMu.Lock()
			articleDBID, err := ensureArticle(db, msg.ArticleURL, msg.ArticleTitle)
			if err != nil {
				dbMu.Unlock()
				log.WithError(err).WithField("url", msg.ArticleURL).Warn("Failed to ensure article row")
				return
			}

			for _, b := range msg.Books {
				rec := database.BookRecommendation{
					Title:       b.Title,
					Author:      b.Author,
					ISBN:        b.ISBN,
					ISBN13:      b.ISBN13,
					PublishYear: b.PublishYear,
					Publisher:   b.Publisher,
					CoverURL:    b.CoverURL,
					SourceKey:   b.SourceKey,
					Relevance:   b.Relevance,
				}
				if err := db.UpsertBookCandidate(articleDBID, msg.ArticleID, rec); err != nil {
					log.WithError(err).WithField("title", b.Title).Warn("Failed to upsert book candidate")
				}
			}
			dbMu.Unlock()

			// Publish ArticleComplete — source primitives use this to mark articles done
			complete := mqttclient.ArticleComplete{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    "storage",
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
		log.WithError(err).Fatal("Failed to subscribe to books/candidates")
	}

	// Subscribe to checked books — update Koha ownership on existing records
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
				"owned":      len(msg.OwnedBooks),
			}).Debug("Updating Koha ownership")

			dbMu.Lock()
			articleDBID, err := db.GetArticleIDByURL(msg.ArticleURL)
			if err != nil {
				dbMu.Unlock()
				log.WithError(err).WithField("url", msg.ArticleURL).Warn("Article not found for Koha update")
				return
			}
			for _, b := range msg.OwnedBooks {
				if err := db.UpdateKohaOwnershipByTitle(articleDBID, b.Title, b.KohaID); err != nil {
					log.WithError(err).WithField("title", b.Title).Warn("Failed to update Koha ownership")
				}
			}
			dbMu.Unlock()
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to books/checked")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Storage primitive ready — listening for pipeline events")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down storage primitive")
}

// ensureArticle upserts an article row and returns its integer PK.
func ensureArticle(db *database.DB, url, title string) (int, error) {
	if id, err := db.GetArticleIDByURL(url); err == nil {
		return id, nil
	}
	article := &database.Article{
		URL:   url,
		Title: title,
	}
	if err := db.SaveArticle(article); err != nil {
		return 0, err
	}
	return article.ID, nil
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
