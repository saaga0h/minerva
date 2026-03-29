package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
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

	// PostgreSQL — fatal if unavailable: brief has no purpose without it.
	ctx := context.Background()
	db, err := store.New(ctx, cfg.Store.DSN)
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to knowledge base (PostgreSQL)")
	}
	defer db.Close()

	log.Info("Connected to knowledge base")

	// MQTT client
	brokerURL := getEnv("MQTT_BROKER_URL", "tcp://localhost:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "minerva-brief")
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

	topK := cfg.Brief.TopK
	if topK <= 0 {
		topK = 5
	}

	// Subscribe to Journal's brief queries.
	if err := mqttClient.Subscribe(mqttclient.TopicQueryBrief, func(payload []byte) {
		data := make([]byte, len(payload))
		copy(data, payload)
		go func() {
			handleBriefQuery(ctx, data, db, mqttClient, topK, cfg.Brief.MinScore, log)
		}()
	}); err != nil {
		log.WithError(err).Fatal("Failed to subscribe to query/brief")
	}

	log.WithFields(logrus.Fields{
		"broker":    brokerURL,
		"client_id": clientID,
		"top_k":     topK,
	}).Info("brief primitive ready — listening for Journal queries")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("Shutting down brief primitive")
}

// handleBriefQuery processes a single BriefQuery and publishes a BriefResponse + BriefResult.
// Always publishes a response (even empty) — Journal times out after 30s.
func handleBriefQuery(
	ctx context.Context,
	data []byte,
	db *store.DB,
	mqttClient *mqttclient.Client,
	topK int,
	minScore float64,
	log *logrus.Logger,
) {
	var query mqttclient.BriefQuery
	if err := json.Unmarshal(data, &query); err != nil {
		log.WithError(err).Warn("brief: failed to unmarshal BriefQuery")
		return
	}

	k := query.TopK
	if k <= 0 {
		k = topK
	}

	log.WithFields(logrus.Fields{
		"session_id":       query.SessionID,
		"has_trend_embeds": len(query.TrendEmbeddings) > 0,
		"top_k":            k,
	}).Debug("brief: handling query")

	var articles []mqttclient.BriefArticle
	var works []mqttclient.BriefWork

	if len(query.TrendEmbeddings) > 0 {
		articles, works = vectorSearch(ctx, db, query, k, log)
	} else {
		articles, works = keywordSearch(ctx, db, query, k, log)
	}

	now := time.Now()

	// Publish response to Journal.
	response := mqttclient.BriefResponse{
		SessionID: query.SessionID,
		Articles:  articles,
		Works:     works,
	}

	responseTopic := query.ResponseTopic
	if responseTopic == "" {
		responseTopic = "minerva/brief/response"
	}

	if err := mqttClient.Publish(responseTopic, response); err != nil {
		log.WithError(err).WithField("session_id", query.SessionID).Warn("brief: failed to publish BriefResponse")
	}

	result := mqttclient.BriefResult{
		SessionID: query.SessionID,
		Articles:  articles,
		Works:     works,
		QueriedAt: now,
	}

	if err := mqttClient.Publish(mqttclient.TopicBriefResult, result); err != nil {
		log.WithError(err).WithField("session_id", query.SessionID).Warn("brief: failed to publish BriefResult")
	}

	log.WithFields(logrus.Fields{
		"session_id":     query.SessionID,
		"articles":       len(articles),
		"works":          len(works),
		"response_topic": responseTopic,
	}).Debug("brief: query handled")
}

// vectorSearch implements Phase 2: parallel ANN over trend + unexpected embeddings
// against both articles and works tables. Returns top-scored articles and works.
func vectorSearch(
	ctx context.Context,
	db *store.DB,
	query mqttclient.BriefQuery,
	topK int,
	log *logrus.Logger,
) ([]mqttclient.BriefArticle, []mqttclient.BriefWork) {
	soulSpeed := query.SoulSpeed
	trendWeight := float32(1.0) - soulSpeed*0.4
	unexpWeight := soulSpeed * 0.4

	type articleCandidate struct {
		articleID    string
		url          string
		title        string
		trendScore   float32
		unexpScore   float32
	}
	type workCandidate struct {
		r          store.WorkSearchResult
		trendScore float32
		unexpScore float32
	}

	var mu sync.Mutex
	articleMap := make(map[string]*articleCandidate)
	workMap := make(map[int]*workCandidate)

	var wg sync.WaitGroup

	// Search articles — trend embeddings
	for _, vec := range query.TrendEmbeddings {
		vec := vec
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := db.SearchByEmbedding(ctx, vec, topK)
			if err != nil {
				log.WithError(err).Warn("brief: article trend embedding search failed")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, r := range results {
				sim := float32(1.0) - r.Distance
				if c, ok := articleMap[r.ArticleID]; ok {
					if sim > c.trendScore {
						c.trendScore = sim
					}
				} else {
					articleMap[r.ArticleID] = &articleCandidate{
						articleID:  r.ArticleID,
						url:        r.URL,
						title:      r.Title,
						trendScore: sim,
					}
				}
			}
		}()
	}

	// Search articles — unexpected embeddings
	for _, vec := range query.UnexpectedEmbeddings {
		vec := vec
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := db.SearchByEmbedding(ctx, vec, topK)
			if err != nil {
				log.WithError(err).Warn("brief: article unexpected embedding search failed")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, r := range results {
				sim := float32(1.0) - r.Distance
				if c, ok := articleMap[r.ArticleID]; ok {
					if sim > c.unexpScore {
						c.unexpScore = sim
					}
				} else {
					articleMap[r.ArticleID] = &articleCandidate{
						articleID: r.ArticleID,
						url:       r.URL,
						title:     r.Title,
						unexpScore: sim,
					}
				}
			}
		}()
	}

	// Search works — trend embeddings
	for _, vec := range query.TrendEmbeddings {
		vec := vec
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := db.SearchWorksByEmbedding(ctx, vec, topK)
			if err != nil {
				log.WithError(err).Warn("brief: works trend embedding search failed")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, r := range results {
				sim := float32(1.0) - r.Distance
				if c, ok := workMap[r.WorkID]; ok {
					if sim > c.trendScore {
						c.trendScore = sim
					}
				} else {
					rc := r
					workMap[r.WorkID] = &workCandidate{r: rc, trendScore: sim}
				}
			}
		}()
	}

	// Search works — unexpected embeddings
	for _, vec := range query.UnexpectedEmbeddings {
		vec := vec
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := db.SearchWorksByEmbedding(ctx, vec, topK)
			if err != nil {
				log.WithError(err).Warn("brief: works unexpected embedding search failed")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, r := range results {
				sim := float32(1.0) - r.Distance
				if c, ok := workMap[r.WorkID]; ok {
					if sim > c.unexpScore {
						c.unexpScore = sim
					}
				} else {
					rc := r
					workMap[r.WorkID] = &workCandidate{r: rc, unexpScore: sim}
				}
			}
		}()
	}

	wg.Wait()

	// Build and rank article results.
	articles := make([]mqttclient.BriefArticle, 0, len(articleMap))
	for _, c := range articleMap {
		blended := c.trendScore*trendWeight + c.unexpScore*unexpWeight
		articles = append(articles, mqttclient.BriefArticle{
			ArticleID: c.articleID,
			URL:       c.url,
			Title:     c.title,
			Score:     blended,
		})
	}
	sort.Slice(articles, func(i, j int) bool { return articles[i].Score > articles[j].Score })
	if len(articles) > topK {
		articles = articles[:topK]
	}

	// Build and rank work results.
	works := make([]mqttclient.BriefWork, 0, len(workMap))
	for _, c := range workMap {
		blended := c.trendScore*trendWeight + c.unexpScore*unexpWeight
		works = append(works, mqttclient.BriefWork{
			WorkID:      c.r.WorkID,
			WorkType:    c.r.WorkType,
			Title:       c.r.Title,
			Authors:     c.r.Authors,
			DOI:         c.r.DOI,
			ArXivID:     c.r.ArXivID,
			ISBN13:      c.r.ISBN13,
			PublishYear: c.r.PublishYear,
			Score:       blended,
		})
	}
	sort.Slice(works, func(i, j int) bool { return works[i].Score > works[j].Score })
	if len(works) > topK {
		works = works[:topK]
	}

	return articles, works
}

// keywordSearch implements Phase 1: full-text search weighted by ManifoldProfile proximity.
// Returns matched articles ranked by score; works list is empty in keyword mode.
func keywordSearch(
	ctx context.Context,
	db *store.DB,
	query mqttclient.BriefQuery,
	topK int,
	log *logrus.Logger,
) ([]mqttclient.BriefArticle, []mqttclient.BriefWork) {
	if len(query.ManifoldProfile) == 0 {
		return nil, nil
	}

	type slugProximity struct {
		slug      string
		proximity float32
	}
	slugs := make([]slugProximity, 0, len(query.ManifoldProfile))
	for slug, prox := range query.ManifoldProfile {
		slugs = append(slugs, slugProximity{slug: slug, proximity: prox})
	}
	sort.Slice(slugs, func(i, j int) bool {
		return slugs[i].proximity > slugs[j].proximity
	})

	type candidate struct {
		articleID string
		url       string
		title     string
		score     float32
	}

	scores := make(map[string]*candidate)

	for _, sp := range slugs {
		results, err := db.SearchByKeywords(ctx, sp.slug, topK)
		if err != nil {
			log.WithError(err).WithField("slug", sp.slug).Warn("brief: keyword search failed")
			continue
		}
		for _, r := range results {
			similarity := float32(1.0) - r.Distance
			weighted := similarity * sp.proximity
			if existing, ok := scores[r.ArticleID]; ok {
				if weighted > existing.score {
					existing.score = weighted
				}
			} else {
				scores[r.ArticleID] = &candidate{
					articleID: r.ArticleID,
					url:       r.URL,
					title:     r.Title,
					score:     weighted,
				}
			}
		}
	}

	articles := make([]mqttclient.BriefArticle, 0, len(scores))
	for _, c := range scores {
		articles = append(articles, mqttclient.BriefArticle{
			ArticleID: c.articleID,
			URL:       c.url,
			Title:     c.title,
			Score:     c.score,
		})
	}
	sort.Slice(articles, func(i, j int) bool { return articles[i].Score > articles[j].Score })
	if len(articles) > topK {
		articles = articles[:topK]
	}

	return articles, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// generateID returns a hex timestamp string used as a message ID.
func generateID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
