package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	"github.com/saaga0h/minerva/internal/database"
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

	// Open recommendations DB read-only (storage primitive owns all writes)
	db, err := database.New(cfg.Database.Path)
	if err != nil {
		log.WithError(err).Fatal("Failed to open recommendations DB")
	}
	defer db.Close()

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-notifier")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  clientID,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()
	mqttClient.SetLogger(log)

	// Ntfy service
	ntfy := services.NewNtfy(cfg.Ntfy)
	ntfy.SetLogger(log)

	// digestHours controls how far back the digest window reaches
	digestHours := getEnvDuration("NOTIFIER_DIGEST_HOURS", 24*time.Hour)

	// Subscribe to digest trigger — query DB and send ntfy notification
	if err := mqttClient.Subscribe(mqttclient.TopicPipelineDigest, func(_ []byte) {
		log.Info("Digest trigger received — building recommendation digest")
		go func() {
			since := time.Now().Add(-digestHours)
			entries, err := db.GetRecommendationsSince(since)
			if err != nil {
				log.WithError(err).Error("Failed to query recommendations for digest")
				return
			}

			log.WithFields(logrus.Fields{
				"entries": len(entries),
				"since":   since.Format(time.RFC3339),
			}).Info("Sending digest notification")

			if len(entries) == 0 {
				notification := services.Notification{
					Topic:   cfg.Ntfy.Topic,
					Title:   "Minerva digest — no new recommendations",
					Message: fmt.Sprintf("No new book recommendations in the past %s.", formatDuration(digestHours)),
				}
				ntfy.Send(context.Background(), notification) //nolint:errcheck
				return
			}

			msg := buildDigestMessage(entries)
			newCount := 0
			for _, e := range entries {
				if !e.OwnedInKoha {
					newCount++
				}
			}

			notification := services.Notification{
				Topic:    cfg.Ntfy.Topic,
				Title:    fmt.Sprintf("Minerva digest — %d new recommendations", newCount),
				Message:  msg,
				Tags:     []string{"minerva", "books"},
				Priority: cfg.Ntfy.Priority,
			}

			if err := ntfy.Send(context.Background(), notification); err != nil {
				log.WithError(err).Warn("Failed to send digest notification")
			}
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to pipeline/digest")
	}

	log.WithFields(logrus.Fields{
		"broker":       brokerURL,
		"client_id":    clientID,
		"digest_hours": digestHours.Hours(),
	}).Info("Notifier primitive ready — waiting for digest trigger")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down notifier primitive")
}

// buildDigestMessage formats DigestEntry slice into a human-readable ntfy message body.
func buildDigestMessage(entries []database.DigestEntry) string {
	// Group by article URL
	type articleGroup struct {
		title string
		url   string
		books []database.DigestEntry
	}

	seen := make(map[string]int)
	var groups []articleGroup

	for _, e := range entries {
		idx, ok := seen[e.ArticleURL]
		if !ok {
			idx = len(groups)
			seen[e.ArticleURL] = idx
			groups = append(groups, articleGroup{title: e.ArticleTitle, url: e.ArticleURL})
		}
		groups[idx].books = append(groups[idx].books, e)
	}

	var sb strings.Builder
	for _, g := range groups {
		sb.WriteString(fmt.Sprintf("📰 %s\n", g.title))
		for _, b := range g.books {
			owned := ""
			if b.OwnedInKoha {
				owned = " ✅"
			}
			sb.WriteString(fmt.Sprintf("  • %s — %s%s\n", b.BookTitle, b.BookAuthor, owned))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// getEnvDuration reads an env var as a number of hours (integer) and returns a time.Duration.
func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	var hours float64
	if _, err := fmt.Sscanf(v, "%f", &hours); err != nil || hours <= 0 {
		return defaultVal
	}
	return time.Duration(hours * float64(time.Hour))
}

func formatDuration(d time.Duration) string {
	h := d.Hours()
	if h < 1 {
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	}
	if h == float64(int(h)) {
		return fmt.Sprintf("%.0f hours", h)
	}
	return fmt.Sprintf("%.1f hours", h)
}

func generateID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
