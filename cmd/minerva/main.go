package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/saaga/minerva/internal/config"
	"github.com/saaga/minerva/internal/database"
	"github.com/saaga/minerva/internal/pipeline"
	"github.com/saaga/minerva/internal/services"
	"github.com/saaga/minerva/pkg/logger"
)

func main() {
	var configPath = flag.String("config", "", "Path to configuration file")
	var dryRun = flag.Bool("dry-run", false, "Run without making external requests (development mode)")

	flag.Parse()

	// Initialize logger
	log := logger.New()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.WithError(err).Fatal("Failed to load configuration")
	}

	// Set log level from config
	logger.SetLevel(cfg.Log.Level)

	log.WithField("version", cfg.App.Version).Info("Starting Minerva")

	// Initialize database
	db, err := database.New(cfg.Database.Path)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize database")
	}
	defer db.Close()

	// Initialize services
	freshRSS := services.NewFreshRSS(cfg.FreshRSS)
	extractor := services.NewContentExtractor(cfg.Extractor)
	ollama := services.NewOllama(cfg.Ollama)
	searx := services.NewSearXNG(cfg.SearXNG)
	openLibrary := services.NewOpenLibrary(cfg.OpenLibrary)
	koha := services.NewKoha(cfg.Koha)
	ntfy := services.NewNtfy(cfg.Ntfy)

	// Initialize pipeline
	p := pipeline.New(pipeline.Config{
		FreshRSS:    freshRSS,
		Extractor:   extractor,
		Ollama:      ollama,
		SearXNG:     searx,
		OpenLibrary: openLibrary,
		Koha:        koha,
		Ntfy:        ntfy,
		Database:    db,
		DryRun:      *dryRun,
		DebugOllama: cfg.App.DebugOllama,
		Logger:      log,
	})
	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info("Received shutdown signal")
		cancel()
	}()

	// Run the pipeline
	if err := p.Run(ctx); err != nil {
		log.WithError(err).Error("Pipeline execution failed")
		os.Exit(1)
	}

	log.Info("Minerva completed successfully")
}
