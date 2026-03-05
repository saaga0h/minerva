package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
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

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-book-search")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// OpenLibrary service
	openLibrary := services.NewOpenLibrary(cfg.OpenLibrary)
	openLibrary.SetLogger(log)

	// Subscribe to analyzed articles — search for book recommendations
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesAnalyzed, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.AnalyzedArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal AnalyzedArticle")
				return
			}

			if len(msg.Keywords) == 0 {
				log.WithField("article_id", msg.ArticleID).Warn("No keywords — skipping book search")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":     msg.ArticleID,
				"keywords_count": len(msg.Keywords),
			}).Debug("Searching books via OpenLibrary")

			recommendations, err := openLibrary.SearchBooks(msg.Keywords, msg.Insights)
			if err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("Book search failed — article dropped for this run")
				return
			}

			// Convert services.BookRecommendation to mqttclient.WorkCandidate
			candidates := make([]mqttclient.WorkCandidate, 0, len(recommendations))
			for _, rec := range recommendations {
				candidates = append(candidates, mqttclient.WorkCandidate{
					ReferenceID:  "openlibrary:" + rec.OpenLibraryKey,
					SearchSource: "openlibrary",
					WorkType:     "book",
					Title:        rec.Title,
					Authors:      []string{rec.Author},
					ISBN:         rec.ISBN,
					ISBN13:       rec.ISBN13,
					PublishYear:  rec.PublishYear,
					Publisher:    rec.Publisher,
					CoverURL:     rec.CoverURL,
					Relevance:    rec.Relevance,
				})
			}

			out := mqttclient.WorkCandidates{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    msg.Source,
					Timestamp: time.Now(),
				},
				ArticleTitle: msg.Title,
				ArticleURL:   msg.URL,
				Works:        candidates,
			}

			if err := mqttClient.Publish(mqttclient.TopicWorksCandidates, out); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Error("Failed to publish WorkCandidates")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":  msg.ArticleID,
				"works_found": len(candidates),
			}).Debug("Published work candidates")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/analyzed")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Book-search primitive ready — listening for analyzed articles")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down book-search primitive")
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
