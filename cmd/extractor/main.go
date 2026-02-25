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
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-extractor")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Extractor service
	extractor := services.NewContentExtractor(cfg.Extractor)
	extractor.SetLogger(log)

	// Subscribe to raw articles — extract content and publish downstream
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesRaw, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.RawArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal RawArticle")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"url":        msg.URL,
			}).Debug("Extracting content")

			content, err := extractor.ExtractContent(msg.URL)
			if err != nil {
				log.WithError(err).WithField("url", msg.URL).Warn("Extraction failed — article dropped for this run")
				return
			}

			// Use extracted title if available and the original title was empty
			title := msg.Title
			if title == "" && content.Title != "" {
				title = content.Title
			}

			out := mqttclient.ExtractedArticle{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    msg.Source,
					Timestamp: time.Now(),
				},
				URL:     msg.URL,
				Title:   title,
				Content: content.Content,
			}

			if err := mqttClient.Publish(mqttclient.TopicArticlesExtracted, out); err != nil {
				log.WithError(err).WithField("url", msg.URL).Error("Failed to publish ExtractedArticle")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":     msg.ArticleID,
				"content_length": len(content.Content),
			}).Debug("Published extracted article")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/raw")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Extractor primitive ready — listening for articles")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down extractor primitive")
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
