package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/saaga0h/minerva/internal/config"
	"github.com/saaga0h/minerva/internal/forge"
	"github.com/saaga0h/minerva/internal/services"
	"github.com/saaga0h/minerva/pkg/logger"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
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

	if cfg.Store.DSN == "" {
		log.Fatal("STORE_DSN / DB_* vars required")
	}

	ctx := context.Background()

	// Build pool with pgvector codec.
	poolCfg, err := pgxpool.ParseConfig(cfg.Store.DSN)
	if err != nil {
		log.WithError(err).Fatal("Failed to parse DSN")
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.WithError(err).Fatal("Failed to create connection pool")
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.WithError(err).Fatal("Cannot reach PostgreSQL")
	}

	// ForgeClient for embedding.
	ollama := services.NewOllama(cfg.Ollama)
	ollama.SetLogger(log)
	forgeClient, err := forge.New(cfg.Forge, forge.BrokerConfig{
		BrokerURL: getEnv("MQTT_BROKER_URL", "tcp://localhost:1883"),
		ClientID:  "forge-reembed-works",
		Username:  getEnv("MQTT_USER", ""),
		Password:  getEnv("MQTT_PASSWORD", ""),
	}, ollama, log)
	if err != nil {
		log.WithError(err).Fatal("Failed to create Forge client")
	}
	defer forgeClient.Disconnect()

	// Count works needing embeddings.
	var total int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM works WHERE embedding IS NULL`).Scan(&total); err != nil {
		log.WithError(err).Fatal("Failed to count works")
	}
	log.WithField("count", total).Info("Works with null embedding")

	if total == 0 {
		log.Info("Nothing to do — all works have embeddings")
		return
	}

	// Query works without embeddings ordered by first_seen_at.
	rows, err := pool.Query(ctx,
		`SELECT work_id, title, COALESCE(abstract, '') FROM works WHERE embedding IS NULL ORDER BY first_seen_at`,
	)
	if err != nil {
		log.WithError(err).Fatal("Failed to query works")
	}
	defer rows.Close()

	done, failed := 0, 0
	for rows.Next() {
		var workID int64
		var title, abstract string
		if err := rows.Scan(&workID, &title, &abstract); err != nil {
			log.WithError(err).Warn("Scan failed — skipping row")
			failed++
			continue
		}

		text := title
		if abstract != "" {
			text = title + " " + abstract
		}

		embedding, embedErr := forgeClient.Embed(text)
		if embedErr != nil {
			log.WithError(embedErr).WithField("work_id", workID).Warn("Embed failed — skipping")
			failed++
			continue
		}

		_, updateErr := pool.Exec(ctx,
			`UPDATE works SET embedding = $1 WHERE work_id = $2`,
			pgvector.NewVector(embedding),
			workID,
		)
		if updateErr != nil {
			log.WithError(updateErr).WithField("work_id", workID).Warn("Update failed — skipping")
			failed++
			continue
		}

		done++
		if done%100 == 0 {
			log.WithFields(map[string]any{
				"done":      done,
				"failed":    failed,
				"remaining": total - done - failed,
			}).Info("Progress")
		}
	}
	if err := rows.Err(); err != nil {
		log.WithError(err).Error("Row iteration error")
	}

	fmt.Printf("Done: %d embedded, %d failed, %d total\n", done, failed, total)
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
