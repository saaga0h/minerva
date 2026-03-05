# Plan: Migrate internal/state from SQLite to PostgreSQL
## Created: 2026-03-05
## Complexity: sonnet
## Recommended implementation model: sonnet

---

## Context

Source primitives (freshrss, miniflux, linkwarden) use `internal/state` (SQLite via go-sqlite3/CGo)
to track which article URLs have been published to the MQTT bus and which have completed the full
pipeline. This enables two things:
- **Dedup**: skip articles already completed on subsequent trigger runs
- **Crash recovery**: on startup, re-publish articles that entered the pipeline but never completed

We now have PostgreSQL (pgx/v5, no CGo) from the knowledge-base-storage plan. The `articles` table
already stores article state — adding `published_at` and `completed_at` columns consolidates article
lifecycle tracking into a single place and eliminates the SQLite state files.

**Net improvements over per-source SQLite:**
- `IsComplete` is now shared across all sources — if one source completed an article, all sources
  skip it on the next trigger (today each source has its own SQLite, so they can't see each other's
  completed articles)
- Cross-source dedup on `published_at` via the UNIQUE(url) constraint already on the articles table
- One less CGo dependency (go-sqlite3 still required for the notifier's recommendations DB)
- `PendingArticles(source)` filters by source so each primitive only re-publishes its own pending
  articles, not another source's incomplete articles

**What does NOT change:**
- `internal/database/` (notifier SQLite recommendations DB) — untouched
- `DatabaseConfig` in config.go — still used by the notifier
- `STORE_ENABLED` flag — still controls `cmd/store`; source primitives always connect to PostgreSQL
- `cmd/store` pure observer pattern — unchanged
- MQTT message types and topic names — unchanged

---

## Prerequisites
- [x] PostgreSQL added to docker-compose (knowledge-base-storage plan Task 1)
- [x] `internal/store/` package exists with `DB`, `UpsertArticleContent`, etc.
- [x] `articles` table has `article_id`, `url`, `title`, `source` columns

---

## Tasks

### 1. Extend articles table schema in internal/store/db.go
- **File**: `internal/store/db.go`
- **Action**: Add `published_at TIMESTAMPTZ` and `completed_at TIMESTAMPTZ` to the `CREATE TABLE IF
  NOT EXISTS articles` statement (after `source TEXT`). Also add two `ALTER TABLE` statements in
  `migrate()` after the CREATE TABLE block for idempotent migration on existing DBs:

```go
// After the CREATE TABLE block in migrate():
alterStmts := []string{
    `ALTER TABLE articles ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ`,
    `ALTER TABLE articles ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ`,
}
for _, stmt := range alterStmts {
    if _, err := db.pool.Exec(ctx, stmt); err != nil {
        return fmt.Errorf("migrate alter: %w", err)
    }
}
```

Also add a partial index for efficient `PendingArticles` queries:
```sql
CREATE INDEX IF NOT EXISTS idx_articles_pending
    ON articles (source, published_at)
    WHERE completed_at IS NULL;
```

- **Pattern**: Follow the existing `migrate()` structure in `internal/store/db.go`
- **Test**: `go build ./internal/store/...` compiles

---

### 2. Add ArticleID() helper and state methods to internal/store/articles.go
- **File**: `internal/store/articles.go`
- **Action**: Add the following to the existing file. Do NOT create a new file.

**`ArticleID` function** (moved from `internal/state/state.go`, same logic):
```go
import "crypto/sha256"

// ArticleID returns a stable, short ID for a URL: the first 16 hex chars of its SHA256.
// This is the canonical article identifier used across all pipeline stages.
func ArticleID(url string) string {
    sum := sha256.Sum256([]byte(url))
    return fmt.Sprintf("%x", sum[:8])
}
```

**`ArticleState` struct** (for PendingArticles return):
```go
type ArticleState struct {
    URL         string
    ArticleID   string
    Title       string
    PublishedAt time.Time
}
```

**State methods on `*DB`**:

```go
// IsComplete returns true if the full pipeline has finished for this URL.
func (db *DB) IsComplete(ctx context.Context, url string) (bool, error) {
    var exists bool
    err := db.pool.QueryRow(ctx,
        `SELECT EXISTS(SELECT 1 FROM articles WHERE url = $1 AND completed_at IS NOT NULL)`,
        url,
    ).Scan(&exists)
    return exists, err
}

// MarkPublished records that this URL has been published to the MQTT bus.
// Uses ON CONFLICT to preserve the original published_at if already set (idempotent).
func (db *DB) MarkPublished(ctx context.Context, url, articleID, title, source string) error {
    _, err := db.pool.Exec(ctx, `
        INSERT INTO articles (article_id, url, title, source, published_at)
        VALUES ($1, $2, $3, $4, now())
        ON CONFLICT (url) DO UPDATE
            SET published_at = COALESCE(articles.published_at, EXCLUDED.published_at)
    `, articleID, url, title, source)
    return err
}

// MarkCompleteByArticleID records that the full pipeline has finished for this article.
func (db *DB) MarkCompleteByArticleID(ctx context.Context, articleID string) error {
    _, err := db.pool.Exec(ctx,
        `UPDATE articles SET completed_at = now() WHERE article_id = $1`,
        articleID,
    )
    return err
}

// PendingArticles returns articles published by the given source that never completed.
// Called on startup to re-publish articles from a previous incomplete run.
func (db *DB) PendingArticles(ctx context.Context, source string) ([]ArticleState, error) {
    rows, err := db.pool.Query(ctx, `
        SELECT url, article_id, title, published_at
        FROM articles
        WHERE source = $1 AND published_at IS NOT NULL AND completed_at IS NULL
    `, source)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var result []ArticleState
    for rows.Next() {
        var a ArticleState
        if err := rows.Scan(&a.URL, &a.ArticleID, &a.Title, &a.PublishedAt); err != nil {
            return nil, err
        }
        result = append(result, a)
    }
    return result, rows.Err()
}
```

- **Pattern**: Follow existing pgx patterns in `internal/store/articles.go` (pool.Exec, pool.QueryRow)
- **Test**: `go build ./internal/store/...` compiles

---

### 3. Update cmd/source-freshrss/main.go
- **File**: `cmd/source-freshrss/main.go`
- **Action**: Replace `internal/state` with `internal/store`.

Replace state DB init block:
```go
// BEFORE:
stateDBPath := getEnv("STATE_DB_PATH", "./data/freshrss-state.db")
stateDB, err := state.New(stateDBPath)
if err != nil {
    log.WithError(err).Fatal("Failed to open state DB")
}
defer stateDB.Close()

// AFTER:
ctx := context.Background()
stateDB, err := store.New(ctx, cfg.Store.DSN)
if err != nil {
    log.WithError(err).Fatal("Failed to connect to PostgreSQL")
}
defer stateDB.Close()
```

In the `TopicArticlesComplete` handler goroutine:
```go
// BEFORE:
stateDB.MarkCompleteByArticleID(msg.ArticleID)
// AFTER:
stateDB.MarkCompleteByArticleID(context.Background(), msg.ArticleID)
```

In `fetchAndPublish` signature and body:
```go
// BEFORE:
func fetchAndPublish(log *logrus.Logger, freshRSS *services.FreshRSS, stateDB *state.DB, mqttClient *mqttclient.Client)

// AFTER:
func fetchAndPublish(log *logrus.Logger, freshRSS *services.FreshRSS, stateDB *store.DB, mqttClient *mqttclient.Client)
```

Replace `stateDB.IsComplete(url)` with `stateDB.IsComplete(context.Background(), url)`.
Replace `state.ArticleID(url)` with `store.ArticleID(url)`.
Replace `stateDB.MarkPublished(url, articleID, item.Title)` with
`stateDB.MarkPublished(context.Background(), url, articleID, item.Title, "freshrss")`.

In the `PendingArticles` startup block:
```go
// BEFORE:
pending, err := stateDB.PendingArticles()
// AFTER:
pending, err := stateDB.PendingArticles(context.Background(), "freshrss")
```

Update imports: remove `"github.com/saaga0h/minerva/internal/state"`, add
`"github.com/saaga0h/minerva/internal/store"` and `"context"`.

- **Test**: `go build ./cmd/source-freshrss/...` compiles

---

### 4. Update cmd/source-miniflux/main.go
- **File**: `cmd/source-miniflux/main.go`
- **Action**: Same pattern as Task 3. Miniflux does not use `config.Load()` — read `STORE_DSN`
  directly from env:

```go
storeDSN := getEnv("STORE_DSN", "postgres://minerva:minerva@localhost:5432/minerva")
ctx := context.Background()
stateDB, err := store.New(ctx, storeDSN)
if err != nil {
    log.WithError(err).Fatal("Failed to connect to PostgreSQL")
}
defer stateDB.Close()
```

All other replacements follow the same pattern as Task 3, with source name `"miniflux"`.

- **Test**: `go build ./cmd/source-miniflux/...` compiles

---

### 5. Update cmd/source-linkwarden/main.go
- **File**: `cmd/source-linkwarden/main.go`
- **Action**: Same pattern as Task 3. Linkwarden uses `config.Load()`, so use `cfg.Store.DSN`.
  Source name `"linkwarden"`.

- **Test**: `go build ./cmd/source-linkwarden/...` compiles

---

### 6. Delete internal/state/ package
- **File**: `internal/state/state.go`
- **Action**: Delete the file (and the directory). Verify no remaining imports:
  `grep -r "internal/state" --include="*.go" .` should return nothing.

- **Test**: `go build ./...` compiles without reference to `internal/state`

---

### 7. Update CLAUDE.md
- **File**: `CLAUDE.md`
- **Action**:
  - Update the constraints section: source primitives (freshrss, miniflux, linkwarden) now require
    PostgreSQL to start — add this alongside the existing `cmd/store` PostgreSQL constraint
  - Update "Adding a source primitive" convention: replace `internal/state/` reference with
    `internal/store` for dedup (call `store.IsComplete`, `store.MarkPublished`,
    `store.MarkCompleteByArticleID`, `store.PendingArticles`)
  - Remove the note "CGo requirement (go-sqlite3) still exists for the notifier and state
    primitives" — update to "notifier only"
  - No other changes needed

- **Test**: Read-through for consistency

---

## Completion Criteria
- [ ] `go build ./...` compiles cleanly
- [ ] `grep -r "internal/state" --include="*.go" .` returns nothing
- [ ] All 3 source primitives start and connect to PostgreSQL (`make pg` must be running)
- [ ] Trigger fires, articles published — `SELECT url, source, published_at, completed_at FROM articles` shows rows
- [ ] Second trigger run: completed articles are skipped; pending articles are re-published
- [ ] `go vet ./...` passes

---

## New Constraints or Gotchas

- **Source primitives now require PostgreSQL**: freshrss, miniflux, and linkwarden will fatal on
  startup if `STORE_DSN` is unreachable. `make pg` must run before these primitives start.
- **`STATE_DB_PATH` env var is obsolete**: any `.env` files referencing `STATE_DB_PATH` can have
  that line removed. The SQLite state files under `./data/*-state.db` can be deleted.
- **`go-sqlite3` CGo is now notifier-only**: `CGO_ENABLED=1` is still required for `make build`
  due to the notifier's `internal/database` SQLite. The state SQLite is gone.
- **Shared completed state is a behavior change**: previously each source had its own SQLite, so
  completing an article via freshrss would not prevent miniflux from re-publishing it. Now all
  sources share `completed_at` — an article completed by any path is skipped by all sources.
  This is intentional and an improvement.
