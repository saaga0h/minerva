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
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-search-semantic-scholar")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
		Username:  getEnv("MQTT_USER", ""),
		Password:  getEnv("MQTT_PASSWORD", ""),
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Semantic Scholar service
	s2 := services.NewSemanticScholar(cfg.SemanticScholar)
	s2.SetLogger(log)

	// Ollama client for embedding — no mutex needed, embed is concurrent-safe
	ollama := services.NewOllama(cfg.Ollama)
	ollama.SetLogger(log)

	// Single-worker queue — Semantic Scholar free tier allows ~1 req/s; a worker pool
	// with concurrency > 1 would just queue up 429s. The MQTT handler enqueues without
	// blocking; the worker drains sequentially (throttle() in the service enforces the gap).
	type workItem struct{ msg mqttclient.AnalyzedArticle }
	queue := make(chan workItem, 256)

	go func() {
		for item := range queue {
			msg := item.msg

			if len(msg.Keywords) == 0 {
				log.WithField("article_id", msg.ArticleID).Warn("No keywords — skipping Semantic Scholar search")
				continue
			}

			log.WithFields(logrus.Fields{
				"article_id":     msg.ArticleID,
				"keywords_count": len(msg.Keywords),
			}).Debug("Searching papers via Semantic Scholar")

			papers, err := s2.SearchPapers(msg.Keywords, msg.Insights)
			if err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("Semantic Scholar search failed — skipping")
				continue
			}

			candidates := make([]mqttclient.WorkCandidate, 0, len(papers))
			for _, p := range papers {
				referenceID := "s2:" + p.PaperID
				if p.ArXivID != "" {
					referenceID = "arxiv:" + p.ArXivID
				}

				// Best-effort embedding: title + abstract. On failure, nil — work still publishes.
				embedding, embedErr := ollama.Embed(p.Title + " " + p.Abstract)
				if embedErr != nil {
					log.WithError(embedErr).WithFields(logrus.Fields{
						"article_id": msg.ArticleID,
						"paper_id":   p.PaperID,
					}).Warn("Embed failed for Semantic Scholar work — continuing without embedding")
					embedding = nil
				}

				candidates = append(candidates, mqttclient.WorkCandidate{
					ReferenceID:  referenceID,
					SearchSource: "semantic-scholar",
					WorkType:     "paper",
					Title:        p.Title,
					Authors:      []string{p.Authors},
					ArXivID:      p.ArXivID,
					PublishYear:  p.PublishYear,
					Relevance:    p.Relevance,
					Embedding:    embedding,
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
				continue
			}

			log.WithFields(logrus.Fields{
				"article_id":   msg.ArticleID,
				"papers_found": len(candidates),
			}).Debug("Published Semantic Scholar paper candidates")
		}
	}()

	// Subscribe to analyzed articles — enqueue for sequential processing.
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesAnalyzed, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.AnalyzedArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal AnalyzedArticle")
				return
			}
			queue <- workItem{msg}
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/analyzed")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("search-semantic-scholar primitive ready — listening for analyzed articles")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down search-semantic-scholar primitive")
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
