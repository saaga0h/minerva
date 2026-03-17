# Plan: Storage Primitive and Notifier Refactor
## Created: 2026-03-17
## Complexity: opus
## Recommended implementation model: sonnet

## Context

The pipeline currently has notifier doing two unrelated things: persisting results to SQLite
and sending ntfy notifications. With three parallel search sources, koha as optional enrichment,
and a planned daily digest, these concerns need to separate.

Additionally, the `Envelope` currently carries `Source` (primitive name e.g. "miniflux") but
not the source-native content ID. Adding `SourceID` (e.g. `"miniflux:12345"`, `"linkwarden:456"`,
`"freshrss:789"`) to the Envelope lets storage record the origin identity of each article, enabling
a future knowledge graph: books/papers found → article → original entry in source system.

**Target architecture:**

```
TopicBooksCandidates  ←── search-openlibrary, search-arxiv, search-semantic-scholar
    └── storage           stores candidates; publishes ArticleComplete

TopicBooksChecked     ←── koha-check
    └── storage           updates existing records with ownership info (enrichment, optional)

TopicArticlesAnalyzed ←── analyzer
    └── storage           stores LLM-extracted metadata (summary, keywords, concepts, insights, domain)

TopicPipelineDigest   ←── external (cron / make digest)
    └── notifier          reads storage DB, sends ntfy digest (no DB writes)

TopicArticlesComplete ←── storage (moved from notifier)
    └── source primitives mark articles done in state DBs
```

**Key decisions:**
- `SourceID string` added to `Envelope` with `omitempty` — backward compatible, ignored by
  primitives that don't set it. Format: `"<source>:<native_id>"` e.g. `"miniflux:12345"`
- `storage` owns all SQLite writes; notifier becomes stateless (no DB writes)
- `storage` subscribes to three topics: `TopicBooksCandidates`, `TopicBooksChecked`, `TopicArticlesAnalyzed`
- `storage` publishes `ArticleComplete` when `BookCandidates` arrives (search phase done;
  koha enrichment may still arrive later and update records)
- Koha enrichment is late-arriving and optional — storage accepts updates after `ArticleComplete`
- Notifier: subscribe `TopicPipelineDigest` → query storage DB (read-only) → send ntfy
- New topic: `TopicPipelineDigest = "minerva/pipeline/digest"`
- `make digest` publishes to this topic (mirrors `make trigger` pattern)
- Dedup in storage: upsert on `(article_id, source_key)` for book_recommendations
- Koha updates match on `(article_id, source_key)` — updates `owned_in_koha` + `koha_id`
- Analyzer data stored in new `analyzed_articles` table keyed by `article_id`
- Notifier uses `NOTIFIER_DIGEST_HOURS` env var (default 24) to window results from storage

## Prerequisites
- [ ] `make build-primitives` succeeds on current state

## Tasks

### 1. Add `SourceID` to `Envelope`
- **File**: `internal/mqtt/messages.go`
- **Action**: Add `SourceID string \`json:"source_id,omitempty"\`` to the `Envelope` struct
- **Pattern**: Existing `Envelope` struct at the top of the file
- **Test**: `go build ./internal/mqtt/...`
- **Notes**: `omitempty` makes this fully backward compatible — existing primitives that don't
  set it produce the same JSON as before

### 2. Populate `SourceID` in `source-freshrss`
- **File**: `cmd/source-freshrss/main.go`
- **Action**: In `fetchAndPublish`, set `SourceID: "freshrss:" + item.ID` on the Envelope.
  Also in the re-publish loop (pending articles): SourceID not available there — leave empty,
  the article was already processed once.
- **Pattern**: Existing `RawArticle` construction at line 157
- **Test**: `go build ./cmd/source-freshrss/...`

### 3. Populate `SourceID` in `source-miniflux`
- **File**: `cmd/source-miniflux/main.go`
- **Action**: In `fetchAndPublish`, set `SourceID: fmt.Sprintf("miniflux:%d", item.ID)` on the Envelope.
  Re-publish loop: leave SourceID empty (same reasoning as FreshRSS).
- **Pattern**: Existing `RawArticle` construction at line 162; `item.ID` is `int64`
- **Test**: `go build ./cmd/source-miniflux/...`

### 4. Populate `SourceID` in `source-linkwarden`
- **File**: `cmd/source-linkwarden/main.go`
- **Action**: In `fetchAndPublish`, set `SourceID: fmt.Sprintf("linkwarden:%d", item.ID)` on the Envelope.
  Re-publish loop: leave SourceID empty.
- **Pattern**: Existing `RawArticle` construction at line 161; `item.ID` is `int`
- **Test**: `go build ./cmd/source-linkwarden/...`

### 5. Add `TopicPipelineDigest` to topics.go
- **File**: `internal/mqtt/topics.go`
- **Action**: Add `TopicPipelineDigest = "minerva/pipeline/digest"`
- **Pattern**: Existing topic constants
- **Test**: `go build ./internal/mqtt/...`

### 6. Extend database.go — new table, upsert methods, koha update, digest query
- **File**: `internal/database/database.go`
- **Action**:

  **a) New `analyzed_articles` table** — add to `migrations` slice:
  ```sql
  CREATE TABLE IF NOT EXISTS analyzed_articles (
      article_id  TEXT PRIMARY KEY,  -- matches Envelope.ArticleID (hex SHA256 prefix)
      source_id   TEXT,              -- e.g. "miniflux:12345"
      url         TEXT NOT NULL,
      title       TEXT,
      domain      TEXT,
      summary     TEXT,
      keywords    TEXT,              -- JSON array string
      concepts    TEXT,              -- JSON array string
      insights    TEXT,
      created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
  )
  ```
  Add index: `CREATE INDEX IF NOT EXISTS idx_analyzed_articles_article_id ON analyzed_articles(article_id)`

  **b) Add `UNIQUE` index on `book_recommendations(article_id, source_key)`** (needed for upsert):
  ```sql
  CREATE UNIQUE INDEX IF NOT EXISTS idx_book_recs_article_source
      ON book_recommendations(article_id, source_key)
  ```
  Add to migrations slice — `CREATE UNIQUE INDEX IF NOT EXISTS` is idempotent.

  **c) Add `koha_id TEXT` column to `book_recommendations`** — wrap in silent-error helper:
  ```go
  db.conn.Exec(`ALTER TABLE book_recommendations ADD COLUMN koha_id TEXT`)
  ```
  Run after the main migration loop (same pattern as `source_key` rename).

  **d) Add `SaveAnalyzedArticle(a AnalyzedArticleRecord) error`**:
  ```go
  type AnalyzedArticleRecord struct {
      ArticleID string
      SourceID  string
      URL       string
      Title     string
      Domain    string
      Summary   string
      Keywords  []string
      Concepts  []string
      Insights  string
  }
  ```
  Use `INSERT OR REPLACE` (full replace on PK conflict — re-analysis overwrites cleanly).

  **e) Add `UpsertBookCandidate(articleDBID int, rec BookRecommendation) error`**:
  Use `INSERT INTO book_recommendations (...) ON CONFLICT(article_id, source_key) DO UPDATE SET
  title=excluded.title, author=excluded.author, ...` (all fields except `owned_in_koha`, `koha_id`).

  **f) Add `UpdateKohaOwnership(articleDBID int, sourceKey string, ownedInKoha bool, kohaID string) error`**:
  `UPDATE book_recommendations SET owned_in_koha=?, koha_id=? WHERE article_id=? AND source_key=?`

  **g) Add `GetRecommendationsSince(since time.Time) ([]DigestEntry, error)`**:
  ```go
  type DigestEntry struct {
      ArticleTitle string
      ArticleURL   string
      SourceID     string  // origin e.g. "miniflux:12345"
      BookTitle    string
      BookAuthor   string
      SourceKey    string  // "openlibrary:...", "arxiv:...", "s2:..."
      OwnedInKoha  bool
      Relevance    float64
      CreatedAt    time.Time
  }
  ```
  JOIN `book_recommendations` with `analyzed_articles` on `article_id` (TEXT), filter
  `book_recommendations.created_at >= since`.

- **Test**: `go build ./internal/database/...`
- **Notes**:
  - `article_id` in `book_recommendations` is currently INTEGER (FK to `articles.id`).
    `analyzed_articles` uses TEXT `article_id` (the hex SHA256 from Envelope).
    These are different — storage will need to join via `articles.url` or maintain both.
    **Simpler**: add a `TEXT envelope_article_id` column to `book_recommendations` for the
    hex ID, populated by storage alongside the integer FK. Or — since storage owns all writes
    now, just use `articles.url` as the join key in the digest query.
    **Decision**: store `Envelope.ArticleID` (hex) in a new `envelope_article_id TEXT` column
    on `book_recommendations`, add via silent migration. Use that to join with `analyzed_articles`.

### 7. Create `cmd/storage/main.go`
- **File**: `cmd/storage/main.go` (new)
- **Action**: New primitive:
  - Load config, open SQLite DB (`cfg.Database.Path`), connect MQTT (`client_id: "minerva-storage"`)
  - `sync.Mutex` for serialized DB writes
  - **Subscribe `TopicArticlesAnalyzed`**: unmarshal `AnalyzedArticle`, call `db.SaveAnalyzedArticle`,
    log result. No `ArticleComplete` here — search results haven't landed yet.
  - **Subscribe `TopicBooksCandidates`**: for each message—
    1. Upsert article into `articles` table (`SaveArticle` or `GetArticleIDByURL`)
    2. For each `BookCandidate`: call `UpsertBookCandidate` (with `envelope_article_id` = msg.ArticleID)
    3. Publish `ArticleComplete{Source: "storage"}`
  - **Subscribe `TopicBooksChecked`**: for each message—
    1. Get article DB ID by URL
    2. For each `OwnedBook`: call `UpdateKohaOwnership(articleDBID, kohaOwnedSourceKey, true, b.KohaID)`
       — need to match owned books back to a `source_key`. **Problem**: `OwnedBook` only has Title/Author/KohaID,
       no `source_key`. Koha matched by title/author, not source_key.
       **Solution**: store `UpdateKohaOwnershipByTitle(articleDBID int, title string, kohaID string) error`
       that does `UPDATE ... WHERE article_id=? AND title=?` — imprecise but workable since title+article
       is a reasonable match. Add this method instead of the source_key variant.
    3. For `NewBooks`: no update needed (already stored as `owned_in_koha=false` from candidates)
- **Pattern**: `cmd/notifier/main.go` — config, MQTT, goroutine, mutex, signal
- **Test**: `go build ./cmd/storage/...`

### 8. Strip notifier to digest-only
- **File**: `cmd/notifier/main.go`
- **Action**: Replace with stripped-down version:
  - Load config, open DB (`cfg.Database.Path`) read-only
  - Connect MQTT (`client_id: "minerva-notifier"`)
  - Ntfy service (unchanged)
  - Subscribe to `TopicPipelineDigest`
  - On trigger: compute `since = time.Now().Add(-digestHours)`, call `db.GetRecommendationsSince(since)`,
    format and send ntfy notification
  - `digestHours` from `getEnvDuration("NOTIFIER_DIGEST_HOURS", 24)`
  - Remove: DB writes, `persistRecommendations`, mutex, `TopicBooksChecked` subscription, `ArticleComplete` publish
- **Test**: `go build ./cmd/notifier/...`

### 9. Update Makefile
- **File**: `Makefile`
- **Action**:
  1. Add `storage` to `build-primitives`, `build-dev`, `.PHONY`
  2. Add `run-storage` target
  3. Add `digest` target: `mosquitto_pub -h localhost -p 1883 -t "minerva/pipeline/digest" -m "{}"`
  4. Add `digest` to `.PHONY`
  5. Update `run:` echo block to include `storage`
- **Pattern**: `trigger` target; existing run targets
- **Test**: `make build-primitives` produces 11 binaries

### 10. Final build verification
- **Action**: `make build-primitives` — confirm 11 binaries; `go build ./...` clean
- **Test**: All binaries in `./build/`

## Completion Criteria
- `go build ./...` clean, 11 binaries from `make build-primitives`
- `Envelope` has `SourceID`; all three source primitives populate it
- `cmd/storage/` subscribes to `TopicBooksCandidates`, `TopicBooksChecked`, `TopicArticlesAnalyzed`
- Storage publishes `ArticleComplete`; notifier no longer does
- Notifier subscribes only to `TopicPipelineDigest`; no DB writes
- `make digest` publishes to `minerva/pipeline/digest`
- `analyzed_articles` table exists with `article_id`, `source_id`, LLM fields
- `book_recommendations` has `envelope_article_id` and `koha_id` columns

## New Constraints or Gotchas
- **Storage publishes `ArticleComplete`, not notifier** — if storage is not running, source
  primitives never mark articles complete and re-publish on next trigger. Update CLAUDE.md.
- **`ArticleComplete` fires once per search-source per article** — `MarkCompleteByArticleID`
  is idempotent (UPDATE, not INSERT), so multiple fires are safe.
- **Notifier requires storage DB to exist** — opens same SQLite file storage writes. Start
  storage before running first digest.
- **`analyzed_articles.article_id` is the hex Envelope ArticleID** (TEXT), not the integer
  PK from the `articles` table. Join to `book_recommendations` via `envelope_article_id` column.
- **`make digest`** requires `mosquitto_pub` in PATH (same constraint as `make trigger`).
- **Koha ownership match is by title** within an article, not source_key — `OwnedBook` carries
  no `source_key`. This is a known approximation; title collisions within a single article are
  extremely unlikely.
