package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/pkg/logger"
)

// Notifier is currently a stub. The planned architecture is:
//
//	consolidator → notifier
//
// The consolidator (not yet implemented) will aggregate completed pipeline
// results from Postgres and publish a structured digest message. The notifier
// will receive that message and deliver it via ntfy (or other channels).
//
// Until the consolidator exists, digest triggers are acknowledged and logged
// but no notification is sent. SQLite has been removed — Postgres is the
// sole persistence layer going forward.
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

	// Subscribe to digest trigger — no-op until consolidator is implemented.
	// Future: consolidator publishes a structured digest message here;
	// notifier receives it and delivers via ntfy/other channels.
	if err := mqttClient.Subscribe(mqttclient.TopicPipelineDigest, func(_ []byte) {
		log.Info("Digest trigger received — notifier stub, no-op until consolidator is implemented")
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to pipeline/digest")
	}

	log.WithField("broker", brokerURL).Info("Notifier primitive ready (stub) — waiting for digest trigger")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down notifier primitive")
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
