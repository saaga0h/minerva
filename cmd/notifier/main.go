package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

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

	ctx := context.Background()

	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-notifier")
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

	ntfyClient := services.NewNtfy(cfg.Ntfy)
	ntfyClient.SetLogger(log)

	// Consolidator digest — the main notification path.
	if err := mqttClient.Subscribe(mqttclient.TopicConsolidatorDigest, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			var msg mqttclient.ConsolidatorDigest
			if err := json.Unmarshal(data, &msg); err != nil {
				log.WithError(err).Warn("notifier: failed to unmarshal ConsolidatorDigest")
				return
			}
			handleConsolidatorDigest(ctx, msg, ntfyClient, cfg.Ntfy.Topic, log)
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to consolidator/digest")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
	}).Info("Notifier primitive ready — listening for consolidator digest")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down notifier primitive")
}

func handleConsolidatorDigest(ctx context.Context, msg mqttclient.ConsolidatorDigest, ntfy *services.Ntfy, topic string, log *logrus.Logger) {
	title := "Minerva: " + msg.Title

	var sb strings.Builder
	if msg.WorkType != "" {
		sb.WriteString(fmt.Sprintf("Type: %s", msg.WorkType))
		if msg.PublishYear > 0 {
			sb.WriteString(fmt.Sprintf(" (%d)", msg.PublishYear))
		}
		sb.WriteString("\n")
	}
	if msg.Authors != "" {
		sb.WriteString(fmt.Sprintf("Authors: %s\n", msg.Authors))
	}
	if msg.DOI != "" {
		sb.WriteString(fmt.Sprintf("DOI: https://doi.org/%s\n", msg.DOI))
	} else if msg.ArXivID != "" {
		sb.WriteString(fmt.Sprintf("arXiv: https://arxiv.org/abs/%s\n", msg.ArXivID))
	}
	// Click URL: DOI → arXiv → ISBN search fallback
	clickURL := ""
	if msg.DOI != "" {
		clickURL = "https://doi.org/" + msg.DOI
	} else if msg.ArXivID != "" {
		clickURL = "https://arxiv.org/abs/" + msg.ArXivID
	} else if msg.ISBN13 != "" {
		clickURL = "https://openlibrary.org/isbn/" + msg.ISBN13
	}

	priority := "default"
	if msg.Score > 0.55 {
		priority = "high"
	}

	tags := []string{"minerva"}
	if msg.WorkType != "" {
		tags = append(tags, msg.WorkType)
	}

	notification := services.Notification{
		Topic:    topic,
		Title:    title,
		Message:  strings.TrimSpace(sb.String()),
		Priority: priority,
		Tags:     tags,
		Click:    clickURL,
	}

	if err := ntfy.Send(ctx, notification); err != nil {
		log.WithError(err).WithField("work_id", msg.WorkID).Warn("notifier: failed to send ntfy notification")
		return
	}

	log.WithFields(logrus.Fields{
		"work_id":  msg.WorkID,
		"title":    msg.Title,
		"score":    msg.Score,
		"priority": priority,
	}).Info("notifier: ConsolidatorDigest delivered")
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
