package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/internal/store"
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

	// Knowledge base — PostgreSQL. Fatal if unavailable: store has no purpose without it.
	log.WithField("dsn_env", os.Getenv("STORE_DSN")).Info("DSN from environment")
	log.WithField("dsn_cfg", cfg.Store.DSN).Info("DSN from config")
	ctx := context.Background()
	db, err := store.New(ctx, cfg.Store.DSN)
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to knowledge base (PostgreSQL)")
	}
	defer db.Close()

	log.Info("Connected to knowledge base")

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-store")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// ── minerva/articles/extracted ──────────────────────────────────────────
	// Store full article content as it enters the pipeline.
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesExtracted, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.ExtractedArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("store: failed to unmarshal ExtractedArticle")
				return
			}

			if err := db.UpsertArticleContent(ctx,
				msg.ArticleID, msg.URL, msg.Title, msg.Source, msg.Content, msg.Timestamp,
			); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("store: failed to upsert article content")
				return
			}

			log.WithField("article_id", msg.ArticleID).Debug("store: saved article content")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/extracted")
	}

	// ── minerva/articles/analyzed ───────────────────────────────────────────
	// Store full LLM analysis including entities discarded from the pipeline message.
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesAnalyzed, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.AnalyzedArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("store: failed to unmarshal AnalyzedArticle")
				return
			}

			entities := map[string]any{
				"facilities": msg.Entities.Facilities,
				"people":     msg.Entities.People,
				"locations":  msg.Entities.Locations,
				"phenomena":  msg.Entities.Phenomena,
			}

			a := store.ArticleAnalysis{
				ArticleID:     msg.ArticleID,
				URL:           msg.URL,
				Title:         msg.Title,
				Source:        msg.Source,
				Domain:        msg.Domain,
				ArticleType:   msg.ArticleType,
				Summary:       msg.Summary,
				Keywords:      msg.Keywords,
				Concepts:      msg.Concepts,
				RelatedTopics: msg.RelatedTopics,
				Entities:      entities,
				Insights:      msg.Insights,
				AnalyzedAt:    msg.Timestamp,
			}

			if err := db.UpsertArticleAnalysis(ctx, a); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("store: failed to upsert article analysis")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":  msg.ArticleID,
				"domain":      msg.Domain,
				"article_type": msg.ArticleType,
			}).Debug("store: saved article analysis")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/analyzed")
	}

	// ── minerva/works/candidates ────────────────────────────────────────────
	// Store all discovered works and link them to the article.
	if err := mqttClient.Subscribe(mqttclient.TopicWorksCandidates, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.WorkCandidates
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("store: failed to unmarshal WorkCandidates")
				return
			}

			for _, w := range msg.Works {
				wi := store.WorkInput{
					ReferenceID:  w.ReferenceID,
					SearchSource: w.SearchSource,
					WorkType:     w.WorkType,
					Title:        w.Title,
					Authors:      w.Authors,
					ISBN:         w.ISBN,
					ISBN13:       w.ISBN13,
					ISSN:         w.ISSN,
					DOI:          w.DOI,
					ArXivID:      w.ArXivID,
					PublishYear:  w.PublishYear,
					Publisher:    w.Publisher,
					CoverURL:     w.CoverURL,
					Relevance:    w.Relevance,
				}

				workID, err := db.UpsertWork(ctx, wi)
				if err != nil {
					log.WithError(err).WithFields(logrus.Fields{
						"article_id":   msg.ArticleID,
						"reference_id": w.ReferenceID,
					}).Warn("store: failed to upsert work")
					continue
				}

				if err := db.LinkArticleWork(ctx, msg.ArticleID, workID, w.SearchSource, w.Relevance); err != nil {
					log.WithError(err).WithFields(logrus.Fields{
						"article_id": msg.ArticleID,
						"work_id":    workID,
					}).Warn("store: failed to link article to work")
				}
			}

			log.WithFields(logrus.Fields{
				"article_id":  msg.ArticleID,
				"works_count": len(msg.Works),
			}).Debug("store: saved work candidates")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to works/candidates")
	}

	// ── minerva/works/checked ───────────────────────────────────────────────
	// Final pipeline stage. Mark the article complete and publish ArticleComplete
	// so source primitives skip it on future runs and the state primitive cleans up.
	if err := mqttClient.Subscribe(mqttclient.TopicWorksChecked, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.CheckedWorks
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("store: failed to unmarshal CheckedWorks")
				return
			}

			if err := db.MarkCompleteByArticleID(ctx, msg.ArticleID); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("store: failed to mark article complete")
				return
			}

			complete := mqttclient.ArticleComplete{
				Envelope:    mqttclient.Envelope{ArticleID: msg.ArticleID},
				CompletedAt: time.Now(),
			}
			if err := mqttClient.Publish(mqttclient.TopicArticlesComplete, complete); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("store: failed to publish ArticleComplete")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":  msg.ArticleID,
				"owned_count": len(msg.OwnedWorks),
			}).Debug("store: article complete")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to works/checked")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Store primitive ready — observing pipeline")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down store primitive")
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
