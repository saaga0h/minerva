# Minerva

> Transforms RSS starred articles and bookmarks into book and paper recommendations via a pipeline of independent MQTT primitives.

MQTT-native, Postgres-backed with pgvector, built in Go.

## Documentation

- **[CONCEPTS.md](CONCEPTS.md)** — the ideas: primitive model, ArticleID derivation, multi-pass LLM reasoning, canonical ID deduplication, crash recovery, brief retrieval, Journal integration, and design decisions
- **[ARCHITECTURE.md](ARCHITECTURE.md)** — the structure: components, data flows, MQTT topic map, data model, wire formats, algorithms, invariants, and known constraints

## Intellectual Property

These ideas are offered freely. The mechanisms described here are documented in detail so that anyone can understand, use, modify, and build on them without encumbrance. This documentation exists as a public record to ensure no party can retrospectively claim these ideas as proprietary.

## How It Works

1. Source primitives (FreshRSS, Miniflux, Linkwarden) fetch starred/bookmarked articles on trigger
2. Extractor cleans article text
3. Analyzer runs three-pass Ollama LLM analysis → domain classification, named entity extraction, concept extraction
4. Four search primitives run in parallel (OpenLibrary, arXiv, Semantic Scholar, OpenAlex) → book and paper candidates
5. Koha-check filters books already owned in the library catalog
6. Store persists the full knowledge base; publishes `ArticleComplete` to signal sources
7. Brief service answers semantic queries (vector ANN or keyword search) against the accumulated corpus
8. Consolidator aggregates brief session scores and deduplicates
9. Notifier sends push notifications via ntfy

**Result**: Curated book and paper recommendations based on what you read, filtered by what you already own.

## Quick Start

### Prerequisites

- Go 1.21+
- Docker + Docker Compose (for Mosquitto and PostgreSQL)
- [Ollama](https://ollama.com) running on the host with `qwen3-embedding:8b` pulled
- Miniflux, FreshRSS, or Linkwarden instance (at least one source)
- Koha library system (optional)
- ntfy server (optional)

```bash
ollama pull qwen3-embedding:8b
```

### Setup

```bash
# Add hooks
git config core.hooksPath .githooks

# Install Go dependencies
make deps

# Copy and configure
cp .env.example .env.dev

# Start Postgres + Mosquitto
make mosquitto
make pg

# Build all primitives (native, for local dev)
make build-primitives
```

Migrations run automatically when any service starts.

### Running

Start each primitive in a separate terminal. **Order matters** — all primitives must be connected before the trigger fires (no persistent MQTT sessions):

```bash
# Observers first
make run-storage
make run-state

# Pipeline primitives
make run-extractor
make run-analyzer
make run-search-openlibrary
make run-search-arxiv
make run-search-semantic-scholar
make run-koha-check

# Sources
make run-source-miniflux
make run-source-freshrss
make run-source-linkwarden

# Brief / notification
make run-brief-assemble
make run-notifier

# Fire the pipeline
make trigger
```

## Configuration

Copy `.env.example` to `.env.dev`. Key variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MQTT_BROKER_URL` | `tcp://localhost:1883` | Mosquitto broker |
| `DB_HOST` / `DB_PORT` | `localhost:5432` | PostgreSQL |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama on host (not in Docker) |
| `OLLAMA_EMBED_MODEL` | `qwen3-embedding:8b` | Must produce 4096-dim vectors |
| `MINIFLUX_BASE_URL` | — | Miniflux instance URL |
| `MINIFLUX_API_KEY` | — | Miniflux API key |
| `FRESHRSS_BASE_URL` | — | FreshRSS Fever API endpoint |
| `FRESHRSS_API_KEY` | — | FreshRSS Fever API key |
| `LINKWARDEN_BASE_URL` | — | Linkwarden instance URL |
| `LINKWARDEN_API_KEY` | — | Linkwarden API key |
| `SEMANTIC_SCHOLAR_API_KEY` | — | Optional; severe rate limits without it |
| `KOHA_BASE_URL` | — | Koha library API endpoint |
| `NTFY_TOPIC` | — | ntfy notification topic |
| `DEBUG_OLLAMA` | `false` | Write per-pass prompts/responses to `./debug/` |

## Building

```bash
make build-primitives  # Native binaries for local dev
make build             # Linux/amd64 production build
make fmt && make lint
```

## Inspecting State

```bash
make psql              # Open psql shell
make mqtt-sub          # Watch all Minerva MQTT traffic (requires mosquitto_sub)
make query             # Inspect recommendations DB
make trigger           # Fire pipeline (requires mosquitto_pub in PATH)
make digest            # Send digest notification
```

## Infrastructure

```bash
make mosquitto         # Start Mosquitto (docker compose)
make pg                # Start PostgreSQL (docker compose)
```

## Project Structure

```
minerva/
├── cmd/
│   ├── source-freshrss/          # FreshRSS source primitive
│   ├── source-miniflux/          # Miniflux source primitive
│   ├── source-linkwarden/        # Linkwarden source primitive
│   ├── extractor/                # HTML content extraction
│   ├── analyzer/                 # Three-pass Ollama LLM analysis
│   ├── search-openlibrary/       # OpenLibrary book search
│   ├── search-arxiv/             # arXiv preprint search
│   ├── search-semantic-scholar/  # Semantic Scholar paper search
│   ├── search-openalex/          # OpenAlex scholarly works search
│   ├── koha-check/               # Library catalog validation
│   ├── store/                    # Knowledge base writes + ArticleComplete
│   ├── state/                    # Crash recovery + message replay
│   ├── brief/                    # Semantic retrieval (vector ANN + keyword)
│   ├── consolidator/             # Score aggregation + notification dedup
│   ├── notifier/                 # ntfy push notifications
│   └── trigger/                  # One-shot pipeline trigger
├── internal/
│   ├── config/                   # Environment-based configuration
│   ├── mqtt/                     # Shared MQTT contract (topics, messages, client)
│   ├── store/                    # PostgreSQL knowledge base (articles, works, sessions)
│   ├── statestore/               # PostgreSQL crash-recovery state
│   └── services/                 # HTTP clients for external APIs
├── pkg/logger/                   # Structured logging
├── deploy/nomad/                 # Nomad job definitions
├── docker-compose.yml
└── Makefile
```

## Troubleshooting

```bash
# Broker not reachable
mosquitto_pub -h localhost -p 1883 -t test -m hello

# Watch all pipeline traffic
mosquitto_sub -h localhost -p 1883 -t 'minerva/#' -v

# Ollama not running
curl http://localhost:11434/api/version
ollama list

# Debug LLM extraction
DEBUG_OLLAMA=true make run-analyzer
# Writes to ./debug/article-{id}-pass{1,2,3}-*.txt and article-{id}-complete.json

# Verbose logging
LOG_LEVEL=debug make run-analyzer
```

The previous monolith is tagged `pre-mqtt-refactor` in git.
