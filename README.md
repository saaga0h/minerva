# Minerva - Intelligent Content Curation Pipeline

Minerva transforms RSS feed items into book recommendations by orchestrating multiple REST APIs and AI services. It demonstrates how to build complex workflows in Go by composing simple, focused services rather than building monolithic systems.

## What Minerva Does

1. Fetches starred articles from FreshRSS
2. Extracts and cleans article content
3. Analyzes content using local LLM (Ollama)
4. Searches for relevant books (OpenLibrary)
5. Validates against your library catalog (Koha)
6. Sends personalized notifications (Ntfy)

**Result**: Wake up to curated book recommendations based on what you read, filtered by what you already own.

## Architecture: Service Composition Pattern

```
RSS Feed → Extract → Analyze → Search → Validate → Notify
   ↓         ↓         ↓         ↓         ↓         ↓
FreshRSS  Extractor  Ollama  OpenLibrary  Koha    Ntfy
(Fever)   (goquery) (local)   (REST)    (REST) (webhook)
```

### Core Design Principles

**1. Service Independence**
Each service is self-contained with no cross-dependencies:

```go
type Service struct {
    config config.ServiceConfig
    client *http.Client
    logger *logrus.Logger
}

func NewService(cfg config.ServiceConfig) *Service
func (s *Service) DoWork(input Data) (Output, error)
func (s *Service) SetLogger(logger *logrus.Logger)
```

**2. Pipeline Orchestration**
The pipeline coordinates service calls without services knowing about each other:

```go
func (p *Pipeline) Run(ctx context.Context) error {
    items := p.freshRSS.GetStarredItems()        // 1. Source
    articles := p.extractor.ExtractContent(items) // 2. Extract
    metadata := p.ollama.ProcessContent(articles) // 3. Analyze
    books := p.openLibrary.SearchBooks(metadata)  // 4. Search
    owned := p.koha.CheckOwnership(books)         // 5. Validate
    p.ntfy.Send(newBooks, ownedBooks)            // 6. Notify
}
```

**3. Data Persistence**
SQLite maintains state between runs:

```
articles → processed → recommendations → checked → notified
```

## Project Structure

```
minerva/
├── cmd/minerva/           # Application entry point
├── internal/
│   ├── config/           # Environment-based configuration
│   ├── database/         # SQLite persistence layer
│   ├── pipeline/         # Service orchestration
│   └── services/         # Independent service integrations
│       ├── freshrss.go   # Fever API client
│       ├── extractor.go  # HTML content extraction
│       ├── ollama.go     # Local LLM processing
│       ├── openlibrary.go # Book metadata search
│       ├── koha.go       # Library catalog integration
│       ├── ntfy.go       # Push notifications
│       └── searxng.go    # Web search (optional)
├── pkg/logger/           # Structured logging
├── deploy/nomad/         # Nomad job definitions
├── docker-compose.yml    # Development containers
├── Dockerfile           # Multi-stage production build
└── Makefile            # Build automation
```

## Quick Start

### Prerequisites

- Go 1.21+
- FreshRSS instance with Fever API enabled
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

# Test without external calls
make dry-run

# Run pipeline
LOG_LEVEL=debug make dev

# Reset DB
make reset-db
```

### Production Deployment

#### Nomad (Recommended)
```bash
# Deploy to cluster
make deploy-nomad

# Runs nightly at 2 AM
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

#### Cron
```bash
# Add to crontab
0 2 * * * /path/to/minerva -config /path/to/.env
```

## Configuration

Create `.env.dev` or `.env`:

```bash
# Application
APP_ENV=production
LOG_LEVEL=info
DATABASE_PATH=./data/minerva.db

# FreshRSS (Fever API)
FRESHRSS_BASE_URL=https://rss.example.com/api/fever.php?api
FRESHRSS_API_KEY=your_fever_api_key
FRESHRSS_TIMEOUT=30

# Ollama (Local LLM)
OLLAMA_BASE_URL=http://localhost:11434
OLLAMA_MODEL=mixtral:8x7b
OLLAMA_TIMEOUT=300
OLLAMA_MAX_TOKENS=2048
OLLAMA_TEMPERATURE=0.7

# OpenLibrary
OPENLIBRARY_TIMEOUT=30

# Content Extractor
EXTRACTOR_USER_AGENT=Minerva/1.0
EXTRACTOR_TIMEOUT=30
EXTRACTOR_MAX_SIZE=10485760

# Koha (Optional)
KOHA_BASE_URL=https://library.example.com
KOHA_USERNAME=minerva
KOHA_PASSWORD=your_password
KOHA_TIMEOUT=30

# Ntfy (Optional)
NTFY_BASE_URL=https://ntfy.example.com
NTFY_TOPIC=minerva
NTFY_TOKEN=your_token
NTFY_PRIORITY=default
NTFY_ENABLED=true
```

### Configuration Reference

| Variable | Description | Default |
|----------|-------------|---------|
| `LOG_LEVEL` | debug/info/warn/error | info |
| `DATABASE_PATH` | SQLite file location | ./data/minerva.db |
| `FRESHRSS_BASE_URL` | Fever API endpoint | - |
| `FRESHRSS_API_KEY` | Fever API key | - |
| `OLLAMA_BASE_URL` | Ollama endpoint | http://localhost:11434 |
| `OLLAMA_MODEL` | Model name | mixtral:8x7b |
| `KOHA_BASE_URL` | Library API endpoint | - |
| `NTFY_TOPIC` | Notification topic | - |

## Development

### Building
```bash
make build        # Production binary
make build-dev    # With debug symbols
make docker       # Docker image
```

### Testing
```bash
make test         # Run tests
make test-coverage # With coverage
make fmt lint     # Code quality
make ci           # Full CI checks
```

### Adding a New Service

1. **Create service** in `internal/services/`:

```go
package services

type NewService struct {
    config config.NewServiceConfig
    client *http.Client
    logger *logrus.Logger
}

func NewNewService(cfg config.NewServiceConfig) *NewService {
    return &NewService{
        config: cfg,
        client: &http.Client{Timeout: 30 * time.Second},
        logger: logrus.New(),
    }
}

func (s *NewService) DoWork(input string) (string, error) {
    // HTTP request
    // Parse response
    // Return data
}

func (s *NewService) SetLogger(logger *logrus.Logger) {
    s.logger = logger
}
```

2. **Add config** to `internal/config/config.go`
3. **Wire into pipeline** in `internal/pipeline/pipeline.go`
4. **Initialize** in `cmd/minerva/main.go`

### Service Design Rules

- **Single Responsibility**: One service, one task
- **No Cross-Dependencies**: Services don't call other services
- **Stateless**: No state between calls
- **Testable**: Easy to mock/stub
- **Composable**: Pipeline handles orchestration

## Monitoring

### Structured Logging

JSON logs for easy aggregation:

```json
{
  "level": "info",
  "time": "2025-01-01T02:00:00Z",
  "msg": "Pipeline completed",
  "articles_processed": 15,
  "duration": "2m30s"
}
```

### Vector/Loki Integration

Nomad job configured for log collection:
- JSON stdout logs
- 10MB max log size
- 3 file rotation

### Key Metrics

- Articles processed per run
- Processing duration
- Success/failure rates
- Database growth

## Database Schema

SQLite tables:

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

## Use Cases Beyond Books

This pattern works for any REST API composition:

- **Content Curation**: Source → Analyze → Categorize → Store
- **Data Enrichment**: Input → Extract → Enhance → Validate
- **Monitoring**: Scrape → Parse → Analyze → Alert
- **Document Processing**: Fetch → OCR → Classify → Store
- **Social Analysis**: Collect → Sentiment → Summarize → Report

The key: Identify discrete steps, build focused services, orchestrate through pipeline.

## Why This Architecture?

### Advantages

✅ **Modularity**: Swap any service independently  
✅ **Testability**: Mock services for unit tests  
✅ **Maintainability**: ~200 lines per service  
✅ **Extensibility**: Add services without touching existing code  
✅ **Debuggability**: Clear boundaries isolate issues  
✅ **Reusability**: Extract services to separate packages  

### Trade-offs

❌ **Latency**: Sequential processing takes time  
❌ **Complexity**: More moving parts  
❌ **Error Handling**: Careful propagation needed  
❌ **State**: Database required for progress  

For Minerva, latency doesn't matter (runs overnight), but modularity and maintainability are essential.

## Troubleshooting

### Database Issues
```bash
mkdir -p ./data
chmod 755 ./data
sqlite3 ./data/minerva.db ".tables"
```

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
ollama run mixtral:8x7b "test"
```

### Debug Logging
```bash
LOG_LEVEL=debug make dev
```

## Future Enhancements

- Multi-pass LLM analysis for deeper understanding
- Semantic similarity scoring using embeddings
- Feedback loop for recommendation quality
- Web UI for browsing recommendations
- Parallel article processing
- Additional content sources (Pocket, Instapaper)
- Advanced ranking algorithms

## Contributing

This is a personal prototype demonstrating architectural patterns. Fork and adapt for your use cases.
