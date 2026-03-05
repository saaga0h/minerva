# Minerva

Transforms RSS starred articles and bookmarks into book recommendations via a pipeline of independent MQTT primitives: source-freshrss, source-miniflux, source-linkwarden, extractor, analyzer, book-search, koha-check, notifier, store. Each is a long-running binary communicating through Mosquitto.

## Build & Test

```bash
make mosquitto          # start Mosquitto broker (docker compose)
make pg                 # start PostgreSQL (docker compose) — required for source primitives and store
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
make run-store          # optional — populates knowledge base (requires PostgreSQL, as do source primitives)

make trigger            # fire pipeline (requires mosquitto_pub in PATH)
make query              # inspect recommendations DB
make fmt && make lint
```

No tests currently exist.

CGo is required: `CGO_ENABLED=1` (go-sqlite3 dependency in the notifier's recommendations DB).

## Constraints

**Startup order is critical.** paho uses `CleanSession=true` — no persistent sessions. Every primitive must be connected to Mosquitto *before* the trigger fires. Messages published to a topic with no active subscriber are lost permanently.

**Mosquitto must be running** before any primitive starts. `make mosquitto` uses docker compose.

**Ollama must be reachable** at `OLLAMA_BASE_URL` (default `http://localhost:11434`) before the analyzer starts.

**`cmd/store` is a pure observer** — it does not publish to any pipeline topic. The pipeline runs normally without it. Start it before the trigger if knowledge base population is wanted.

**PostgreSQL must be running** before source primitives (freshrss, miniflux, linkwarden) and `cmd/store` start. `make pg` uses docker compose. `pgxpool` (pgx/v5) is concurrency-safe — no `sync.Mutex` needed around store calls (unlike the SQLite notifier). Source primitives will fatal on startup if `STORE_DSN` is unreachable.

**MQTT topic names changed**: `minerva/books/candidates` → `minerva/works/candidates`, `minerva/books/checked` → `minerva/works/checked`. All primitives must use the new topic strings simultaneously — mixed old/new strings drop messages silently.

**`canonical_id` in the `works` table** is the cross-source dedup key. Priority: `isbn13:{x}` > `doi:{x}` > `arxiv:{x}` > `ref:{reference_id}`. Same book from two sources upserts into one row; both reference IDs are appended to the `reference_ids JSONB` array.

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

**Adding a source primitive:** service in `internal/services/`, binary in `cmd/source-<name>/`. Subscribe to `minerva/pipeline/trigger` and `minerva/articles/complete`. Use `internal/store` for dedup: call `store.IsComplete`, `store.MarkPublished`, `store.MarkCompleteByArticleID`, `store.PendingArticles(ctx, "source-name")`. Use `store.ArticleID(url)` for the stable article ID. Publish `mqtt.RawArticle` to `minerva/articles/raw`. Add Makefile targets.

**Adding a search primitive** (e.g. arXiv): publish `mqtt.WorkCandidates` to `minerva/works/candidates`. Set `WorkType` to `"book"` or `"paper"`. Set `ReferenceID` to `"source-name:source-item-id"` (e.g. `"arxiv:2301.07041"`). Populate `ISBN13`/`DOI`/`ArXivID` where available — the store uses these for cross-source dedup. Multiple search primitives publish concurrently; koha-check filters by `work_type == "book"` before Koha lookup.

## Gotchas

- `DEBUG_OLLAMA=true` writes per-pass prompts and responses to `./debug/` — useful for diagnosing bad LLM output
- Miniflux source queries with `status=read&status=unread` — without this, starred+read articles are not returned by the API
- Linkwarden pagination is cursor-based: cursor starts at 0, each page's next cursor is the `id` of the last item returned. Stops when response array is empty. The title field in the JSON is `name`, not `title`.
- `make trigger` requires `mosquitto_pub` installed on the host (not in the container)
- Git tag `pre-mqtt-refactor` marks the last monolith state (cmd/minerva/ + internal/pipeline/ — now deleted)
- `STATE_DB_PATH` env var and `./data/*-state.db` SQLite files are obsolete — article state is now in PostgreSQL (`articles.published_at` / `articles.completed_at`). Remove from any `.env` files.
- Completed-article dedup is now shared across all sources — if freshrss completes an article, miniflux and linkwarden will also skip it on their next trigger run (was per-source SQLite before).
