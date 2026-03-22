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
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-koha-check")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Koha service
	koha := services.NewKoha(cfg.Koha)
	koha.SetLogger(log)

	// Subscribe to work candidates — check books against Koha, pass through non-books
	if err := mqttClient.Subscribe(mqttclient.TopicWorksCandidates, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.WorkCandidates
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("Failed to unmarshal WorkCandidates")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id":     msg.ArticleID,
				"works_to_check": len(msg.Works),
			}).Debug("Checking works against Koha catalog")

			var newWorks []mqttclient.WorkCandidate
			var ownedWorks []mqttclient.OwnedWork

			for _, work := range msg.Works {
				// Only books are checked against Koha; papers pass through unchecked
				if work.WorkType != "book" {
					newWorks = append(newWorks, work)
					continue
				}

				isbn := work.ISBN13
				if isbn == "" {
					isbn = work.ISBN
				}

				// Skip Koha lookup for books without an ISBN — title-only searches
				// are unreliable and Koha won't have papers or ISBNless entries anyway.
				if isbn == "" {
					newWorks = append(newWorks, work)
					continue
				}

				owned, kohaRecord, err := koha.CheckOwnership(isbn)
				if err != nil {
					log.WithError(err).WithField("title", work.Title).Warn("Koha check failed — treating as new work")
					newWorks = append(newWorks, work)
					continue
				}

				if owned && kohaRecord != nil {
					ownedWorks = append(ownedWorks, mqttclient.OwnedWork{
						Title:  kohaRecord.Title,
						Author: kohaRecord.Author,
						KohaID: fmt.Sprintf("%d", kohaRecord.BiblioID),
					})
					log.WithFields(logrus.Fields{
						"title":   work.Title,
						"koha_id": kohaRecord.BiblioID,
					}).Info("Book found in Koha catalog")
				} else {
					newWorks = append(newWorks, work)
				}
			}

			out := mqttclient.CheckedWorks{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    msg.Source,
					Timestamp: time.Now(),
				},
				ArticleTitle: msg.ArticleTitle,
				ArticleURL:   msg.ArticleURL,
				NewWorks:     newWorks,
				OwnedWorks:   ownedWorks,
			}

			if err := mqttClient.Publish(mqttclient.TopicWorksChecked, out); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Error("Failed to publish CheckedWorks")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"new_works":  len(newWorks),
				"owned":      len(ownedWorks),
			}).Debug("Published checked works")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to works/candidates")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Koha-check primitive ready — listening for work candidates")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down koha-check primitive")
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
