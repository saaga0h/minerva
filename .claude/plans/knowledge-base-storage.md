# Plan: Knowledge Base — Persistent Article Analysis & Work Catalog
## Created: 2026-03-01
## Complexity: opus
## Recommended implementation model: sonnet

---

## Context

Minerva's pipeline is currently lossy. The analyzer computes a rich 3-pass `MultiPassResult`
(entity extraction: facilities, people, locations, phenomena; classification type; separate
related topics list) but discards most of it when publishing `AnalyzedArticle`. No book/paper
abstracts are fetched. All intermediate intelligence evaporates after each run. The notifier's
SQLite only records the final recommendation outcome.

This plan adds a **persistent knowledge base** (PostgreSQL) that captures what the pipeline
knows — article analysis and discovered works — in a form suitable for later LLM context,
cross-article analysis, and future validation primitives.

**Scope boundaries:**
- Lock in the MQTT contract for multi-source work discovery (the interface that future arXiv,
  Google Books, etc. primitives will share)
- Deliver minimal, correct initial schema — articles, works, article_works
- Deliver `cmd/store` as a pure observer primitive
- Schema will evolve; this is the foundation
- `book-enricher` (OpenLibrary abstract fetching), validation primitives, LLM context export
  are explicitly **out of scope** for this plan

---

## Key Architectural Decisions

### PostgreSQL — not SQLite
- FTS via `tsvector GENERATED ALWAYS AS ... STORED` on article content and work abstracts
- `JSONB` for arrays (keywords, concepts, entities, subjects) with GIN indexing
- `pgvector` extension available as zero-migration-cost future addition for semantic search
- `pgx/v5` driver is **pure Go — no CGo** (unlike go-sqlite3)
- One more docker-compose service alongside existing Mosquitto

### WorkCandidate — unified message type for books and papers
`BookCandidate` and `BookCandidates` are renamed and extended to cover all work types.
Multiple search primitives (OpenLibrary, future arXiv, etc.) all publish `WorkCandidates`
to the same topic. Downstream primitives (koha-check, store) handle `work_type` routing.

`referenceId` format: `source-name:source-item-id`
- OpenLibrary book: `openlibrary:/works/OL12345W`
- arXiv paper: `arxiv:2301.07041`

### Canonical dedup in the knowledge base
Works are deduplicated in PostgreSQL by a computed `canonical_id`:
- `isbn13:{value}` — if ISBN-13 is known (cross-source dedup for books)
- `doi:{value}` — if DOI is known (cross-source dedup for papers)
- `arxiv:{value}` — if arXiv ID is known without DOI
- `ref:{reference_id}` — fallback when no canonical identifier available

Single `UNIQUE(canonical_id)` constraint. `reference_ids JSONB` array accumulates all
source-specific keys that map to the same work. The same book found by OpenLibrary and a
future source upserts into one record.

### Store primitive is a pure observer
`cmd/store` subscribes to pipeline topics but publishes nothing back to the pipeline.
The pipeline runs identically whether or not `store` is connected. No startup dependency
is created for the pipeline itself — only for the knowledge base to be populated.

### koha-check routes by work_type
koha-check subscribes to `minerva/works/candidates` and skips entries where
`work_type != "book"`. Papers flow through to the store but not to Koha.

---

## PostgreSQL Schema

```sql
-- Full article analysis record
CREATE TABLE IF NOT EXISTS articles (
    article_id      TEXT PRIMARY KEY,
    url             TEXT UNIQUE NOT NULL,
    title           TEXT NOT NULL,
    source          TEXT,
    content         TEXT,
    domain          TEXT,
    article_type    TEXT,
    summary         TEXT,
    keywords        JSONB,
    concepts        JSONB,
    related_topics  JSONB,
    entities        JSONB,
    insights        TEXT,
    content_tsv     tsvector GENERATED ALWAYS AS (
        to_tsvector('english',
            coalesce(title, '') || ' ' ||
            coalesce(content, '') || ' ' ||
            coalesce(summary, ''))
    ) STORED,
    extracted_at    TIMESTAMPTZ,
    analyzed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ DEFAULT now()
);

-- Books and papers, deduplicated by canonical_id
CREATE TABLE IF NOT EXISTS works (
    work_id         SERIAL PRIMARY KEY,
    canonical_id    TEXT UNIQUE NOT NULL,
    reference_ids   JSONB DEFAULT '[]',
    sources         JSONB DEFAULT '[]',
    work_type       TEXT NOT NULL,
    title           TEXT NOT NULL,
    authors         JSONB,
    publisher       TEXT,
    publish_year    INT,
    isbn            TEXT,
    isbn13          TEXT,
    issn            TEXT,
    doi             TEXT,
    arxiv_id        TEXT,
    cover_url       TEXT,
    abstract        TEXT,
    subjects        JSONB,
    abstract_tsv    tsvector GENERATED ALWAYS AS (
        to_tsvector('english',
            coalesce(title, '') || ' ' ||
            coalesce(abstract, ''))
    ) STORED,
    first_seen_at   TIMESTAMPTZ DEFAULT now(),
    enriched_at     TIMESTAMPTZ
);

-- Junction: which works were found for which articles
CREATE TABLE IF NOT EXISTS article_works (
    id              SERIAL PRIMARY KEY,
    article_id      TEXT REFERENCES articles(article_id) ON DELETE CASCADE,
    work_id         INT  REFERENCES works(work_id) ON DELETE CASCADE,
    search_source   TEXT,
    relevance       REAL,
    owned_in_koha   BOOLEAN DEFAULT FALSE,
    created_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE (article_id, work_id)
);
```

---

## Prerequisites
- [x] PostgreSQL 16 accessible (added to docker-compose in Task 1)
- [x] Existing 7 primitives tested end-to-end before this plan starts (MQTT refactor complete)

---

## Tasks

### 1. Add PostgreSQL to docker-compose.yml and .env.example
- **File**: `docker-compose.yml`, `.env.example`
- **Action**: Add `postgres:16` service to docker-compose. Add `STORE_DSN` and `STORE_ENABLED`
  to `.env.example`.

`docker-compose.yml` addition:
```yaml
postgres:
  image: postgres:16
  environment:
    POSTGRES_DB: minerva
    POSTGRES_USER: minerva
    POSTGRES_PASSWORD: minerva
  volumes:
    - postgres_data:/var/lib/postgresql/data
  ports:
    - "5432:5432"
  networks:
    - minerva_network
  healthcheck:
    test: ["CMD-SHELL", "pg_isready -U minerva"]
    interval: 5s
    timeout: 5s
    retries: 5
```

Also add `postgres_data` to the `volumes:` section at the bottom of docker-compose.yml.

`.env.example` additions:
```
STORE_DSN=postgres://minerva:minerva@localhost:5432/minerva
STORE_ENABLED=false
```

- **Test**: `docker compose up postgres -d` starts without error; `pg_isready` passes

---

### 2. Add pgx/v5 dependency
- **File**: `go.mod`, `go.sum`
- **Action**: `go get github.com/jackc/pgx/v5@latest`
- **Notes**: pgx/v5 is pure Go — no CGo. Use `pgxpool` for connection pooling in the store
  primitive. Do not use `database/sql` adapter; use `pgx` native API directly.
- **Test**: `go mod tidy` completes without errors

---

### 3. Add StoreConfig to internal/config/config.go
- **File**: `internal/config/config.go`
- **Action**: Add `StoreConfig` struct and `Store StoreConfig` field to `Config`.

```go
type StoreConfig struct {
    DSN     string
    Enabled bool
}
```

Parse from env vars:
```go
Store: StoreConfig{
    DSN:     getEnv("STORE_DSN", "postgres://minerva:minerva@localhost:5432/minerva"),
    Enabled: getEnv("STORE_ENABLED", "false") == "true",
},
```

- **Pattern**: Follow the existing env var parsing pattern in `config.go` (getEnv helper)
- **Test**: `go build ./internal/config/...` compiles

---

### 4. Extend AnalyzedArticle in internal/mqtt/messages.go
- **File**: `internal/mqtt/messages.go`
- **Action**: Add `ArticleEntities` struct and three new fields to `AnalyzedArticle`.
  This is purely additive — existing subscribers that don't know these fields ignore them.

Add new type:
```go
// ArticleEntities carries the named entity extraction from analyzer Pass 2.
type ArticleEntities struct {
    Facilities []string `json:"facilities"`
    People     []string `json:"people"`
    Locations  []string `json:"locations"`
    Phenomena  []string `json:"phenomena"`
}
```

Add to `AnalyzedArticle`:
```go
type AnalyzedArticle struct {
    Envelope
    URL           string          `json:"url"`
    Title         string          `json:"title"`
    Domain        string          `json:"domain"`
    ArticleType   string          `json:"article_type"`    // NEW: Pass1 type field
    Summary       string          `json:"summary"`
    Keywords      []string        `json:"keywords"`
    Concepts      []string        `json:"concepts"`
    RelatedTopics []string        `json:"related_topics"`  // NEW: Pass3 related_topics (separate from keywords)
    Entities      ArticleEntities `json:"entities"`        // NEW: full Pass2 entity extraction
    Insights      string          `json:"insights"`
}
```

- **Test**: `go build ./internal/mqtt/...` compiles

---

### 5. Replace BookCandidate/BookCandidates with WorkCandidate/WorkCandidates
- **File**: `internal/mqtt/messages.go`
- **Action**: Delete `BookCandidate`, `BookCandidates`. Replace with `WorkCandidate`,
  `WorkCandidates`. Rename `CheckedBooks` → `CheckedWorks`, update its fields.

```go
// WorkCandidate represents a single discovered book or paper from any search source.
type WorkCandidate struct {
    ReferenceID  string   `json:"reference_id"`   // source:item-id, e.g. "openlibrary:/works/OL123W"
    SearchSource string   `json:"search_source"`  // "openlibrary" | "arxiv" | etc.
    WorkType     string   `json:"work_type"`      // "book" | "paper"
    Title        string   `json:"title"`
    Authors      []string `json:"authors"`
    ISBN         string   `json:"isbn"`
    ISBN13       string   `json:"isbn13"`
    ISSN         string   `json:"issn"`
    DOI          string   `json:"doi"`
    ArXivID      string   `json:"arxiv_id"`
    PublishYear  int      `json:"publish_year"`
    Publisher    string   `json:"publisher"`
    CoverURL     string   `json:"cover_url"`
    Relevance    float64  `json:"relevance"`
}

// WorkCandidates is published by any search primitive to TopicWorksCandidates.
type WorkCandidates struct {
    Envelope
    ArticleTitle string          `json:"article_title"`
    ArticleURL   string          `json:"article_url"`
    Works        []WorkCandidate `json:"works"`
}

// CheckedWorks is published by the koha-check primitive to TopicWorksChecked.
type CheckedWorks struct {
    Envelope
    ArticleTitle string          `json:"article_title"`
    ArticleURL   string          `json:"article_url"`
    NewWorks     []WorkCandidate `json:"new_works"`   // not in Koha (or non-book type)
    OwnedWorks   []OwnedWork     `json:"owned_works"` // found in Koha catalog
}

// OwnedWork is a book confirmed present in the Koha library catalog.
type OwnedWork struct {
    Title  string `json:"title"`
    Author string `json:"author"` // primary author from Koha record
    KohaID string `json:"koha_id"`
}
```

Delete the old `OwnedBook` type from messages.go (the `database.OwnedBook` in internal/database/
is a separate type and is not affected).

- **Test**: `go build ./internal/mqtt/...` compiles

---

### 6. Update topics.go
- **File**: `internal/mqtt/topics.go`
- **Action**: Rename `TopicBooksCandidates` → `TopicWorksCandidates`,
  rename `TopicBooksChecked` → `TopicWorksChecked`.

```go
const (
    TopicArticlesRaw       = "minerva/articles/raw"
    TopicArticlesExtracted = "minerva/articles/extracted"
    TopicArticlesAnalyzed  = "minerva/articles/analyzed"
    TopicWorksCandidates   = "minerva/works/candidates"  // renamed from books/candidates
    TopicWorksChecked      = "minerva/works/checked"     // renamed from books/checked
    TopicArticlesComplete  = "minerva/articles/complete"
    TopicPipelineTrigger   = "minerva/pipeline/trigger"
)
```

- **Notes**: The topic strings on the wire change (`books/` → `works/`). All primitives using
  old topic strings will be updated in subsequent tasks. All primitives must be restarted
  together after this change — mixed old/new topic strings will break message flow.
- **Test**: `go build ./internal/mqtt/...` compiles

---

### 7. Update cmd/analyzer/main.go — populate new AnalyzedArticle fields
- **File**: `cmd/analyzer/main.go`
- **Action**: Populate `ArticleType`, `RelatedTopics`, and `Entities` in the `AnalyzedArticle`
  message using data from `multiPass` that is currently discarded.

In the goroutine that builds `out := mqttclient.AnalyzedArticle{...}`, add:
```go
out := mqttclient.AnalyzedArticle{
    // ... existing fields ...
    ArticleType:   multiPass.Pass1.Type,
    RelatedTopics: multiPass.Pass3.RelatedTopics,
    Entities: mqttclient.ArticleEntities{
        Facilities: multiPass.Pass2.Facilities,
        People:     multiPass.Pass2.People,
        Locations:  multiPass.Pass2.Locations,
        Phenomena:  multiPass.Pass2.Phenomena,
    },
}
```

The `Insights` field can stay as-is (the "Domain: X. Related: Y" string) — it is not removed.

- **Pattern**: `cmd/analyzer/main.go:111` is where `out` is currently built
- **Test**: `go build ./cmd/analyzer/...` compiles

---

### 8. Update cmd/book-search/main.go — publish WorkCandidates
- **File**: `cmd/book-search/main.go`
- **Action**: Replace `BookCandidates`/`BookCandidate` with `WorkCandidates`/`WorkCandidate`.
  Subscribe to `TopicWorksCandidates` (no change on subscribe side — book-search subscribes
  to `TopicArticlesAnalyzed`). Update publish to use `TopicWorksCandidates`.

When building candidates from `services.BookRecommendation`:
```go
candidates = append(candidates, mqttclient.WorkCandidate{
    ReferenceID:  rec.OpenLibraryKey,  // e.g. "/works/OL12345W"
    SearchSource: "openlibrary",
    WorkType:     "book",
    Authors:      []string{rec.Author},
    ISBN:         rec.ISBN,
    ISBN13:       rec.ISBN13,
    // ... rest of fields ...
})
```

Publish to `mqttclient.TopicWorksCandidates`.

- **Pattern**: `cmd/book-search/main.go:84–113` is the conversion and publish block
- **Test**: `go build ./cmd/book-search/...` compiles

---

### 9. Update cmd/koha-check/main.go — subscribe to WorkCandidates, skip non-books
- **File**: `cmd/koha-check/main.go`
- **Action**: Update to unmarshal `WorkCandidates` instead of `BookCandidates`. Subscribe to
  `TopicWorksCandidates`. Publish `CheckedWorks` to `TopicWorksChecked`. Add work_type filter:
  only send books to Koha; pass papers through directly to `NewWorks` without checking.

```go
var msg mqttclient.WorkCandidates
// ...
for _, work := range msg.Works {
    if work.WorkType != "book" {
        // non-books pass through unchecked
        newWorks = append(newWorks, work)
        continue
    }
    // existing Koha check logic using work.ISBN13, work.Title, etc.
}
```

When building `OwnedWork` from a Koha result, use `work.Authors[0]` (with len check) for
the `Author` field.

- **Test**: `go build ./cmd/koha-check/...` compiles

---

### 10. Update cmd/notifier/main.go — subscribe to CheckedWorks
- **File**: `cmd/notifier/main.go`
- **Action**: Update to unmarshal `CheckedWorks` instead of `CheckedBooks`. Subscribe to
  `TopicWorksChecked`. Update all references from `NewBooks`/`OwnedBooks` to
  `NewWorks`/`OwnedWorks` and from `BookCandidate` to `WorkCandidate`.

The notifier writes to the existing `internal/database/` SQLite — leave that unchanged.
Map `WorkCandidate` fields to `database.BookRecommendation` fields:
- `Authors[0]` (with len check) → `Author`
- Other fields map directly

- **Test**: `go build ./cmd/notifier/...` compiles

---

### 11. Create internal/store/ package
- **Files**: `internal/store/db.go`, `internal/store/articles.go`, `internal/store/works.go`,
  `internal/store/article_works.go`
- **Action**: PostgreSQL repository layer using `pgxpool`. Implements the schema above.

**`db.go`**:
```go
package store

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
    pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*DB, error) {
    pool, err := pgxpool.New(ctx, dsn)
    // ... error handling ...
    db := &DB{pool: pool}
    if err := db.migrate(ctx); err != nil { ... }
    return db, nil
}

func (db *DB) Close() { db.pool.Close() }
```

`migrate()` runs the CREATE TABLE / CREATE INDEX statements from the schema above using
`IF NOT EXISTS` — idempotent on every startup.

**`articles.go`**:
```go
// UpsertArticleContent — called when store sees minerva/articles/extracted
func (db *DB) UpsertArticleContent(ctx context.Context, articleID, url, title, source, content string, extractedAt time.Time) error

// UpsertArticleAnalysis — called when store sees minerva/articles/analyzed
func (db *DB) UpsertArticleAnalysis(ctx context.Context, a ArticleAnalysis) error
// ArticleAnalysis holds all AnalyzedArticle fields
```

Use `INSERT INTO articles (...) ON CONFLICT (article_id) DO UPDATE SET ...` for upsert.

**`works.go`**:
```go
// canonicalID computes the dedup key from a WorkCandidate:
//   isbn13:{x} | doi:{x} | arxiv:{x} | ref:{reference_id}
func canonicalID(w WorkInput) string

// UpsertWork — upserts by canonical_id, appends reference_id and source to JSONB arrays
// Returns the internal work_id
func (db *DB) UpsertWork(ctx context.Context, w WorkInput) (int64, error)
// WorkInput mirrors WorkCandidate fields
```

Use:
```sql
INSERT INTO works (canonical_id, reference_ids, sources, work_type, ...)
VALUES ($1, $2::jsonb, $3::jsonb, $4, ...)
ON CONFLICT (canonical_id) DO UPDATE SET
    reference_ids = (
        SELECT jsonb_agg(DISTINCT v)
        FROM jsonb_array_elements(works.reference_ids || EXCLUDED.reference_ids) v
    ),
    sources = (
        SELECT jsonb_agg(DISTINCT v)
        FROM jsonb_array_elements(works.sources || EXCLUDED.sources) v
    )
RETURNING work_id
```

**`article_works.go`**:
```go
// LinkArticleWork — creates article_works record; on conflict (article_id, work_id) do nothing
func (db *DB) LinkArticleWork(ctx context.Context, articleID string, workID int64, searchSource string, relevance float64) error

// MarkOwnedInKoha — sets owned_in_koha=true for a given article_id + work canonical_id
func (db *DB) MarkOwnedInKoha(ctx context.Context, articleID, canonicalID string) error
```

- **Pattern**: Follow `internal/state/state.go` for struct + constructor pattern; use pgx native
  API (not database/sql) — `pool.Exec`, `pool.QueryRow`, `pgx.CollectRows`
- **Test**: `go build ./internal/store/...` compiles

---

### 12. Create cmd/store/main.go
- **File**: `cmd/store/main.go`
- **Action**: Pure observer primitive. Subscribes to 4 topics. All handlers dispatch goroutines
  per the MQTT handler pattern in CLAUDE.md. Does not publish to any topic.

Subscribe to:
- `TopicArticlesExtracted` → `store.UpsertArticleContent()`
- `TopicArticlesAnalyzed` → `store.UpsertArticleAnalysis()`
- `TopicWorksCandidates` → for each work: `store.UpsertWork()` then `store.LinkArticleWork()`
- `TopicWorksChecked` → for each owned work: `store.MarkOwnedInKoha()`

Config env vars:
- `STORE_DSN` — PostgreSQL DSN
- `MQTT_BROKER_URL`, `MQTT_CLIENT_ID` (default: `"minerva-store"`)

Startup: if `STORE_DSN` is empty or connection fails, log fatal and exit — the store has no
meaning without its database.

**Concurrent writes**: `pgxpool` handles connection pooling internally. Multiple goroutines
can call store methods concurrently without additional mutexes (unlike the SQLite notifier).

- **Pattern**: Follow `cmd/notifier/main.go` for overall structure; goroutine-per-message
  pattern from CLAUDE.md. Do NOT copy the `sync.Mutex` pattern — pgxpool is concurrency-safe.
- **Test**: `go build ./cmd/store/...` compiles; run against local postgres, verify rows appear
  after a pipeline run

---

### 13. Update Makefile
- **File**: `Makefile`
- **Action**: Add `make pg`, `make run-store`; add `store` binary to `build-primitives`.

```makefile
pg: ## Start PostgreSQL via docker compose
	docker compose up postgres -d

run-store: ## Run store primitive locally
	go run ./cmd/store/ --config .env.dev

build-primitives: ## Build all primitive binaries
	# add store to the existing list:
	go build -o build/store ./cmd/store/
```

- **Test**: `make build-primitives` produces a `build/store` binary

---

### 14. Update CLAUDE.md
- **File**: `CLAUDE.md`
- **Action**: Add new constraints and startup order changes.

Add to **Constraints**:
- `cmd/store` is a pure observer — it does not publish to any pipeline topic. The pipeline
  runs normally without it. Start before trigger if knowledge base population is wanted.
- PostgreSQL must be running before `cmd/store` starts (`make pg`).
- `pgxpool` is concurrency-safe — no `sync.Mutex` needed around store calls (unlike SQLite
  in the notifier).

Add to **Gotchas**:
- MQTT topic rename: `minerva/books/candidates` → `minerva/works/candidates`,
  `minerva/books/checked` → `minerva/works/checked`. All primitives must be on new topics
  simultaneously — mixed old/new strings drop messages silently.
- `canonical_id` is the dedup key in the `works` table. Same book from two sources upserts
  into one record. The priority: isbn13 > doi > arxiv_id > reference_id.

Update the **Adding a source primitive** section to mention topic rename.

---

## Completion Criteria
- [ ] `go build ./...` compiles cleanly  ← pending: run `go mod tidy` first (Go binary unavailable during implementation)
- [ ] `make pg` starts PostgreSQL; `make mosquitto` starts Mosquitto
- [ ] All 8 primitives (7 existing + store) start without error
- [ ] End-to-end pipeline run: trigger fires, articles flow through all stages, `store` populates
  `articles`, `works`, `article_works` tables in PostgreSQL
- [ ] Same article run twice: store upserts (no duplicate rows)
- [ ] Same book found for two different articles: one `works` row, two `article_works` rows
- [ ] `koha-check` correctly skips works where `work_type != "book"`
- [ ] `cmd/notifier` builds and functions as before using the renamed `WorkCandidates`/`CheckedWorks`
- [ ] `go vet ./...` passes

---

## New Constraints or Gotchas

- **MQTT topic wire format changed**: `minerva/books/candidates` and `minerva/books/checked` no
  longer exist. Any external tooling or scripts that published/subscribed to those topics must be
  updated. `make trigger` is unaffected (publishes to `minerva/pipeline/trigger` which is unchanged).
- **pgx/v5 requires Go generics**: already satisfied by Go 1.24.0 in this project.
- **PostgreSQL startup order**: add `make pg` before primitives in any local dev runbook. In Nomad,
  PostgreSQL must be a dependency service before `store` task starts.
- **No CGo added**: `pgx/v5` is pure Go. The CGo requirement (`go-sqlite3`) still exists for the
  notifier and state primitives — unchanged.
