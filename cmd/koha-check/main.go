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

	// Subscribe to book candidates — check each against Koha catalog
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
				"article_id":     msg.ArticleID,
				"books_to_check": len(msg.Books),
			}).Debug("Checking books against Koha catalog")

			var newBooks []mqttclient.BookCandidate
			var ownedBooks []mqttclient.OwnedBook

			for _, book := range msg.Books {
				isbn := book.ISBN13
				if isbn == "" {
					isbn = book.ISBN
				}

				owned, kohaRecord, err := koha.CheckOwnership(isbn, book.Title, book.Author)
				if err != nil {
					log.WithError(err).WithField("title", book.Title).Warn("Koha check failed — treating as new book")
					newBooks = append(newBooks, book)
					continue
				}

				if owned && kohaRecord != nil {
					ownedBooks = append(ownedBooks, mqttclient.OwnedBook{
						Title:  kohaRecord.Title,
						Author: kohaRecord.Author,
						KohaID: fmt.Sprintf("%d", kohaRecord.BiblioID),
					})
					log.WithFields(logrus.Fields{
						"title":   book.Title,
						"koha_id": kohaRecord.BiblioID,
					}).Info("Book found in Koha catalog")
				} else {
					newBooks = append(newBooks, book)
				}
			}

			out := mqttclient.CheckedBooks{
				Envelope: mqttclient.Envelope{
					MessageID: generateID(),
					ArticleID: msg.ArticleID,
					Source:    msg.Source,
					Timestamp: time.Now(),
				},
				ArticleTitle: msg.ArticleTitle,
				ArticleURL:   msg.ArticleURL,
				NewBooks:     newBooks,
				OwnedBooks:   ownedBooks,
			}

			if err := mqttClient.Publish(mqttclient.TopicBooksChecked, out); err != nil {
				log.WithError(err).WithField("article_id", msg.ArticleID).Error("Failed to publish CheckedBooks")
				return
			}

			log.WithFields(logrus.Fields{
				"article_id": msg.ArticleID,
				"new_books":  len(newBooks),
				"owned":      len(ownedBooks),
			}).Debug("Published checked books")
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to books/candidates")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Koha-check primitive ready — listening for book candidates")

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
