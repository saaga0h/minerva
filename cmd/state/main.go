package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/internal/statestore"
	"github.com/saaga0h/minerva/pkg/logger"
	"github.com/sirupsen/logrus"
)

// observedTopics are all pipeline stage topics the state primitive taps.
// The state primitive stores every message it sees without parsing content.
var observedTopics = []string{
	mqttclient.TopicArticlesRaw,
	mqttclient.TopicArticlesExtracted,
	mqttclient.TopicArticlesAnalyzed,
	mqttclient.TopicWorksCandidates,
	mqttclient.TopicWorksChecked,
}

// stageOrder defines replay priority. Higher index = more advanced pipeline stage.
// On replay, each article is resumed from its most advanced recorded stage.
var stageOrder = map[string]int{
	mqttclient.TopicArticlesRaw:       0,
	mqttclient.TopicArticlesExtracted: 1,
	mqttclient.TopicArticlesAnalyzed:  2,
	mqttclient.TopicWorksCandidates:   3,
	mqttclient.TopicWorksChecked:      4,
}

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

	ctx := context.Background()
	db, err := statestore.New(ctx, cfg.Store.DSN)
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to state store (PostgreSQL)")
	}
	defer db.Close()

	log.Info("Connected to state store")

	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-state")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// ── Observe all pipeline stage topics ───────────────────────────────────
	// Store raw payload per (article_id, topic) without parsing content.
	for _, topic := range observedTopics {
		topic := topic
		if err := mqttClient.Subscribe(topic, func(payload []byte) {
			data := make([]byte, len(payload))
			copy(data, payload)
			go func() {
				articleID := extractArticleID(data)
				if articleID == "" {
					log.WithField("topic", topic).Warn("state: could not extract article_id, skipping")
					return
				}
				if err := db.UpsertState(ctx, articleID, topic, data); err != nil {
					log.WithError(err).WithFields(logrus.Fields{
						"article_id": articleID,
						"topic":      topic,
					}).Warn("state: failed to upsert state")
					return
				}
				log.WithFields(logrus.Fields{
					"article_id": articleID,
					"topic":      topic,
				}).Debug("state: recorded pipeline message")
			}()
		}); err != nil {
			log.WithError(err).Fatalf("Failed to subscribe to %s", topic)
		}
	}

	// ── Trigger — replay incomplete articles ─────────────────────────────────
	// On trigger, replay the most advanced stored stage for each article so the
	// pipeline resumes from where it left off. New articles (no stored state)
	// are unaffected — sources publish them normally.
	if err := mqttClient.Subscribe(mqttclient.TopicPipelineTrigger, func(_ []byte) {
		go func() {
			pending, err := db.PendingArticles(ctx, stageOrder)
			if err != nil {
				log.WithError(err).Error("state: failed to query pending articles")
				return
			}

			if len(pending) == 0 {
				log.Debug("state: no incomplete articles to replay")
				return
			}

			log.WithField("count", len(pending)).Info("state: replaying incomplete articles")

			for _, e := range pending {
				if err := mqttClient.PublishRaw(e.Topic, e.Payload); err != nil {
					log.WithError(err).WithFields(logrus.Fields{
						"article_id": e.ArticleID,
						"topic":      e.Topic,
					}).Warn("state: failed to replay message")
				} else {
					log.WithFields(logrus.Fields{
						"article_id": e.ArticleID,
						"topic":      e.Topic,
					}).Debug("state: replayed message")
				}
			}
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to pipeline/trigger")
	}

	// ── ArticleComplete — delete stored state ────────────────────────────────
	// Article has finished the full pipeline. Remove its state so it won't be
	// replayed on future triggers.
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesComplete, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			articleID := extractArticleID(data)
			if articleID == "" {
				return
			}
			if err := db.DeleteArticle(ctx, articleID); err != nil {
				log.WithError(err).WithField("article_id", articleID).Warn("state: failed to delete completed article state")
				return
			}
			log.WithField("article_id", articleID).Debug("state: deleted completed article state")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/complete")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("State primitive ready — observing pipeline for crash recovery")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down state primitive")
}

// extractArticleID pulls article_id from a raw JSON envelope without full unmarshal.
func extractArticleID(payload []byte) string {
	var env struct {
		ArticleID string `json:"article_id"`
	}
	json.Unmarshal(payload, &env) //nolint:errcheck
	return env.ArticleID
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
