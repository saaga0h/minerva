# Plan: Expand Search to OpenLibrary, arXiv, and Semantic Scholar
## Created: 2026-03-17
## Complexity: opus
## Recommended implementation model: sonnet

## Context

Currently the pipeline has a single `book-search` primitive that searches OpenLibrary.
The goal is to:
1. Rename it to `search-openlibrary` to make its scope explicit
2. Add two new parallel search primitives: `search-arxiv` and `search-semantic-scholar`

All three subscribe to `TopicArticlesAnalyzed` — the MQTT broker fans out the message
to each simultaneously, giving true parallelism with zero coordination needed. Each
independently publishes `BookCandidates` to `TopicBooksCandidates`. Downstream primitives
(`koha-check`, `notifier`) already handle multiple `BookCandidates` messages per article.

**Key design decisions:**
- `BookCandidate.OpenLibraryKey` → renamed to `SourceKey` with URI-style prefixed values:
  `openlibrary:/works/OL123W`, `arxiv:2301.00001`, `s2:abc123def456`
  This makes the field self-describing — no separate `SearchSource` field needed.
- DB column `openlibrary_key` → `source_key` via SQLite `ALTER TABLE ... RENAME COLUMN`
- **Semantic Scholar** (not Google Scholar) — free official API at `api.semanticscholar.org`,
  good coverage of CS/physics/ML papers, no auth required for basic use, no TOS risk
- arXiv API returns Atom XML at `export.arxiv.org/api/query` — parsed with `encoding/xml`
- Semantic Scholar API returns JSON
- koha-check falls back gracefully when ISBN is empty (papers won't have ISBNs) — no change needed
- Each primitive is fully independent: no dependency on other primitives running

## Prerequisites
- [x] Mosquitto running (`make mosquitto`)
- [x] Existing `make build-primitives` succeeds before starting

## Tasks

### 1. Rename `BookCandidate.OpenLibraryKey` → `SourceKey` in MQTT messages
- **File**: `internal/mqtt/messages.go`
- **Action**: Rename field `OpenLibraryKey string \`json:"openlibrary_key"\`` to `SourceKey string \`json:"source_key"\`` in `BookCandidate`
- **Pattern**: Existing `BookCandidate` struct at line 51
- **Test**: `go build ./...` passes with no errors
- **Notes**: This breaks `cmd/book-search/main.go` and `cmd/notifier/main.go` — fix in subsequent tasks

### 2. Update `cmd/book-search/main.go` to use `SourceKey` with `openlibrary:` prefix
- **File**: `cmd/book-search/main.go`
- **Action**: Change `OpenLibraryKey: rec.OpenLibraryKey` → `SourceKey: "openlibrary:" + rec.OpenLibraryKey`
- **Pattern**: BookCandidate construction at line 87–98
- **Test**: `go build ./cmd/book-search/` passes

### 3. Update `cmd/notifier/main.go` to use `SourceKey`
- **File**: `cmd/notifier/main.go`
- **Action**: In `persistRecommendations`, change `OpenLibraryKey: b.OpenLibraryKey` → `SourceKey: b.SourceKey` (in the `database.BookRecommendation` construction at line 178–191)
- **Pattern**: Existing field mapping at lines 178–191
- **Test**: `go build ./cmd/notifier/` passes
- **Notes**: `database.BookRecommendation.OpenLibraryKey` still exists — the notifier just maps `b.SourceKey` into it. The DB struct rename is a separate task.

### 4. Rename `database.BookRecommendation.OpenLibraryKey` → `SourceKey` and add DB migration
- **File**: `internal/database/database.go`
- **Action**:
  1. Rename struct field `OpenLibraryKey string \`json:"openlibrary_key"\`` → `SourceKey string \`json:"source_key"\``
  2. Add migration to `migrate()` slice: `ALTER TABLE book_recommendations RENAME COLUMN openlibrary_key TO source_key`
  3. Update `SaveBookRecommendation` query and `Exec` call to use `source_key` / `rec.SourceKey`
  4. Update `GetUncheckedRecommendations` scan to use `rec.SourceKey`
- **Pattern**: Existing `migrate()` at line 98, `SaveBookRecommendation` at line 257, `GetUncheckedRecommendations` at line 359
- **Test**: `go build ./internal/database/` passes; `make run-notifier` starts without DB errors
- **Notes**: SQLite `ALTER TABLE ... RENAME COLUMN` requires SQLite ≥ 3.25.0 (2018). This is safe on any modern system. The migration uses `CREATE TABLE IF NOT EXISTS` pattern already used — add the `ALTER TABLE` after existing DDL migrations. Since migrations run unconditionally on startup, wrap with a no-op guard: SQLite ignores `ALTER TABLE` if the column name already matches, but it will error if the old column is gone. Use `CREATE TABLE IF NOT EXISTS` style — add a separate migration entry that runs `ALTER TABLE book_recommendations RENAME COLUMN openlibrary_key TO source_key` but guard it by checking if column exists first, OR simply add it to the slice and accept that it errors on second run. **Better approach**: use `IF NOT EXISTS` equivalent — SQLite doesn't support it for RENAME COLUMN, so run it in a helper that ignores "no such column" errors. See implementation note below.

  **Implementation note for migration safety**: wrap the rename in a helper:
  ```go
  // Run rename migration; ignore error if column already renamed
  db.conn.Exec(`ALTER TABLE book_recommendations RENAME COLUMN openlibrary_key TO source_key`)
  ```
  SQLite returns an error if `openlibrary_key` doesn't exist (already renamed). Run this outside the fatal-on-error loop, or add a separate helper `runOptionalMigration` that silently ignores errors.

### 5. Update `internal/config/config.go` — add ArXiv and SemanticScholar config structs
- **File**: `internal/config/config.go`
- **Action**:
  1. Add `ArXiv ArXivConfig` field to `Config` struct
  2. Add `SemanticScholar SemanticScholarConfig` field to `Config` struct
  3. Define:
     ```go
     type ArXivConfig struct {
         Timeout int `json:"timeout" env:"ARXIV_TIMEOUT" default:"30"`
     }
     type SemanticScholarConfig struct {
         Timeout int    `json:"timeout" env:"SEMANTIC_SCHOLAR_TIMEOUT" default:"30"`
         APIKey  string `json:"api_key" env:"SEMANTIC_SCHOLAR_API_KEY"` // optional, raises rate limit
     }
     ```
  4. Populate both in `Load()` using existing `getEnv`/`getEnvInt` helpers
- **Pattern**: `OpenLibraryConfig` at line 60–62, populated at lines 137–139
- **Test**: `go build ./internal/config/` passes

### 6. Create `internal/services/arxiv.go`
- **File**: `internal/services/arxiv.go` (new file)
- **Action**: Implement `ArXiv` service with `SearchPapers(keywords []string, insights string) ([]PaperRecommendation, error)`
  - Query: `https://export.arxiv.org/api/query?search_query=all:{terms}&max_results=10&sortBy=relevance`
  - Parse Atom XML response (`encoding/xml`)
  - Return `[]PaperRecommendation` where:
    ```go
    type PaperRecommendation struct {
        Title     string
        Authors   string  // comma-joined first 3 authors
        ArXivID   string  // e.g. "2301.00001"
        Published string  // year only
        Abstract  string
        URL       string  // https://arxiv.org/abs/{id}
        Relevance float64
    }
    ```
  - Use same keyword selection logic as OpenLibrary (`selectBestConcepts` pattern — copy/adapt)
  - `SetLogger` method following existing service pattern
- **Pattern**: `internal/services/openlibrary.go` — HTTP client setup, `SetLogger`, logger field, request with User-Agent header
- **Test**: `go build ./internal/services/` passes
- **Notes**:
  - arXiv rate limit: ~3 requests/second. Add `time.Sleep(350 * time.Millisecond)` between retries if needed
  - arXiv query syntax: `all:keyword` for full-text, `ti:keyword` for title. Use `all:` for broader results
  - ArXiv IDs in entry URLs look like `http://arxiv.org/abs/2301.00001v1` — strip version suffix for stable ID
  - Search term construction: join top 3–4 keywords with `+AND+` for precision, fall back to `+OR+` if empty results

### 7. Create `internal/services/semanticscholar.go`
- **File**: `internal/services/semanticscholar.go` (new file)
- **Action**: Implement `SemanticScholar` service with `SearchPapers(keywords []string, insights string) ([]PaperRecommendation, error)`
  - Query: `https://api.semanticscholar.org/graph/v1/paper/search?query={terms}&fields=title,authors,year,externalIds,abstract,url&limit=10`
  - Parse JSON response
  - Response struct:
    ```go
    type s2SearchResponse struct {
        Data []s2Paper `json:"data"`
    }
    type s2Paper struct {
        PaperID     string            `json:"paperId"`
        Title       string            `json:"title"`
        Authors     []s2Author        `json:"authors"`
        Year        int               `json:"year"`
        Abstract    string            `json:"abstract"`
        URL         string            `json:"url"`
        ExternalIDs map[string]string `json:"externalIds"`
    }
    type s2Author struct {
        Name string `json:"name"`
    }
    ```
  - Return `[]PaperRecommendation` (same type as arXiv service — share the type)
  - If `APIKey` config is set, add `x-api-key` header
  - `SetLogger` method
- **Pattern**: Same as arXiv service above; `internal/services/openlibrary.go` for HTTP client pattern
- **Test**: `go build ./internal/services/` passes
- **Notes**:
  - `PaperRecommendation` type should be defined once — put it in a new file `internal/services/papers.go` or define in `arxiv.go` and reference from `semanticscholar.go`
  - Semantic Scholar free tier: 1 req/second without API key, 10/second with key
  - `externalIds` may contain `"ArXiv"` key — use that for SourceKey prefix if available: `arxiv:{id}`, else `s2:{paperId}`

### 8. Rename `cmd/book-search/` → `cmd/search-openlibrary/`
- **File**: `cmd/search-openlibrary/main.go` (new), `cmd/book-search/` (delete)
- **Action**:
  1. Copy `cmd/book-search/main.go` to `cmd/search-openlibrary/main.go`
  2. Update default `MQTT_CLIENT_ID` from `"minerva-book-search"` to `"minerva-search-openlibrary"`
  3. Update startup log message from `"Book-search primitive ready"` to `"search-openlibrary primitive ready"`
  4. Delete `cmd/book-search/` directory
- **Pattern**: `cmd/book-search/main.go` — identical structure
- **Test**: `go build ./cmd/search-openlibrary/` passes
- **Notes**: The `SourceKey` change from Task 2 must be done before or together with this rename

### 9. Create `cmd/search-arxiv/main.go`
- **File**: `cmd/search-arxiv/main.go` (new file)
- **Action**: New primitive following exact pattern of `cmd/search-openlibrary/main.go`:
  - Load config, create MQTT client (`client_id: "minerva-search-arxiv"`)
  - Create `services.ArXiv` using `cfg.ArXiv`
  - Subscribe to `TopicArticlesAnalyzed`
  - In goroutine: call `arxiv.SearchPapers(msg.Keywords, msg.Insights)`
  - Map `[]PaperRecommendation` → `[]BookCandidate`:
    - `Title`: paper title
    - `Author`: authors string
    - `ISBN`, `ISBN13`, `Publisher`, `CoverURL`: empty string / zero
    - `PublishYear`: parsed from `Published` year string
    - `SourceKey`: `"arxiv:" + rec.ArXivID`
    - `Relevance`: `rec.Relevance`
  - Publish `BookCandidates` to `TopicBooksCandidates`
- **Pattern**: `cmd/search-openlibrary/main.go` — identical structure
- **Test**: `go build ./cmd/search-arxiv/` passes

### 10. Create `cmd/search-semantic-scholar/main.go`
- **File**: `cmd/search-semantic-scholar/main.go` (new file)
- **Action**: Same structure as `cmd/search-arxiv/main.go`:
  - `client_id: "minerva-search-semantic-scholar"`
  - Use `services.SemanticScholar` with `cfg.SemanticScholar`
  - Map `PaperRecommendation` → `BookCandidate` same as arXiv task above
  - `SourceKey`: `"s2:" + rec.PaperID` (or `"arxiv:" + externalArXivID` if available — handled inside the service)
- **Pattern**: `cmd/search-arxiv/main.go`
- **Test**: `go build ./cmd/search-semantic-scholar/` passes

### 11. Update Makefile
- **File**: `Makefile`
- **Action**:
  1. Replace `book-search` with `search-openlibrary` in `build-primitives`, `build-dev`, `.PHONY`, `run:` echo block
  2. Replace `run-book-search` target with `run-search-openlibrary`
  3. Add `search-arxiv` and `search-semantic-scholar` to `build-primitives` and `build-dev`
  4. Add `run-search-arxiv` and `run-search-semantic-scholar` run targets
  5. Update `.PHONY` list
- **Pattern**: Existing `run-book-search` / `build-primitives` targets at lines 149–186
- **Test**: `make build-primitives` succeeds; `make help` shows all new targets

### 12. Final build verification
- **Action**: Run `make build-primitives` and confirm all 9 binaries build without errors:
  `source-freshrss`, `source-miniflux`, `source-linkwarden`, `extractor`, `analyzer`,
  `search-openlibrary`, `search-arxiv`, `search-semantic-scholar`, `koha-check`, `notifier`
- **Test**: All binaries present in `./build/`

## Completion Criteria
- `make build-primitives` succeeds with all 9 binaries (was 8)
- `cmd/book-search/` directory is gone; `cmd/search-openlibrary/` exists
- `BookCandidate.SourceKey` is used everywhere; `OpenLibraryKey` field is gone
- DB migration renames column `openlibrary_key` → `source_key` safely on first run
- `search-arxiv` and `search-semantic-scholar` subscribe to `TopicArticlesAnalyzed` and publish to `TopicBooksCandidates`
- All three search primitives are independent — each works if the others are not running
- `go build ./...` passes with no errors

## New Constraints or Gotchas
- **DB migration is one-shot but must be error-tolerant**: The `ALTER TABLE ... RENAME COLUMN` will error on second startup once the column is already renamed. Run it outside the fatal migration loop, or in a helper that ignores errors — do not use `log.Fatal` for this specific migration.
- **arXiv rate limit**: 3 req/sec. The service should not be called in a tight loop. One call per `AnalyzedArticle` message is fine — the goroutine-per-message pattern naturally spaces them out.
- **Semantic Scholar without API key**: 1 req/sec limit. Same reasoning applies — one call per article is fine.
- **`PaperRecommendation` shared type**: Define it once in `internal/services/papers.go` (or `arxiv.go`) to avoid duplicate type declarations between the two paper services.
- **arXiv XML namespace**: arXiv Atom feed uses namespace `http://www.w3.org/2005/Atom`. The XML entry IDs are URLs like `http://arxiv.org/abs/2301.00001v2` — strip the `v\d+` suffix and the base URL prefix to get the bare ID.
