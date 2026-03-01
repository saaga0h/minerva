# Minerva

Transforms RSS starred articles and bookmarks into book recommendations via a pipeline of independent MQTT primitives: source-freshrss, source-miniflux, source-linkwarden, extractor, analyzer, book-search, koha-check, notifier. Each is a long-running binary communicating through Mosquitto.

## Build & Test

```bash
make mosquitto          # start Mosquitto broker (docker compose)
make build-primitives   # native build for local dev
make build              # Linux/amd64 production build

# Run each in a separate terminal — all expect .env.dev
make run-source-freshrss
make run-source-miniflux
make run-source-linkwarden
make run-extractor
make run-analyzer
make run-book-search
make run-koha-check
make run-notifier

make trigger            # fire pipeline (requires mosquitto_pub in PATH)
make query              # inspect recommendations DB
make fmt && make lint
```

No tests currently exist.

CGo is required: `CGO_ENABLED=1` (go-sqlite3 dependency).

## Constraints

**Startup order is critical.** paho uses `CleanSession=true` — no persistent sessions. Every primitive must be connected to Mosquitto *before* the trigger fires. Messages published to a topic with no active subscriber are lost permanently.

**Mosquitto must be running** before any primitive starts. `make mosquitto` uses docker compose.

**Ollama must be reachable** at `OLLAMA_BASE_URL` (default `http://localhost:11434`) before the analyzer starts.

## Conventions

**MQTT handlers must dispatch work in a goroutine.** paho's default ordered delivery (`OrderMatters=true`) blocks all subsequent messages while a handler runs. Calling `Publish` (which calls `token.Wait()`) inside a handler without a goroutine deadlocks the router after the first message. Pattern used in every primitive:

```go
mqttClient.Subscribe(topic, func(payload []byte) {
    data := make([]byte, len(payload))
    copy(data, payload)
    go func() {
        // all blocking work here, including Publish calls
    }()
})
```

**Ollama calls are serialized** with `sync.Mutex` in the analyzer — Ollama handles one inference at a time. Timeout is 300s per pass × 3 passes = up to 15min per article.

**SQLite writes in the notifier are serialized** with `sync.Mutex` — concurrent goroutines writing to the recommendations DB need protection.

**ArticleID** = first 16 hex chars of SHA256(URL). Stable across all pipeline stages and sources.

**Adding a source primitive:** service in `internal/services/`, binary in `cmd/source-<name>/`. Subscribe to `minerva/pipeline/trigger` and `minerva/articles/complete`. Use `internal/state/` for dedup. Publish `mqtt.RawArticle` to `minerva/articles/raw`. Add Makefile targets.

## Gotchas

- `DEBUG_OLLAMA=true` writes per-pass prompts and responses to `./debug/` — useful for diagnosing bad LLM output
- Miniflux source queries with `status=read&status=unread` — without this, starred+read articles are not returned by the API
- Linkwarden pagination is cursor-based: cursor starts at 0, each page's next cursor is the `id` of the last item returned. Stops when response array is empty. The title field in the JSON is `name`, not `title`.
- `make trigger` requires `mosquitto_pub` installed on the host (not in the container)
- Git tag `pre-mqtt-refactor` marks the last monolith state (cmd/minerva/ + internal/pipeline/ — now deleted)
