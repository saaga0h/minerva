# Plan: LLM Re-ranking of Book/Paper Recommendations
## Created: 2026-03-18
## Complexity: opus
## Recommended implementation model: sonnet

## Context

Search primitives (especially OpenLibrary) return noisy results. Positional "relevance"
scores (1.0 for rank 1, 0.92 for rank 2, etc.) are meaningless — a bad query's top result
still scores 1.0. A post-processing pass asks Ollama "does this book actually relate to
this article?" and writes a real 0–1 semantic score with a one-sentence reason, enabling
the notifier digest and any future UI to filter on meaningful signal.

**Architecture decision: one-shot batch binary, not a primitive.**
Runs on-demand or via Nomad cron, processes all unranked rows, then exits. No MQTT.
This keeps it simple and schedulable without broker dependency.

**Scoring design:**
- Single Ollama call per (article, book) pair — lightweight, not multi-pass
- Prompt gives: article title, domain, summary, top 5 concepts; book title + author
- Response: `{"score": 0.0-1.0, "reason": "one sentence"}`
- Temperature 0.1 for consistency (same model as analyzer, `cfg.Ollama`)
- Ollama calls serialized with `sync.Mutex` (same pattern as analyzer)

**DB columns added to `book_recommendations`:**
- `llm_score REAL` — NULL = not yet ranked; 0.0–1.0 after ranking
- `llm_reason TEXT` — one-sentence explanation from LLM
- `llm_ranked_at DATETIME` — timestamp of scoring; used as "unranked" sentinel

**Threshold:** `RERANKER_MIN_SCORE` env var (default 0.65). Not used by the reranker
itself — it scores everything. Digest and future queries filter on this threshold.

**Batch cap:** `RERANKER_BATCH_SIZE` env var (default 200). Safety limit per run to
avoid unexpectedly long Nomad jobs.

## Prerequisites
- [ ] `make build-primitives` succeeds on current state
- [ ] At least one pipeline run has populated `book_recommendations` and `analyzed_articles`

## Tasks

### 1. Add DB columns and query methods to database.go
- **File**: `internal/database/database.go`
- **Action**:

  **a) Silent column migrations** — add after the existing silent migrations block:
  ```go
  db.conn.Exec(`ALTER TABLE book_recommendations ADD COLUMN llm_score REAL`)
  db.conn.Exec(`ALTER TABLE book_recommendations ADD COLUMN llm_reason TEXT`)
  db.conn.Exec(`ALTER TABLE book_recommendations ADD COLUMN llm_ranked_at DATETIME`)
  ```

  **b) Add `RerankCandidate` struct** (near the other types at the top):
  ```go
  type RerankCandidate struct {
      BookID          int
      BookTitle       string
      BookAuthor      string
      SourceKey       string
      ArticleTitle    string
      ArticleDomain   string
      ArticleSummary  string
      ArticleConcepts []string // parsed from JSON column
  }
  ```

  **c) Add `GetUnrankedRecommendations(limit int) ([]RerankCandidate, error)`**:
  JOIN `book_recommendations br` with `analyzed_articles aa` ON
  `aa.article_id = br.envelope_article_id`, WHERE `br.llm_ranked_at IS NULL`,
  ORDER BY `br.id ASC`, LIMIT `limit`.
  Parse `aa.concepts` JSON column into `[]string` using `json.Unmarshal`.
  For rows where `envelope_article_id` is NULL or no `analyzed_articles` match,
  still return them but with empty ArticleDomain/Summary/Concepts — reranker
  will score conservatively.

  **d) Add `UpdateLLMScore(bookID int, score float64, reason string) error`**:
  `UPDATE book_recommendations SET llm_score=?, llm_reason=?, llm_ranked_at=CURRENT_TIMESTAMP WHERE id=?`

- **Pattern**: Existing silent migrations at line ~200; `GetRecommendationsSince` for JOIN pattern
- **Test**: `go build ./internal/database/...`

### 2. Add `ScoreRelevance` to internal/services/ollama.go
- **File**: `internal/services/ollama.go`
- **Action**: Add new exported method after `SetLogger`:
  ```go
  type RelevanceScore struct {
      Score  float64 `json:"score"`
      Reason string  `json:"reason"`
  }

  func (o *Ollama) ScoreRelevance(articleTitle, domain, summary string, concepts []string, bookTitle, bookAuthor string) (float64, string, error)
  ```
  Prompt (keep it tight — LLM doesn't need the full article):
  ```
  You are evaluating whether a book or paper is relevant to a magazine/news article.

  Article: "<title>"
  Domain: <domain>
  Summary: <summary>
  Key concepts: <concept1>, <concept2>, ... (max 5)

  Book/Paper: "<title>" by <author>

  Score the relevance from 0.0 (completely irrelevant) to 1.0 (highly relevant).
  A score >= 0.65 means a reader of this article would genuinely benefit from this book.

  Output ONLY valid JSON:
  {"score": 0.0, "reason": "one sentence explanation"}

  JSON:
  ```
  Use `o.generateCompletion` with temperature override of 0.1:
  - Add a helper `generateWithTemperature(prompt string, temp float64) (string, error)` that
    sets `Options.Temperature = temp` — or just set `0.1` inline by building the request
    directly (see `generateCompletion` at line ~360 for the pattern).
  - Parse response with `o.extractJSON` then `json.Unmarshal` into `RelevanceScore`.
  - On parse failure, return `(0.0, "parse error", err)` — do not skip, let caller decide.

- **Pattern**: `classifyArticle` at line ~137 for prompt + `generateCompletion` + `extractJSON` + unmarshal pattern
- **Test**: `go build ./internal/services/...`
- **Notes**: Keep concepts capped at 5 to limit prompt length. If `summary` is empty,
  use article title only. Temperature 0.1 is important — consistency matters more than creativity here.

### 3. Create cmd/reranker/main.go
- **File**: `cmd/reranker/main.go` (new)
- **Action**: One-shot binary:
  ```
  load config → open DB → init Ollama → fetch unranked batch → score each → exit
  ```
  - Load config with `config.Load(*configPath)` (same flag pattern as other cmds)
  - `batchSize := getEnvInt("RERANKER_BATCH_SIZE", 200)`
  - `candidates, err := db.GetUnrankedRecommendations(batchSize)` — fatal if error
  - If `len(candidates) == 0`: log "No unranked recommendations, nothing to do" and exit 0
  - Iterate: for each candidate, call `ollama.ScoreRelevance(...)` (under `sync.Mutex`),
    then `db.UpdateLLMScore(c.BookID, score, reason)`, log each result at DEBUG level
  - After loop: log summary `"Reranking complete" scored=N failed=M` at INFO level, exit 0
  - On individual score error: log warn, call `db.UpdateLLMScore(c.BookID, 0.0, "scoring failed")`
    so the row gets `llm_ranked_at` set and won't be retried forever
  - No MQTT, no signal handling, no goroutines — simple sequential loop

- **Pattern**: `cmd/analyzer/main.go` for config/Ollama/mutex setup; but no MQTT subscription
- **Test**: `go build ./cmd/reranker/...`
- **Notes**: `getEnvInt` helper needed locally (same as other cmds). The binary exits after
  one batch — Nomad reruns it on schedule to drain the queue over time.

### 4. Update Makefile
- **File**: `Makefile`
- **Action**:
  1. Add `reranker` to `.PHONY` line
  2. Add to `build-primitives`: `go build -o $(BUILD_DIR)/reranker ./cmd/reranker/`
  3. Add to `build-dev`: same with `-gcflags="all=-N -l"`
  4. Add `run-reranker` target: `$(BUILD_DIR)/reranker -config .env.dev`
  5. Add `rerank` target (alias): same as `run-reranker`, with echo
  6. Add `reranker` to the `run:` echo block

- **Pattern**: `run-storage` target; `trigger` for the alias pattern
- **Test**: `make build-primitives` produces 12 binaries

### 5. Final build verification
- **Action**: `go build ./...` clean; `make build-primitives` produces 12 binaries in `./build/`
- **Test**: `ls ./build/ | wc -l` shows 12 (excluding old `book-search` and `minerva` leftovers)

## Completion Criteria
- `book_recommendations` has `llm_score`, `llm_reason`, `llm_ranked_at` columns
- `./build/reranker -config .env.dev` runs, scores unranked rows, exits cleanly
- After a run, `SELECT COUNT(*) FROM book_recommendations WHERE llm_ranked_at IS NULL` = 0
  (for rows that had `analyzed_articles` data; rows without it also get scored at 0.0)
- `go build ./...` clean

## New Constraints or Gotchas
- **Reranker is a one-shot binary** — no MQTT, no persistent process. Nomad runs it as a
  batch job. `make rerank` = `make run-reranker` for local dev.
- **Requires Ollama running** — same constraint as analyzer. Will time out if Ollama is
  unreachable (uses `cfg.Ollama.Timeout` per call, default 300s).
- **Writes to same SQLite file as storage** — safe when run after the pipeline has settled.
  Don't run simultaneously with storage (no cross-process mutex). Nomad schedule: run
  reranker N minutes after pipeline trigger to give storage time to finish.
- **Rows without `analyzed_articles` match** (`envelope_article_id` NULL or old pre-storage
  data) — these get scored with empty context. LLM will likely score them low (0.0–0.3).
  This is acceptable: they're old data without semantic metadata.
- **`RERANKER_MIN_SCORE` (default 0.65)** is a filter threshold, not enforced by the
  reranker itself. The digest query in `GetRecommendationsSince` should be updated to
  filter `WHERE llm_score >= ? OR llm_ranked_at IS NULL` (pass threshold as param) —
  this is a follow-on task when the digest is built out.
