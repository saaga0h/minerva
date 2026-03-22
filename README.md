# Minerva - Intelligent Content Curation Pipeline

Minerva transforms RSS feed items into book recommendations by orchestrating multiple REST APIs and AI services. It is a distributed system of independent MQTT primitives connected through a Mosquitto broker — not a monolith.

## What Minerva Does

1. Fetches starred articles from Miniflux (or FreshRSS)
2. Extracts and cleans article content
3. Analyzes content using local LLM (Ollama)
4. Searches for relevant books and papers (OpenLibrary, arXiv, Semantic Scholar)
5. Validates against your library catalog (Koha)
6. Sends personalized notifications (Ntfy)

**Result**: Wake up to curated book recommendations based on what you read, filtered by what you already own.

## Architecture: Distributed MQTT Primitives

Each stage of the pipeline is an independent long-running binary. They communicate exclusively through a Mosquitto broker — no service calls each other directly.

```
[trigger] → source-freshrss ─┐
            source-miniflux  ─┤→ extractor → analyzer → search-openlibrary ─┐
            source-linkwarden ┘                        ├─→ search-arxiv ────┤→ koha-check ─┐
                                                       └─→ search-semantic-scholar ↑      │
                                                                        (state) ← (storage) ←─┘
                                                                           ↓
                                                                        notifier (digest)
```

### Topic Chain

| Topic | Publisher | Subscriber(s) |
|-------|-----------|---------------|
| `minerva/pipeline/trigger` | external / `make trigger` | source-freshrss, source-miniflux, source-linkwarden |
| `minerva/articles/raw` | source primitives | extractor, state |
| `minerva/articles/extracted` | extractor | analyzer, state |
| `minerva/articles/analyzed` | analyzer | storage, search-openlibrary, search-arxiv, search-semantic-scholar, state |
| `minerva/books/candidates` | search-openlibrary, search-arxiv, search-semantic-scholar | storage, state |
| `minerva/books/checked` | koha-check | storage, state |
| `minerva/articles/complete` | storage | source-freshrss, source-miniflux, source-linkwarden |
| `minerva/pipeline/digest` | external / `make digest` | notifier |

### Core Design Principles

**1. Primitive Independence**
Each primitive is a self-contained binary with no cross-dependencies:

```go
// Subscribe to one topic, publish to the next
mqttClient.Subscribe(mqtt.TopicArticlesRaw, func(payload []byte) {
    var msg mqtt.RawArticle
    json.Unmarshal(payload, &msg)
    result := doWork(msg)
    mqttClient.Publish(mqtt.TopicArticlesExtracted, result)
})
```

**2. Source Pluggability**
Source primitives are interchangeable. Miniflux is the primary source; FreshRSS and Linkwarden are also supported. All subscribe to `minerva/pipeline/trigger` and publish `RawArticle` messages to `minerva/articles/raw`.

**3. Data Persistence**
Each source primitive maintains its own SQLite state DB for dedup and completion tracking. The storage primitive owns all book recommendations database writes.

```
published → pipeline stages → storage → ArticleComplete → marked done in source state DB
```

## Project Structure

```
minerva/
├── cmd/
│   ├── source-freshrss/         # FreshRSS source primitive
│   ├── source-miniflux/         # Miniflux source primitive
│   ├── source-linkwarden/       # Linkwarden source primitive
│   ├── extractor/               # HTML content extraction
│   ├── analyzer/                # Ollama LLM analysis
│   ├── search-openlibrary/      # OpenLibrary book search
│   ├── search-arxiv/            # arXiv paper search
│   ├── search-semantic-Scholar/ # Semantic Scholar paper search
│   ├── koha-check/              # Library catalog validation
│   ├── storage/                 # Recommendations DB writes + article completion
│   ├── state/                   # Pipeline crash recovery + message replay
│   └── notifier/                # Digest notifications (Ntfy)
├── internal/
│   ├── config/           # Environment-based configuration
│   ├── database/         # Book recommendations SQLite DB
│   ├── mqtt/             # Shared MQTT contract (topics, messages, client)
│   ├── state/            # Per-source SQLite dedup state
│   └── services/         # HTTP clients for external APIs
│       ├── freshrss.go
│       ├── miniflux.go
│       ├── extractor.go
│       ├── ollama.go
│       ├── openlibrary.go
│       ├── arxiv.go
│       ├── semanticscholar.go
│       ├── koha.go
│       └── ntfy.go
├── pkg/logger/           # Structured logging
├── deploy/
│   ├── nomad/            # Nomad job definitions
│   └── mosquitto/        # Mosquitto broker config
├── docker-compose.yml    # Development containers (includes Mosquitto)
├── Dockerfile            # Multi-stage production build
└── Makefile              # Build automation
```

## Quick Start

### Prerequisites

- Go 1.21+
- Mosquitto MQTT broker (via Docker Compose or system install)
- Miniflux instance with API key (primary source)
- FreshRSS instance with Fever API enabled (optional source)
- Linkwarden instance (optional source)
- Ollama running locally or remote
- Koha library system (optional)
- Ntfy server (optional)

### Development Setup

```bash
# Clone and configure
git clone <repository>
cd minerva
cp .env.example .env.dev

# Edit .env.dev with your endpoints
# See Configuration section below

# Install dependencies
make deps

# Start Mosquitto broker
make mosquitto

# Build all primitives (native, for local dev)
make build-primitives

# Run each primitive in a separate terminal
make run-source-miniflux
make run-source-freshrss
make run-source-linkwarden
make run-extractor
make run-analyzer
make run-search-openlibrary
make run-search-arxiv
make run-search-semantic-scholar
make run-koha-check
make run-storage
make run-state
make run-notifier

# Trigger the pipeline
make trigger

# Send a digest notification (requires storage and notifier running)
make digest

# Reset state DB
make reset-db
```

### Production Deployment

#### Nomad (Recommended)
```bash
# Deploy to cluster
make deploy-nomad

nomad job status minerva
```

#### Docker Compose
```bash
# Build and run
make docker
docker-compose up

# With dev services (includes Ollama)
make docker-dev
```

## Configuration

Create `.env.dev` or `.env`, see .env.example for details

### Configuration Reference

| Variable | Description | Default |
|----------|-------------|---------|
| `LOG_LEVEL` | debug/info/warn/error | info |
| `MQTT_BROKER_URL` | Mosquitto broker address | tcp://localhost:1883 |
| `MQTT_CLIENT_ID` | Unique client ID per primitive | primitive-specific |
| `STATE_DB_PATH` | Per-source SQLite state file | ./data/<source>-state.db |
| `DATABASE_PATH` | Book recommendations DB (storage) | ./data/minerva.db |
| `MINIFLUX_BASE_URL` | Miniflux instance URL | - |
| `MINIFLUX_API_KEY` | Miniflux API key | - |
| `FRESHRSS_BASE_URL` | Fever API endpoint | - |
| `FRESHRSS_API_KEY` | Fever API key | - |
| `OLLAMA_BASE_URL` | Ollama endpoint | http://localhost:11434 |
| `OLLAMA_MODEL` | Model name | llama2 |
| `ARXIV_TIMEOUT` | arXiv API timeout (seconds) | 30 |
| `SEMANTIC_SCHOLAR_TIMEOUT` | Semantic Scholar timeout (seconds) | 30 |
| `SEMANTIC_SCHOLAR_API_KEY` | Semantic Scholar API key (optional, raises rate limit) | - |
| `KOHA_BASE_URL` | Library API endpoint | - |
| `NTFY_TOPIC` | Notification topic | - |

### Debug Settings

**DEBUG_OLLAMA**: When enabled, writes detailed multi-pass analysis files to `./debug/` directory:
- `article-{id}-pass1-*.txt` - Domain classification
- `article-{id}-pass2-*.txt` - Entity extraction
- `article-{id}-pass3-*.txt` - Concept extraction
- `article-{id}-complete.json` - Combined analysis
```bash
DEBUG_OLLAMA=true make run-analyzer
```

## Development

### Building
```bash
make build-primitives  # Native binaries for local dev
make build             # Production binaries (Linux/amd64)
make build-dev         # With debug symbols
make docker            # Docker image
```

### Testing
```bash
make test              # Run tests
make test-coverage     # With coverage
make fmt lint          # Code quality
make ci                # Full CI checks
```

### Adding a New Source Primitive

1. **Create service** in `internal/services/<source>.go` — HTTP client, fetch starred/unread items
2. **Create primitive** at `cmd/source-<name>/main.go`:

```go
// Subscribe to trigger and completion topics
mqttClient.Subscribe(mqtt.TopicPipelineTrigger, func(_ []byte) {
    fetchAndPublish(log, svc, stateDB, mqttClient)
})
mqttClient.Subscribe(mqtt.TopicArticlesComplete, func(payload []byte) {
    var msg mqtt.ArticleComplete
    json.Unmarshal(payload, &msg)
    stateDB.MarkCompleteByArticleID(msg.ArticleID)
})
```

3. **Use `internal/state/`** for SQLite dedup
4. **Add build and run targets** to Makefile

### Message Types

All defined in `internal/mqtt/messages.go`:

- `RawArticle` — URL + title only (extractor fetches content from URL)
- `ExtractedArticle` — adds clean text Content field
- `AnalyzedArticle` — summary, keywords, concepts, insights; no Content
- `BookCandidates` / `CheckedBooks` / `ArticleComplete`

Every message type embeds `Envelope`: MessageID (UUID), ArticleID, Source, SourceID, Timestamp.

**Article ID** is the first 16 hex chars of the SHA256 of the URL — stable across sources and pipeline stages.
**Source ID** is a source-native identifier, e.g. `miniflux:12345`, `freshrss:789`, `linkwarden:456`.

## Monitoring

### Structured Logging

JSON logs for easy aggregation:

```json
{
  "level": "info",
  "time": "2025-01-01T02:00:00Z",
  "msg": "Published article to bus",
  "article_id": "a3f9c12b44e1d7f0",
  "source": "miniflux"
}
```

### Vector/Loki Integration

Nomad job configured for log collection:
- JSON stdout logs
- 10MB max log size
- 3 file rotation

### Key Metrics

- Articles published per trigger
- Processing duration per stage
- Success/failure rates
- Pending (incomplete) articles per source

## Database Schema

Book recommendations DB (written by storage primitive):

```sql
articles
  - id, url, title, content
  - summary, keywords, insights
  - processed_at, created_at

book_recommendations
  - id, article_id
  - title, author, isbn, isbn13
  - publisher, publish_year, cover_url
  - openlibrary_key, owned_in_koha
  - relevance, created_at
```

Per-source state DB (`internal/state/`):

```sql
article_state
  - url, article_id, title
  - published_at, completed_at
```

## Use Cases Beyond Books

This pattern works for any event-driven pipeline over MQTT:

- **Content Curation**: Source → Analyze → Categorize → Store
- **Data Enrichment**: Input → Extract → Enhance → Validate
- **Monitoring**: Scrape → Parse → Analyze → Alert
- **Document Processing**: Fetch → OCR → Classify → Store
- **Social Analysis**: Collect → Sentiment → Summarize → Report

The key: Identify discrete stages, build independent primitives, connect through a message broker.

## Why This Architecture?

### Advantages

- **Independent deployment**: restart or replace any primitive without touching others
- **Pluggable sources**: add new RSS readers without modifying the pipeline
- **Testability**: mock MQTT messages for unit tests
- **Debuggability**: subscribe to any topic to inspect in-flight messages
- **Resilience**: at-least-once delivery (QoS 1); pending articles re-published on restart

### Trade-offs

- **Latency**: multi-hop message passing adds overhead
- **Operational complexity**: 7 processes instead of one
- **Local dev**: requires a running Mosquitto broker

The previous monolith is tagged `pre-mqtt-refactor` in git.

## Troubleshooting

### MQTT / Broker Issues
```bash
# Start broker
make mosquitto

# Check broker is reachable
mosquitto_pub -h localhost -p 1883 -t test -m hello

# Subscribe to all Minerva topics for debugging
mosquitto_sub -h localhost -p 1883 -t 'minerva/#' -v
```

### Database Issues
```bash
mkdir -p ./data
chmod 755 ./data
sqlite3 ./data/minerva.db ".tables"
```

### Miniflux Authentication
- Verify API key in `.env`
- Confirm starred entries exist in Miniflux

### FreshRSS Authentication
- Verify Fever API enabled
- Check credentials in `.env`
- Confirm starred articles exist

### Ollama Connection
```bash
# Check Ollama running
curl http://localhost:11434/api/version

# List models
ollama list

# Test model
ollama run llama2 "test"
```

### Debug Logging
```bash
LOG_LEVEL=debug make run-analyzer
```

## Future Enhancements

- ~~Multi-pass LLM analysis for deeper understanding~~
- ~~Parallel search primitives (OpenLibrary, arXiv, Semantic Scholar)~~
- Semantic similarity scoring using embeddings
- Feedback loop for recommendation quality
- Web UI for browsing recommendations
- Parallel article processing
- Nomad per-primitive job definitions
- Additional content sources (Pocket, Instapaper)
- Advanced ranking algorithms

## Contributing

This is a personal prototype demonstrating architectural patterns. Fork and adapt for your use cases.
