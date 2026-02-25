# Minerva

Transforms RSS starred articles into book recommendations via a pipeline of independent MQTT primitives. Sources fetch articles, the pipeline extracts, analyzes with Ollama, searches OpenLibrary, checks Koha ownership, and notifies via Ntfy.

## Architecture

Minerva is a distributed system of 7 long-running MQTT primitives connected through a Mosquitto broker. There is no monolith — each primitive is an independent binary that subscribes to one topic and publishes to the next.

**Message flow:**

```
[trigger] → source-freshrss ─┐
            source-miniflux  ─┤→ extractor → analyzer → book-search → koha-check → notifier
                              └─────────────────────────────────────────────────────↑
                                                                        (ArticleComplete)
```

**Topic chain:**

| Topic | Publisher | Subscriber(s) |
|---|---|---|
| `minerva/pipeline/trigger` | external / `make trigger` | source-freshrss, source-miniflux |
| `minerva/articles/raw` | source primitives | extractor |
| `minerva/articles/extracted` | extractor | analyzer |
| `minerva/articles/analyzed` | analyzer | book-search |
| `minerva/books/candidates` | book-search | koha-check |
| `minerva/books/checked` | koha-check | notifier |
| `minerva/articles/complete` | notifier | source-freshrss, source-miniflux |

**Key design decisions:**

- Content is dropped after `minerva/articles/analyzed` — it is large and not needed downstream
- Source primitives subscribe to `minerva/articles/complete` to mark articles done in their own SQLite state DB (dedup and completion tracking)
- Only the notifier writes to the book recommendations DB (`internal/database/`)
- All MQTT messages are QoS 1 (at-least-once), non-retained

## Current State

Implemented and working:
- All 7 primitives in `cmd/`: source-freshrss, source-miniflux, extractor, analyzer, book-search, koha-check, notifier
- Shared MQTT contract in `internal/mqtt/`: topics.go, messages.go, client.go
- Per-source SQLite state in `internal/state/` for dedup and completion tracking
- Miniflux HTTP client in `internal/services/miniflux.go` (no external library)
- Mosquitto in docker-compose.yml with config at `deploy/mosquitto/mosquitto.conf`
- Makefile build and run targets for all primitives

The monolith (`cmd/minerva/` and `internal/pipeline/`) has been deleted. Git tag `pre-mqtt-refactor` marks the last monolith state.

Nomad per-primitive deployment: planned, not yet done.

## Development

```bash
# Start Mosquitto broker
make mosquitto

# Build all primitives (native, for local dev)
make build-primitives

# Run each primitive in a separate terminal
make run-source-freshrss
make run-source-miniflux
make run-extractor
make run-analyzer
make run-book-search
make run-koha-check
make run-notifier

# Trigger the pipeline
make trigger          # publishes {} to minerva/pipeline/trigger

# Tests, formatting, lint
make test
make fmt
make lint

# Production build (Linux/amd64)
make build

# Inspect book recommendations DB
make query
```

All run targets expect a `.env.dev` config file.

## Conventions

**Adding a new source primitive:**
1. Create `internal/services/<source>.go` — HTTP client, fetch starred/unread items
2. Create `cmd/source-<name>/main.go` — subscribe to `minerva/pipeline/trigger`, publish `mqtt.RawArticle` to `minerva/articles/raw`
3. Use `internal/state/` for SQLite dedup; subscribe to `minerva/articles/complete` to mark done
4. Add build and run targets to Makefile

**Message types** (all in `internal/mqtt/messages.go`):
- `RawArticle` — URL + title only, no content (extractor fetches content from URL)
- `ExtractedArticle` — adds clean text Content field
- `AnalyzedArticle` — summary, keywords, concepts, insights; no Content
- `BookCandidates` / `CheckedBooks` / `ArticleComplete`

**MQTT client** (`internal/mqtt/client.go`):
- QoS 1, auto-reconnect, 5s retry interval, 10s connect timeout
- `Publish(topic, any)` — marshals to JSON
- `Subscribe(topic, func([]byte))` — delivers raw JSON bytes to handler

**Article ID** is SHA256 of URL — stable across sources and pipeline stages.

**Envelope** struct is embedded in every message type: MessageID (UUID), ArticleID, Source, Timestamp.

**Dependencies:** `github.com/eclipse/paho.mqtt.golang` for MQTT, `go-readability` for extraction, `go-sqlite3` (CGo) for state and DB, `logrus` for structured logging.

CGo is required (`CGO_ENABLED=1`) because of go-sqlite3.

## Recent Changes

- 2026-02-25: MQTT primitive refactor — deleted monolith, split into 7 independent primitives communicating via Mosquitto; added internal/mqtt/ contract, internal/state/ per-source dedup, Miniflux source, Mosquitto in docker-compose, updated Makefile with primitive build/run/trigger targets
