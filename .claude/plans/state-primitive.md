# Plan: State Primitive
## Created: 2026-03-22
## Complexity: sonnet
## Recommended implementation model: sonnet

## Context

The pipeline has no crash recovery. When a primitive fails mid-run, the entire pipeline
restarts from scratch on the next trigger — re-fetching, re-extracting, re-analyzing articles
that were already successfully processed. This wastes time and Ollama capacity.

Source primitives currently access Postgres directly (`internal/store`) to track which articles
have been published and completed. This couples them to the database and violates the MQTT-only
communication principle that keeps primitives decoupled.

## Design

A new `state` primitive that:

1. **Taps all pipeline topics** — subscribes to every stage topic and stores the raw JSON
   payload per `(article_id, topic)` in Postgres as JSONB. It does not parse message content.
   It is a transparent observer.

2. **On trigger** (`minerva/pipeline/trigger`) — queries all stored state rows, groups by
   `article_id`, and replays the most advanced stored message for each article onto its topic.
   This resumes incomplete articles exactly where they left off. New articles (no stored state)
   are untouched — sources publish them normally.

3. **On `ArticleComplete`** — deletes all stored state rows for that `article_id`. The article
   is done and needs no further tracking.

4. **Primitives stay dumb** — no skip logic, no DB access, no awareness of state. They process
   whatever arrives on their topic, whether from a source or a replay.

## Pipeline topic order (replay order)

```
1. minerva/articles/raw        ← source publishes here; replay triggers extractor
2. minerva/articles/extracted  ← extractor; replay triggers analyzer
3. minerva/articles/analyzed   ← analyzer; replay triggers all three search primitives
4. minerva/works/candidates    ← search primitives; replay triggers storage/koha-check
5. minerva/works/checked       ← koha-check; replay triggers storage (ArticleComplete)
```

`minerva/articles/complete` is the terminal event — causes state deletion, not storage.

## Schema

Single table in Postgres, separate from the knowledge base (`internal/store`):

```sql
CREATE TABLE IF NOT EXISTS pipeline_state (
    article_id  TEXT        NOT NULL,
    topic       TEXT        NOT NULL,
    payload     JSONB       NOT NULL,
    stored_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (article_id, topic)
);

CREATE INDEX IF NOT EXISTS idx_pipeline_state_article_id ON pipeline_state (article_id);
CREATE INDEX IF NOT EXISTS idx_pipeline_state_stored_at  ON pipeline_state (stored_at);
```

Upsert on `(article_id, topic)` — later messages for the same stage overwrite earlier ones.
This handles retries gracefully.

## Files

### New package: `internal/statestore/`

**`internal/statestore/db.go`**
```go
package statestore

type DB struct { pool *pgxpool.Pool }

func New(ctx context.Context, dsn string) (*DB, error)
func (db *DB) Close()
func (db *DB) migrate(ctx context.Context) error
```

**`internal/statestore/state.go`**
```go
// UpsertState stores or overwrites the payload for (article_id, topic).
func (db *DB) UpsertState(ctx context.Context, articleID, topic string, payload []byte) error

// DeleteArticle removes all state rows for a completed article.
func (db *DB) DeleteArticle(ctx context.Context, articleID string) error

// PendingArticles returns all stored state rows, grouped by article_id,
// ordered by topic stage index (most advanced stage per article).
func (db *DB) PendingArticles(ctx context.Context) ([]PendingEntry, error)

type PendingEntry struct {
    ArticleID string
    Topic     string  // the most advanced topic this article has reached
    Payload   []byte
}
```

`PendingArticles` returns one row per article: the most advanced stage that was recorded.
Replay publishes to that topic, which resumes the pipeline from that point.

### New binary: `cmd/state/main.go`

```go
// Topics to observe — state primitive subscribes to all of these
var observedTopics = []string{
    mqttclient.TopicArticlesRaw,
    mqttclient.TopicArticlesExtracted,
    mqttclient.TopicArticlesAnalyzed,
    mqttclient.TopicWorksCandidates,
    mqttclient.TopicWorksChecked,
}

// stageOrder defines replay priority — higher index = more advanced stage.
// On replay, publish to the most advanced topic stored for each article.
var stageOrder = map[string]int{
    mqttclient.TopicArticlesRaw:       0,
    mqttclient.TopicArticlesExtracted: 1,
    mqttclient.TopicArticlesAnalyzed:  2,
    mqttclient.TopicWorksCandidates:   3,
    mqttclient.TopicWorksChecked:      4,
}
```

**Subscribe to each observed topic:**
```go
for _, topic := range observedTopics {
    topic := topic
    mqttClient.Subscribe(topic, func(payload []byte) {
        data := make([]byte, len(payload))
        copy(data, payload)
        go func() {
            // Extract article_id from the JSON envelope without full unmarshal
            articleID := extractArticleID(data)
            if articleID == "" {
                return
            }
            db.UpsertState(ctx, articleID, topic, data)
        }()
    })
}
```

**Extract article_id without full unmarshal:**
```go
func extractArticleID(payload []byte) string {
    var env struct {
        ArticleID string `json:"article_id"`
    }
    json.Unmarshal(payload, &env)
    return env.ArticleID
}
```

**Subscribe to trigger — replay pending articles:**
```go
mqttClient.Subscribe(mqttclient.TopicPipelineTrigger, func(_ []byte) {
    go func() {
        pending, err := db.PendingArticles(ctx)
        // publish each entry's payload back to its topic
        for _, e := range pending {
            mqttClient.PublishRaw(e.Topic, e.Payload)
        }
    }()
})
```

**Subscribe to ArticleComplete — delete state:**
```go
mqttClient.Subscribe(mqttclient.TopicArticlesComplete, func(payload []byte) {
    data := make([]byte, len(payload))
    copy(data, payload)
    go func() {
        articleID := extractArticleID(data)
        db.DeleteArticle(ctx, articleID)
    }()
})
```

### MQTT client — add PublishRaw

The existing `mqttClient.Publish` serializes a struct to JSON. We need a raw bytes variant:

**`internal/mqtt/client.go`** — add:
```go
// PublishRaw publishes a pre-serialized JSON payload to a topic.
func (c *Client) PublishRaw(topic string, payload []byte) error
```

## Makefile changes

```makefile
# In build-primitives and build-dev:
go build -o $(BUILD_DIR)/state ./cmd/state/

# New run target:
run-state: build-primitives ## Run state primitive (pipeline crash recovery)
    $(BUILD_DIR)/state -config .env.dev

# Add to .PHONY:
run-state
```

## Startup order

State primitive must connect **before** the trigger fires, like all other primitives.
Add `make run-state` to the startup instructions in CLAUDE.md.

State primitive should also start **before** source primitives so it can record
`ArticleComplete` messages and correctly delete state for any articles that complete
before state is connected (edge case on slow startup).

## Follow-up tasks (not part of this plan)

- Remove direct Postgres access from source primitives (`internal/store` dependency in
  `cmd/source-freshrss`, `cmd/source-miniflux`, `cmd/source-linkwarden`)
- Source primitives will rely solely on `ArticleComplete` for dedup going forward
- Consider whether `internal/store` package (source dedup) can be deleted once sources
  are decoupled

## Verification

1. `make build-primitives` succeeds with `state` binary
2. `make run-state` connects and logs "State primitive ready"
3. Run full pipeline trigger — verify state rows appear in Postgres:
   ```sql
   SELECT article_id, topic, stored_at FROM pipeline_state ORDER BY stored_at;
   ```
4. After pipeline completes, verify state rows are deleted:
   ```sql
   SELECT count(*) FROM pipeline_state; -- should be 0
   ```
5. Kill analyzer mid-run, restart all primitives, trigger again — verify articles
   resume from `minerva/articles/extracted` (not re-fetched from source)
