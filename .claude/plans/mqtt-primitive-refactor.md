# Plan: MQTT Primitive Refactor
## Created: 2026-02-25
## Complexity: opus
## Recommended implementation model: sonnet

---

## Context

Minerva is a monolithic Go pipeline that runs as a nightly Nomad batch job. The pipeline is already
well-structured — 7 stateless services with no cross-dependencies — but the orchestration is a single
process. The goal is to break this into independent **primitives**: small binaries that each do one
thing, communicate via MQTT (Mosquitto), and can be composed arbitrarily by the user.

**Why this matters:**
- Source primitives become pluggable: FreshRSS, Miniflux, Linkwarden, Karakeep — they all publish
  to the same topic. Downstream primitives don't know or care which source produced an article.
- Each primitive can be deployed, restarted, and scaled independently in Nomad (Nomad deployment
  is a separate future plan).
- The existing `internal/services/` business logic is preserved. Primitives are thin MQTT wrappers
  around it.

**Architecture: Event-Carried State Transfer**

Each MQTT message carries the accumulated context forward. The payload grows as articles move through
the pipeline. No shared database is required for intermediate state — only the final output
(book recommendations) is persisted to SQLite.

```
[source-freshrss]  ─┐
[source-miniflux]  ─┼──► minerva/articles/raw
[source-linkwarden]─┘         │
                               ▼
                         [extractor] ──► minerva/articles/extracted
                                                   │
                                                   ▼
                                          [analyzer] ──► minerva/articles/analyzed
                                                                  │
                                                                  ▼
                                                       [book-search] ──► minerva/books/candidates
                                                                                 │
                                                                                 ▼
                                                                      [koha-check] ──► minerva/books/checked
                                                                                              │
                                                                                              ▼
                                                                                       [notifier] ──► SQLite + Ntfy
                                                                                              │
                                                                                              ▼
                                                                              minerva/articles/complete
                                                                                    (source primitives listen here)
```

**State / failure approach:**
- Each source primitive maintains a local SQLite table: `{url, article_id, published_at, completed_at}`.
- On trigger, the source publishes unstarred/un-completed articles to the bus.
- The notifier publishes to `minerva/articles/complete` when a full pipeline run finishes for an article.
- Source primitives subscribe to completion events and mark articles done.
- Articles published but never completed (e.g., Ollama crash) are re-processed on the next run.
- Processing primitives are stateless — QoS 1 means potential duplicate delivery; this is acceptable
  for now (nightly batch, occasional re-processing of an article is harmless).

**Message size:** Article content (5–50 KB) + LLM analysis (1–5 KB) + book candidates (few KB) stays
well within Mosquitto's default limits. Content is dropped from the payload after the analyzer stage.

**Out of scope:** Nomad per-primitive deployment (separate plan), Linkwarden source, Karakeep source.

---

## Prerequisites
- [ ] Mosquitto broker accessible (add to docker-compose for local dev)
- [ ] Miniflux instance accessible for Miniflux primitive testing
- [ ] Existing Minerva pipeline still works before refactoring begins (`make dev`)

---

## Tasks

### [x] 1. Add MQTT dependency to go.mod
- **File**: `go.mod`, `go.sum`
- **Action**: Add `github.com/eclipse/paho.mqtt.golang` and confirm `miniflux.app/v2` (check exact
  module path in the project where you've used it: `miniflux.app v1.0.46 // indirect` suggests
  the import path may be `miniflux.app/v2` or `miniflux.app` — verify before adding)
- **Command**: `go get github.com/eclipse/paho.mqtt.golang@latest && go get miniflux.app/v2@latest`
- **Test**: `go mod tidy` completes without errors

---

### [x] 2. Define the MQTT contract: topics and message types
- **Files**: `internal/mqtt/topics.go`, `internal/mqtt/messages.go`
- **Action**: Create the shared contract all primitives adhere to.

**`internal/mqtt/topics.go`**:
```go
package mqtt

const (
    // Source primitives publish here. Downstream primitives don't care about the source.
    TopicArticlesRaw = "minerva/articles/raw"

    // Extractor publishes clean text here.
    TopicArticlesExtracted = "minerva/articles/extracted"

    // Ollama analyzer publishes LLM analysis here. Content field is dropped at this stage.
    TopicArticlesAnalyzed = "minerva/articles/analyzed"

    // OpenLibrary publishes book candidates here.
    TopicBooksCandidates = "minerva/books/candidates"

    // Koha publishes ownership-checked books here.
    TopicBooksChecked = "minerva/books/checked"

    // Notifier publishes here after successful notification. Sources subscribe for completion tracking.
    TopicArticlesComplete = "minerva/articles/complete"

    // External trigger (e.g., from Nomad batch job or manual run). Sources subscribe.
    TopicPipelineTrigger = "minerva/pipeline/trigger"
)
```

**`internal/mqtt/messages.go`** — five message types that progressively build context:
```go
package mqtt

import "time"

// Envelope is the common header carried in every message.
type Envelope struct {
    MessageID string    `json:"message_id"` // UUID, unique per message
    ArticleID string    `json:"article_id"` // stable ID: SHA256 of URL
    Source    string    `json:"source"`     // e.g. "freshrss", "miniflux"
    Timestamp time.Time `json:"timestamp"`
}

// RawArticle is published by source primitives to TopicArticlesRaw.
type RawArticle struct {
    Envelope
    URL   string `json:"url"`
    Title string `json:"title"`
    // Note: no content. The extractor fetches full content from the URL.
}

// ExtractedArticle is published by the extractor to TopicArticlesExtracted.
type ExtractedArticle struct {
    Envelope
    URL     string `json:"url"`
    Title   string `json:"title"`
    Content string `json:"content"` // clean text, ready for LLM
}

// AnalyzedArticle is published by the analyzer to TopicArticlesAnalyzed.
// Content is intentionally dropped here — it's large and no longer needed downstream.
type AnalyzedArticle struct {
    Envelope
    URL      string   `json:"url"`
    Title    string   `json:"title"`
    Domain   string   `json:"domain"`
    Summary  string   `json:"summary"`
    Keywords []string `json:"keywords"`
    Concepts []string `json:"concepts"`
    Insights string   `json:"insights"`
}

// BookCandidates is published by the book-search primitive to TopicBooksCandidates.
type BookCandidates struct {
    Envelope
    ArticleTitle string          `json:"article_title"`
    ArticleURL   string          `json:"article_url"`
    Books        []BookCandidate `json:"books"`
}

type BookCandidate struct {
    Title          string  `json:"title"`
    Author         string  `json:"author"`
    ISBN           string  `json:"isbn"`
    ISBN13         string  `json:"isbn13"`
    PublishYear    int     `json:"publish_year"`
    Publisher      string  `json:"publisher"`
    CoverURL       string  `json:"cover_url"`
    OpenLibraryKey string  `json:"openlibrary_key"`
    Relevance      float64 `json:"relevance"`
}

// CheckedBooks is published by the koha-check primitive to TopicBooksChecked.
type CheckedBooks struct {
    Envelope
    ArticleTitle string          `json:"article_title"`
    ArticleURL   string          `json:"article_url"`
    NewBooks     []BookCandidate `json:"new_books"`   // not in Koha catalog
    OwnedBooks   []OwnedBook     `json:"owned_books"` // already owned
}

type OwnedBook struct {
    Title  string `json:"title"`
    Author string `json:"author"`
    KohaID string `json:"koha_id"`
}

// ArticleComplete is published by the notifier to TopicArticlesComplete.
// Source primitives subscribe to this to mark articles as done.
type ArticleComplete struct {
    Envelope
    CompletedAt time.Time `json:"completed_at"`
}
```

- **Test**: `go build ./internal/mqtt/...` compiles without errors

---

### [x] 3. Create MQTT client wrapper
- **File**: `internal/mqtt/client.go`
- **Action**: Thin wrapper around `paho.mqtt.golang`. Handles connect, reconnect, subscribe,
  publish. Adds structured logging (logrus). All primitives use this.

Key behaviors:
- Connect with client ID passed as config (e.g., `"minerva-extractor"`)
- Auto-reconnect enabled
- Publish uses QoS 1 (at-least-once)
- Subscribe uses QoS 1
- `Publish(topic string, payload any) error` — marshals to JSON internally
- `Subscribe(topic string, handler func(payload []byte)) error`
- Logger injected via `SetLogger(*logrus.Logger)`

- **Pattern**: Follow the existing service pattern in `internal/services/*.go` — struct with config,
  logger, and a constructor
- **Test**: `go build ./internal/mqtt/...` compiles; manual test: connect to local Mosquitto,
  publish and receive a test message

---

### [x] 4. Create state package for source primitive dedup
- **File**: `internal/state/state.go`
- **Action**: SQLite-backed minimal state tracker used **only by source primitives**. Tracks which
  article URLs have been published to the bus and which have completed the full pipeline.

Schema:
```sql
CREATE TABLE IF NOT EXISTS article_state (
    url          TEXT PRIMARY KEY,
    article_id   TEXT NOT NULL,  -- SHA256 of URL, used as stable ID in messages
    published_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP        -- NULL means pipeline not yet complete
);
```

Methods:
- `IsPublished(url string) (bool, error)` — has this URL been sent to the bus?
- `IsComplete(url string) (bool, error)` — has the full pipeline finished for this URL?
- `MarkPublished(url, articleID string) error`
- `MarkComplete(url string) error`
- `PendingArticles() ([]ArticleState, error)` — published but not complete (for re-publish on restart)

- **Notes**: This is separate from the existing `internal/database/` which tracks book recommendations.
  Both can coexist. State DB path is its own config env var (e.g., `STATE_DB_PATH`).
- **Test**: Unit test: insert, query, update a record

---

### [x] 5. Create FreshRSS source primitive
- **File**: `cmd/source-freshrss/main.go`
- **Action**: Wrap existing `services.FreshRSS`. On trigger (subscribe to `TopicPipelineTrigger`),
  fetch starred items, skip already-completed URLs (check state), publish `RawArticle` to
  `TopicArticlesRaw` for each new article. Subscribe to `TopicArticlesComplete` to mark done.

Structure:
```go
// Subscribe to trigger → fetch starred items → filter via state → publish RawArticle per item
// Subscribe to completion events → MarkComplete in state DB
// Run as long-running process (not batch — stays alive waiting for MQTT messages)
```

Config env vars (all prefixed, no conflicts with other primitives):
- All existing `FRESHRSS_*` vars
- `MQTT_BROKER_URL` (e.g., `tcp://localhost:1883`)
- `MQTT_CLIENT_ID` (default: `"minerva-source-freshrss"`)
- `STATE_DB_PATH` (default: `./data/freshrss-state.db`)

- **Pattern**: Follow `cmd/minerva/main.go` for config loading, signal handling, logger setup.
  The MQTT subscription loop replaces the `pipeline.Run()` call.
- **Test**: Run against local Mosquitto + FreshRSS, verify `RawArticle` messages appear on bus

---

### [x] 6. Create Miniflux service + source primitive
- **Files**: `internal/services/miniflux.go`, `cmd/source-miniflux/main.go`

**`internal/services/miniflux.go`**:
- Wrap `miniflux.app/v2` client library (same pattern as `freshrss.go`)
- Struct: `Miniflux` with config, client, logger
- Method: `GetStarredEntries() ([]MinifuxItem, error)` — fetch starred/saved entries
- `MinifuxItem`: `{ID, URL, Title, PublishedAt}` — same shape as `FreshRSSItem` conceptually
- Config: `MINIFLUX_BASE_URL`, `MINIFLUX_API_KEY`
- Follow existing service pattern: constructor + `SetLogger`

**`cmd/source-miniflux/main.go`**:
- Identical structure to `cmd/source-freshrss/main.go`
- Publishes same `RawArticle` message format (downstream doesn't know the source)
- `Source` field in Envelope set to `"miniflux"`

- **Notes**: Both source primitives publish identical `RawArticle` structs. The contract is the
  message type, not the source. This is the core composability principle.
- **Test**: Verify Miniflux entries appear as `RawArticle` messages on `minerva/articles/raw`

---

### [x] 7. Create extractor primitive
- **File**: `cmd/extractor/main.go`
- **Action**: Subscribe to `TopicArticlesRaw`. For each `RawArticle`, call
  `services.ContentExtractor.ExtractContent(url)`. Publish `ExtractedArticle` to
  `TopicArticlesExtracted`.

Error handling: if extraction fails, log and do not publish (article is lost for this run;
it will be re-published next run since state never marks it complete).

Config env vars:
- All existing `EXTRACTOR_*` vars
- `MQTT_BROKER_URL`, `MQTT_CLIENT_ID` (default: `"minerva-extractor"`)

- **Pattern**: The existing `extractContent()` in `pipeline.go:149` is the reference logic.
  Strip the DB checks (handled by source primitive now) and the dry-run logic.
- **Test**: Publish a test `RawArticle` message, verify `ExtractedArticle` appears with clean content

---

### [x] 8. Create analyzer primitive (Ollama)
- **File**: `cmd/analyzer/main.go`
- **Action**: Subscribe to `TopicArticlesExtracted`. For each `ExtractedArticle`, call
  `services.Ollama.ProcessContentMultiPass()`. Build `AnalyzedArticle` from the multi-pass results.
  Publish to `TopicArticlesAnalyzed`. **Drop the `Content` field** — it's large and not needed
  downstream.

Config env vars:
- All existing `OLLAMA_*` vars
- `MQTT_BROKER_URL`, `MQTT_CLIENT_ID` (default: `"minerva-analyzer"`)
- `DEBUG_OLLAMA` (write debug files to `./debug/` as today)

- **Pattern**: The keyword dedup + multi-pass assembly logic in `pipeline.go:220` is the reference.
- **Test**: Publish a test `ExtractedArticle`, verify `AnalyzedArticle` appears with domain/keywords

---

### [x] 9. Create book-search primitive (OpenLibrary)
- **File**: `cmd/book-search/main.go`
- **Action**: Subscribe to `TopicArticlesAnalyzed`. For each `AnalyzedArticle`, call
  `services.OpenLibrary.SearchBooks(keywords, insights)`. Publish `BookCandidates` to
  `TopicBooksCandidates`.

Config env vars:
- All existing `OPENLIBRARY_*` vars (currently none, base URL hardcoded — may need adding)
- `MQTT_BROKER_URL`, `MQTT_CLIENT_ID` (default: `"minerva-book-search"`)

- **Pattern**: `generateBookRecommendations()` in `pipeline.go:302` is the reference. Strip the
  DB reads/writes — those move to the notifier.
- **Test**: Publish a test `AnalyzedArticle` with known keywords, verify `BookCandidates` appears

---

### [x] 10. Create koha-check primitive
- **File**: `cmd/koha-check/main.go`
- **Action**: Subscribe to `TopicBooksCandidates`. For each `BookCandidates` message, call
  `services.Koha.CheckOwnership()` for each book. Publish `CheckedBooks` to `TopicBooksChecked`.

Config env vars:
- All existing `KOHA_*` vars
- `MQTT_BROKER_URL`, `MQTT_CLIENT_ID` (default: `"minerva-koha-check"`)

- **Pattern**: `checkKohaOwnership()` in `pipeline.go:392` is the reference. Strip the DB writes.
- **Test**: Publish a test `BookCandidates` message, verify `CheckedBooks` appears with
  new/owned split

---

### [x] 11. Create notifier primitive
- **File**: `cmd/notifier/main.go`
- **Action**: Subscribe to `TopicBooksChecked`. For each `CheckedBooks` message:
  1. Call `services.Ntfy.NotifyPipelineComplete()` with the books and article info
  2. Write book recommendations to SQLite (the existing `internal/database/` schema)
  3. Publish `ArticleComplete` to `TopicArticlesComplete` (source primitives listen for this)

This primitive is the **only one with DB write access** for final output. It uses the existing
`internal/database/` package unchanged.

Config env vars:
- All existing `NTFY_*` vars
- All existing `DATABASE_*` vars (for final book recommendation storage)
- `MQTT_BROKER_URL`, `MQTT_CLIENT_ID` (default: `"minerva-notifier"`)

- **Pattern**: Final steps of `pipeline.go` + `ntfy.go` notification logic.
- **Test**: Publish a test `CheckedBooks` message, verify Ntfy notification sent, DB row written,
  `ArticleComplete` published

---

### [x] 12. Add Mosquitto to docker-compose.yml
- **File**: `docker-compose.yml`
- **Action**: Add Mosquitto service for local development.

```yaml
mosquitto:
  image: eclipse-mosquitto:2
  ports:
    - "1883:1883"
    - "9001:9001"
  volumes:
    - ./deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf
  networks:
    - minerva_network
```

Also create `deploy/mosquitto/mosquitto.conf`:
```
listener 1883
allow_anonymous true
persistence true
persistence_location /mosquitto/data/
log_dest stdout
```

- **Test**: `docker compose up mosquitto` starts without errors; `mosquitto_pub` / `mosquitto_sub`
  can communicate

---

### [x] 13. Update Makefile
- **File**: `Makefile`
- **Action**: Add build and run targets for each primitive.

New targets:
```makefile
build-primitives: ## Build all primitive binaries
    go build -o build/source-freshrss   ./cmd/source-freshrss/
    go build -o build/source-miniflux   ./cmd/source-miniflux/
    go build -o build/extractor         ./cmd/extractor/
    go build -o build/analyzer          ./cmd/analyzer/
    go build -o build/book-search       ./cmd/book-search/
    go build -o build/koha-check        ./cmd/koha-check/
    go build -o build/notifier          ./cmd/notifier/

run-extractor: ## Run extractor primitive locally
    ./build/extractor

run-analyzer: ## Run analyzer primitive locally
    ./build/analyzer

# etc. for each primitive
```

Also add `trigger`: a helper target that publishes to `minerva/pipeline/trigger` via
`mosquitto_pub` to kick off a local run without waiting for the nightly schedule.

---

### [x] 14. Remove monolithic pipeline
- **Files to delete**: `internal/pipeline/pipeline.go`, `cmd/minerva/main.go` (and directory)
- **Action**: Once all primitives are tested end-to-end, delete the old pipeline and entry point.
  The monolith is superseded.
- **Notes**: Do this last. Keep it until integration testing of all primitives is complete.
  Consider a git tag `pre-mqtt-refactor` before deletion as a restore point.
- **Test**: `go build ./...` still compiles (no orphaned imports). All primitives start and
  communicate correctly end-to-end via local Mosquitto.

---

## Completion Criteria
- [ ] All 7 primitives compile and start successfully
- [ ] End-to-end run: publish to `minerva/pipeline/trigger`, watch article flow through all stages,
  Ntfy notification received, book recommendations in SQLite
- [ ] FreshRSS and Miniflux source primitives both produce valid `RawArticle` messages
- [ ] Failure recovery: kill the analyzer mid-run, restart it, confirm articles re-process on next
  trigger
- [ ] `docker compose up` starts Mosquitto + all primitives cleanly
- [ ] Old `cmd/minerva/` and `internal/pipeline/` deleted
- [ ] `go build ./...` and `go vet ./...` pass cleanly

---

## Context Updates
When implementation is complete, add to CLAUDE.md:
- Minerva is now a distributed system of MQTT primitives connected via Mosquitto
- Message contract lives in `internal/mqtt/` — topics.go and messages.go are the shared API
- Source primitives (freshrss, miniflux) each maintain their own SQLite state DB for dedup
- Only the notifier primitive writes to the book recommendations DB (`internal/database/`)
- All primitives are long-running processes (not batch) — they subscribe and wait for MQTT messages
- Pipeline is triggered by publishing to `minerva/pipeline/trigger`
- New source types: create `internal/services/<source>.go` + `cmd/source-<name>/main.go`,
  publish `RawArticle` to `minerva/articles/raw` — downstream is automatically composed
- Nomad per-primitive deployment: separate future plan
