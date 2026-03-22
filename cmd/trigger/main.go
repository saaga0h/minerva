package main

import (
	"flag"
	"os"
	"time"

	"github.com/joho/godotenv"
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/pkg/logger"
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

	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  "minerva-trigger",
		Username:  getEnv("MQTT_USER", ""),
		Password:  getEnv("MQTT_PASSWORD", ""),
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()

	if err := mqttClient.Publish(mqttclient.TopicPipelineTrigger, map[string]any{
		"triggered_at": time.Now().UTC(),
	}); err != nil {
		log.WithError(err).Fatal("Failed to publish trigger")
	}

	log.WithField("broker", brokerURL).Info("Pipeline trigger fired")
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
