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
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-search-arxiv")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// arXiv service
	arxiv := services.NewArXiv(cfg.ArXiv)
	arxiv.SetLogger(log)

	// Subscribe to analyzed articles — search for paper recommendations
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
				log.WithField("article_id", msg.ArticleID).Warn("No keywords — skipping arXiv search")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":     msg.ArticleID,
				"keywords_count": len(msg.Keywords),
			}).Debug("Searching papers via arXiv")

			papers, err := arxiv.SearchPapers(msg.Keywords, msg.Insights)
			if err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("arXiv search failed — skipping")
				return
			}

			candidates := make([]mqttclient.BookCandidate, 0, len(papers))
			for _, p := range papers {
				candidates = append(candidates, mqttclient.BookCandidate{
					Title:       p.Title,
					Author:      p.Authors,
					PublishYear: p.PublishYear,
					SourceKey:   "arxiv:" + p.ArXivID,
					Relevance:   p.Relevance,
				})
			}

			out := mqttclient.BookCandidates{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    msg.Source,
					Timestamp: time.Now(),
				},
				ArticleTitle: msg.Title,
				ArticleURL:   msg.URL,
				Books:        candidates,
			}

			if err := mqttClient.Publish(mqttclient.TopicBooksCandidates, out); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Error("Failed to publish BookCandidates")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":   msg.ArticleID,
				"papers_found": len(candidates),
			}).Debug("Published arXiv paper candidates")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/analyzed")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("search-arxiv primitive ready — listening for analyzed articles")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down search-arxiv primitive")
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
