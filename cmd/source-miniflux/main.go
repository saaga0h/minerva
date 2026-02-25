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
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/internal/services"
	"github.com/saaga0h/minerva/internal/state"
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

	logLevel := getEnv("LOG_LEVEL", "info")
	logger.SetLevel(logLevel)

	// Miniflux config from environment
	minifluxCfg := services.MinifluxConfig{
		BaseURL: getEnv("MINIFLUX_BASE_URL", ""),
		APIKey:  getEnv("MINIFLUX_API_KEY", ""),
		Timeout: 30,
	}
	if minifluxCfg.BaseURL == "" {
		log.Fatal("MINIFLUX_BASE_URL is required")
	}

	// State DB
	stateDBPath := getEnv("STATE_DB_PATH", "./data/miniflux-state.db")
	stateDB, err := state.New(stateDBPath)
	if err != nil {
		log.WithError(err).Fatal("Failed to open state DB")
	}
	defer stateDB.Close()

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-source-miniflux")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Miniflux service
	miniflux := services.NewMiniflux(minifluxCfg)
	miniflux.SetLogger(log)

	// Subscribe to completion events
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesComplete, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.ArticleComplete
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal ArticleComplete message")
				return
			}
			if err := stateDB.MarkCompleteByArticleID(msg.ArticleID); err != nil {
				log.WithError(err).Warn("Failed to mark article complete")
			} else {
				log.WithField("article_id", msg.ArticleID).Debug("Marked article complete")
			}
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to completion topic")
	}

	// Subscribe to trigger
	if err := mqttClient.Subscribe(mqttclient.TopicPipelineTrigger, func(_ []byte) {
		log.Info("Trigger received — fetching starred entries from Miniflux")
		go fetchAndPublish(log, miniflux, stateDB, mqttClient)
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to trigger topic")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Miniflux source primitive ready — waiting for trigger")

	// Re-publish pending articles from a previous incomplete run
	pending, err := stateDB.PendingArticles()
	if err != nil {
		log.WithError(err).Warn("Failed to query pending articles")
	} else if len(pending) > 0 {
		log.WithField("count", len(pending)).Info("Re-publishing pending articles from previous run")
		for _, a := range pending {
			msg := mqttclient.RawArticle{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: a.ArticleID,
					Source:    "miniflux",
					Timestamp: time.Now(),
				},
				URL:   a.URL,
				Title: a.Title,
			}
			if err := mqttClient.Publish(mqttclient.TopicArticlesRaw, msg); err != nil {
				log.WithError(err).WithField("url", a.URL).Warn("Failed to re-publish pending article")
			}
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down Miniflux source primitive")
}

func fetchAndPublish(log *logrus.Logger, miniflux *services.Miniflux, stateDB *state.DB, mqttClient *mqttclient.Client) {
	items, err := miniflux.GetStarredEntries()
	if err != nil {
		log.WithError(err).Error("Failed to fetch starred entries from Miniflux")
		return
	}

	log.WithField("count", len(items)).Info("Fetched starred entries from Miniflux")

	published := 0
	for _, item := range items {
		url := item.URL
		if url == "" {
			continue
		}

		complete, err := stateDB.IsComplete(url)
		if err != nil {
			log.WithError(err).WithField("url", url).Warn("Failed to check completion state")
			continue
		}
		if complete {
			log.WithField("url", url).Debug("Article already completed, skipping")
			continue
		}

		articleID := state.ArticleID(url)

		msg := mqttclient.RawArticle{
			Envelope: mqttclient.Envelope{
				MessageID: generateID(),
				ArticleID: articleID,
				Source:    "miniflux",
				Timestamp: time.Now(),
			},
			URL:   url,
			Title: item.Title,
		}

		if err := mqttClient.Publish(mqttclient.TopicArticlesRaw, msg); err != nil {
			log.WithError(err).WithField("url", url).Error("Failed to publish article")
			continue
		}

		if err := stateDB.MarkPublished(url, articleID, item.Title); err != nil {
			log.WithError(err).WithField("url", url).Warn("Failed to mark article as published")
		}

		published++
		log.WithFields(logrus.Fields{
			"url":        url,
			"article_id": articleID,
			"title":      item.Title,
		}).Debug("Published article to bus")
	}

	log.WithField("published", published).Info("Finished publishing articles to bus")
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
