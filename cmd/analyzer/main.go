package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
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

	debugOllama := getEnv("DEBUG_OLLAMA", "false") == "true"

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-analyzer")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Ollama service
	ollama := services.NewOllama(cfg.Ollama)
	ollama.SetLogger(log)

	var ollamaMu sync.Mutex

	// Subscribe to extracted articles — run multi-pass LLM analysis
	if err := mqttClient.Subscribe(mqttclient.TopicArticlesExtracted, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.ExtractedArticle
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal ExtractedArticle")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"title":      msg.Title,
			}).Debug("Running Ollama multi-pass analysis")

			// Use article_id as a numeric-ish identifier for debug file naming
			debugID := 0
			ollamaMu.Lock()
			multiPass, err := ollama.ProcessContentMultiPass(msg.Title, msg.Content, debugID, debugOllama)
			ollamaMu.Unlock()
			if err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Warn("Ollama analysis failed — article dropped for this run")
				return
			}

			// Build keywords from phenomena + concepts + related topics
			var keywords []string
			keywords = append(keywords, multiPass.Pass2.Phenomena...)
			keywords = append(keywords, multiPass.Pass3.Concepts...)
			keywords = append(keywords, multiPass.Pass3.RelatedTopics...)

			// Deduplicate
			seen := make(map[string]bool)
			var uniqueKeywords []string
			for _, kw := range keywords {
				lower := strings.ToLower(kw)
				if !seen[lower] {
					seen[lower] = true
					uniqueKeywords = append(uniqueKeywords, kw)
				}
			}

			insights := fmt.Sprintf("Domain: %s. Related: %s",
				multiPass.Pass1.Domain,
				strings.Join(multiPass.Pass3.RelatedTopics, ", "),
			)

			out := mqttclient.AnalyzedArticle{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    msg.Source,
					SourceID:  msg.SourceID,
					Timestamp: time.Now(),
				},
				URL:           msg.URL,
				Title:         msg.Title,
				Domain:        multiPass.Pass1.Domain,
				ArticleType:   multiPass.Pass1.Type,
				Summary:       multiPass.Pass1.Topic,
				Keywords:      uniqueKeywords,
				Concepts:      multiPass.Pass3.Concepts,
				RelatedTopics: multiPass.Pass3.RelatedTopics,
				Entities: mqttclient.ArticleEntities{
					Facilities: multiPass.Pass2.Facilities,
					People:     multiPass.Pass2.People,
					Locations:  multiPass.Pass2.Locations,
					Phenomena:  multiPass.Pass2.Phenomena,
				},
				Insights: insights,
			}

			if err := mqttClient.Publish(mqttclient.TopicArticlesAnalyzed, out); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Error("Failed to publish AnalyzedArticle")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":     msg.ArticleID,
				"domain":         multiPass.Pass1.Domain,
				"keywords_count": len(uniqueKeywords),
			}).Debug("Published analyzed article")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to articles/extracted")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Analyzer primitive ready — listening for extracted articles")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down analyzer primitive")
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
