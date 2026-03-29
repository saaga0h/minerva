package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	mqttclient "github.com/saaga0h/minerva/internal/mqtt"
	"github.com/saaga0h/minerva/internal/store"
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

	lookbackHours := getEnvInt("CONSOLIDATOR_LOOKBACK_HOURS", 24)
	dedupHours := getEnvInt("CONSOLIDATOR_DEDUP_HOURS", 20)
	minScore := getEnvFloat("CONSOLIDATOR_MIN_SCORE", 0.50)
	topN := getEnvInt("CONSOLIDATOR_TOP_N", 1)

	log.WithFields(logrus.Fields{
		"lookback_hours": lookbackHours,
		"dedup_hours":    dedupHours,
		"min_score":      minScore,
		"top_n":          topN,
	}).Info("consolidator: starting")

	ctx := context.Background()

	db, err := store.New(ctx, cfg.Store.DSN)
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to knowledge base (PostgreSQL)")
	}
	defer db.Close()

	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	mqttClient, err := mqttclient.NewClient(mqttclient.ClientConfig{
		BrokerURL: brokerURL,
		ClientID:  getEnv("MQTT_CLIENT_ID", "minerva-consolidator"),
		Username:  getEnv("MQTT_USER", ""),
		Password:  getEnv("MQTT_PASSWORD", ""),
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()

	published, err := consolidate(ctx, db, mqttClient, consolidateConfig{
		lookbackHours: lookbackHours,
		dedupHours:    dedupHours,
		minScore:      float32(minScore),
		topN:          topN,
	}, log)
	if err != nil {
		log.WithError(err).Fatal("consolidator: failed")
	}

	if published == 0 {
		log.Info("consolidator: nothing cleared threshold or all deduplicated — no notification sent")
	} else {
		log.WithField("published", published).Info("consolidator: done")
	}
}

type consolidateConfig struct {
	lookbackHours int
	dedupHours    int
	minScore      float32
	topN          int
}

func consolidate(ctx context.Context, db *store.DB, mqttClient *mqttclient.Client, cfg consolidateConfig, log *logrus.Logger) (int, error) {
	// Fetch topN*3 candidates so we have headroom after dedup filtering.
	works, _, err := db.AggregateSessionScores(ctx, cfg.lookbackHours, cfg.topN*3)
	if err != nil {
		return 0, fmt.Errorf("aggregate session scores: %w", err)
	}

	if len(works) == 0 {
		log.Info("consolidator: no brief sessions found in lookback window")
		return 0, nil
	}

	published := 0
	for _, w := range works {
		if published >= cfg.topN {
			break
		}
		if w.Score < cfg.minScore {
			log.WithFields(logrus.Fields{
				"work_id": w.WorkID,
				"title":   w.Title,
				"score":   w.Score,
			}).Debug("consolidator: score below threshold — stopping")
			break
		}

		already, err := db.IsAlreadySurfaced(ctx, w.WorkID, w.ArticleID, cfg.dedupHours)
		if err != nil {
			log.WithError(err).WithField("work_id", w.WorkID).Warn("consolidator: dedup check failed — skipping")
			continue
		}
		if already {
			log.WithFields(logrus.Fields{
				"work_id": w.WorkID,
				"title":   w.Title,
			}).Debug("consolidator: already surfaced within dedup window — skipping")
			continue
		}

		digest := mqttclient.ConsolidatorDigest{
			SessionID:    w.SessionID,
			WorkID:       w.WorkID,
			WorkType:     w.WorkType,
			Title:        w.Title,
			Authors:      w.Authors,
			DOI:          w.DOI,
			ArXivID:      w.ArXivID,
			ISBN13:       w.ISBN13,
			PublishYear:  w.PublishYear,
			ArticleID:    w.ArticleID,
			ArticleURL:   w.ArticleURL,
			ArticleTitle: w.ArticleTitle,
			Score:        w.Score,
			SurfacedAt:   time.Now(),
		}

		if err := mqttClient.Publish(mqttclient.TopicConsolidatorDigest, digest); err != nil {
			log.WithError(err).WithField("work_id", w.WorkID).Warn("consolidator: failed to publish digest — skipping")
			continue
		}

		if err := db.RecordSurfaced(ctx, w.WorkID, w.ArticleID, w.SessionID, w.Score); err != nil {
			log.WithError(err).WithField("work_id", w.WorkID).Warn("consolidator: failed to record surfaced — dedup may not apply next run")
		}

		log.WithFields(logrus.Fields{
			"work_id":  w.WorkID,
			"title":    w.Title,
			"score":    w.Score,
			"via":      w.ArticleTitle,
		}).Info("consolidator: published digest")

		published++
	}

	return published, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultValue
}
