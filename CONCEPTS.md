# Minerva — Concepts

> _A pipeline that turns reading habits into a queryable library of thought._

**Date of initial implementation**: 2025–2026. The mechanisms described here are original to this project unless otherwise noted.

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [The Primitive Model — Decoupled Processing via MQTT](#2-the-primitive-model--decoupled-processing-via-mqtt)
3. [Article Identity — Stable ArticleID from URL](#3-article-identity--stable-articleid-from-url)
4. [Multi-Pass LLM Reasoning — From Text to Concepts](#4-multi-pass-llm-reasoning--from-text-to-concepts)
5. [Work Deduplication — Canonical IDs Across Sources](#5-work-deduplication--canonical-ids-across-sources)
6. [Cross-Source Completion Dedup — Shared Article State](#6-cross-source-completion-dedup--shared-article-state)
7. [Crash Recovery — The State Primitive](#7-crash-recovery--the-state-primitive)
8. [The Brief — Semantic Retrieval from a Personal Corpus](#8-the-brief--semantic-retrieval-from-a-personal-corpus)
9. [Consolidation and Notification Deduplication](#9-consolidation-and-notification-deduplication)
10. [Journal Integration — Externally-Directed Retrieval](#10-journal-integration--externally-directed-retrieval)
11. [Design Decisions and Roads Not Taken](#11-design-decisions-and-roads-not-taken)
12. [Relationships Between Concepts](#12-relationships-between-concepts)

---

## 1. Problem Statement

### What This Solves

A person who reads extensively — bookmarking articles in feed readers, starring items, saving links — generates an implicit record of intellectual interest. That record is fragmented across tools (FreshRSS, Miniflux, Linkwarden), is not analyzed for thematic content, and does not connect to what might be worth reading next in depth: books, papers, references.

The standard response is manual curation. This system takes a different position: the reading habit itself is the signal. A starred article is not interesting because it was starred — it is interesting because its conceptual content reveals what the reader is currently thinking about. The task is to extract that conceptual content, persist it in a form that is queryable, and use it to surface material (books, preprints, papers) that extends the current intellectual trajectory.

### Why the Approach is Non-Obvious

The non-obvious move is to treat an RSS item not as a document to be filed but as a trace of thinking. The pipeline does not store the article for retrieval — it extracts what the article reveals about the reader's interests at the moment of engagement, and uses that extraction to seed external searches.

A second non-obvious decision: rather than a monolith that does all of this in sequence, each transformation is performed by an independent process communicating via MQTT. The decomposition is not merely structural — it reflects a view that each stage (extraction, analysis, search, ownership verification, recommendation) has genuinely different operational characteristics and failure modes, and should be independently replaceable.

---

## 2. The Primitive Model — Decoupled Processing via MQTT

### What a Primitive Is

A **primitive** is a long-running binary that performs exactly one transformation: it subscribes to one or more MQTT topics, performs a bounded operation on each message, and publishes results to another topic. Primitives share no process memory, no function calls, and no direct database access to each other's state.

The pipeline consists of: source-freshrss, source-miniflux, source-linkwarden, extractor, analyzer, search-openlibrary, search-arxiv, search-semantic-scholar, search-openalex, koha-check, store, state, brief, consolidator, notifier.

### The Dispatch Invariant

Every MQTT message handler must dispatch its work into a goroutine immediately on receipt. This is not optional: paho's default delivery model (`OrderMatters=true`) blocks all subsequent message delivery while a handler runs. Any handler that calls `Publish` (which waits for broker acknowledgment) without a goroutine will deadlock the router after the first message.

The canonical pattern used throughout every primitive:

```go
client.Subscribe(topic, func(payload []byte) {
    data := make([]byte, len(payload))
    copy(data, payload)
    go func() {
        // all blocking work here
    }()
})
```

The `copy` is load-bearing: paho reuses the payload buffer; capturing the slice without copying produces a data race.

### CleanSession and the Startup Ordering Constraint

All primitives connect with `CleanSession=true` — no persistent sessions, no queued messages. Any message published to a topic with no active subscriber is permanently lost. This creates a hard startup ordering constraint: every primitive must be connected to the broker before the trigger fires. If `store` is not running when a search primitive publishes `WorkCandidates`, those candidates are dropped and the article never receives `ArticleComplete`.

This is an architectural choice, not a bug. Persistent sessions would require message ID tracking and redelivery logic that adds complexity. The ordering constraint is instead documented and enforced operationally.

### Why MQTT Rather Than HTTP or gRPC

HTTP requires a server endpoint per primitive. gRPC requires interface definitions and bidirectional awareness. MQTT requires only topic strings, which are defined in one place (`internal/mqtt/topics.go`) and are stable. A new primitive subscribes to an existing topic without modifying any other component. A primitive can be replaced, restarted, or scaled out without any other component being aware of it.

The latency introduced by the broker is acceptable for a pipeline that runs on article timescales (minutes to hours), not request timescales (milliseconds).

---

## 3. Article Identity — Stable ArticleID from URL

### The Mechanism

Every article in the system has a stable, source-independent identifier computed as the first 16 hexadecimal characters of the SHA256 hash of the article's URL:

```
ArticleID = hex(SHA256(url))[:16]
```

This produces a 64-bit identifier that is:
- **Deterministic**: the same URL always produces the same ID, regardless of which source primitive found it or when.
- **Source-independent**: if FreshRSS and Linkwarden both bookmark the same URL, they produce the same ArticleID and the dedup logic (§6) handles the collision.
- **Compact**: 16 hex characters (8 bytes) fits in a TEXT column and is legible in logs.

### Why Not a Database Sequence

A database-assigned integer ID requires a round-trip to the database before publishing. The ArticleID needs to be computable by source primitives before any database interaction — it is embedded in the MQTT Envelope and used throughout the pipeline, including by the state primitive for crash recovery. A sequence ID would require every source to first claim an ID from the database, adding latency and a failure mode.

The collision probability for 16 hex characters across a personal reading corpus is negligible (birthday bound at ~4 billion entries).

---

## 4. Multi-Pass LLM Reasoning — From Text to Concepts

### The Problem with Single-Pass Extraction

A single prompt asking an LLM to simultaneously classify an article's domain, extract named entities, identify abstract concepts, and propose related topics produces worse results than breaking the task into sequential passes where each pass can use the output of prior passes as grounding. The extractor/analyzer split in Minerva makes this decomposition explicit.

### Three-Pass Analysis

The analyzer runs three sequential Ollama inference passes per article, each building on prior outputs:

**Pass 1 — Classification**

Input: article title + extracted text.

Output: `domain` (one of: physics, climate, programming, medicine, biology, astronomy, other), `article_type` (discovery, review, tutorial, opinion, news), `topic` (one-sentence summary).

This pass establishes the semantic frame. Its outputs scope the entity extraction in Pass 2.

**Pass 2 — Named Entity Extraction**

Input: title + text + Pass 1 domain.

Output: `facilities[]`, `people[]`, `locations[]`, `phenomena[]`.

The domain from Pass 1 guides entity type emphasis. An article classified as `astronomy` produces different entity expectations than one classified as `programming`. Passing the domain prevents the model from hallucinating entities outside the relevant category.

**Pass 3 — Concept Extraction**

Input: title + text + Pass 1 domain + Pass 2 entities.

Output: `concepts[]`, `related_topics[]`.

By this pass, the model has the article's classification, its named entities, and can produce abstract concepts that generalize beyond the article's specific content. `related_topics` captures adjacent intellectual territory — the conceptual neighbourhood, not just the explicit content.

### Keywords as a Derived Aggregate

Keywords are not extracted directly. They are computed post-analysis as the deduplicated union of `phenomena[]` + `concepts[]` + `related_topics[]`, case-normalized. This means keywords represent a three-dimensional extraction of the article's content:
- What physical/observable things are mentioned (phenomena)
- What abstract ideas are developed (concepts)
- What related fields are implied (related_topics)

These keywords drive external search: each search primitive queries its API using these terms.

### Serialization Constraint

Ollama handles one inference at a time. All three passes per article are serialized by the analyzer's `sync.Mutex`. With a 300-second timeout per pass, a single article can occupy the analyzer for up to 15 minutes. This is the intended behavior — depth of extraction is preferred over throughput.

The total pipeline is bounded by the number of articles in the trigger window, not by any external rate limit.

### Embedding as a Post-Analysis Product

After the three passes, the analyzer calls Ollama's `/api/embed` endpoint on the concatenation of `topic + keywords + concepts` (Pass 1 topic + derived keywords). This produces a 4096-dimensional float32 vector that is attached to the article and persisted for later vector search. The embedding is best-effort: if Ollama is unavailable or times out, a nil embedding is stored and the article proceeds through the pipeline. Vector search will silently skip nil-embedding articles; keyword search remains available.

---

## 5. Work Deduplication — Canonical IDs Across Sources

### The Problem

Four search primitives (OpenLibrary, arXiv, Semantic Scholar, OpenAlex) run in parallel against the same set of keywords. A paper indexed on both arXiv and Semantic Scholar will be returned by both primitives, potentially with different metadata completeness. Without dedup, the same physical work would appear multiple times in the knowledge base.

### The Canonical ID

Each work is assigned a **canonical ID** that encodes the most reliable available identifier, in priority order:

```
isbn13:{x}   — physical book; ISBN-13 is globally unique and stable
doi:{x}      — registered DOI; cross-database stable identifier
arxiv:{x}    — arXiv preprint ID; stable for arXiv-indexed papers
ref:{id}     — source-specific reference ID; fallback when no universal identifier exists
```

The priority ordering reflects identifier reliability: ISBN-13 is a formal registry; DOI is a persistent identifier standard; arXiv IDs are stable within the arXiv system but not globally unique across all academic databases; source-specific reference IDs are last resort.

### Merge on Upsert

When a work is inserted, `ON CONFLICT (canonical_id) DO UPDATE` merges the incoming record into the existing one:
- `reference_ids` (JSONB array) is appended with the new source's ID.
- `sources` (JSONB array) is appended with the new search source name.
- Scalar fields (title, authors, publisher, etc.) are updated only if currently null.

The result: the same physical book discovered by OpenLibrary (with ISBN) and Semantic Scholar (with DOI) occupies a single row. Both reference IDs are preserved in the array. Both source names are recorded. The higher-priority canonical ID wins if it is set by either source.

### Why the Reference IDs Array

A single `reference_id` column would record only one source's identifier for each work. The array preserves the full provenance: given a work, it is possible to know that OpenLibrary found it as `OL123W`, arXiv indexed it as `2301.00001`, and Semantic Scholar referenced it as `s2:abc123`. This is useful for external linking and for diagnosing cases where two sources disagree on authorship or title.

---

## 6. Cross-Source Completion Dedup — Shared Article State

### The Problem

Multiple source primitives (FreshRSS, Miniflux, Linkwarden) may bookmark the same URL independently. Without coordination, they would each publish the same article into the pipeline, resulting in duplicate analysis and duplicate work candidates.

### Shared Completion Table

All source primitives share a single `articles` table in PostgreSQL. Before publishing a `RawArticle`, each source calls `store.IsComplete(url)`. If `completed_at` is set (meaning the article has already traversed the full pipeline), the source skips it.

Critically, the completion table is not per-source. If FreshRSS completes an article, Miniflux and Linkwarden will also skip it on their next trigger run, even though they found the URL independently. The article is considered complete for the entire system, not for a specific source.

This is a deliberate trade-off: it means that an article discovered via Linkwarden but already processed via FreshRSS will not be re-analyzed, even though the Linkwarden context (e.g., tags, annotations) might differ. For the current use case — deduplication of article processing — this is the correct behaviour.

### Published vs. Completed

Two timestamps are tracked per article:

- `published_at`: set when a source calls `MarkPublished`. Records entry into the pipeline.
- `completed_at`: set by the `store` primitive when it publishes `ArticleComplete`. Records successful full traversal.

An article with `published_at` but no `completed_at` is in-flight. The state primitive (§7) uses this to identify articles that need crash recovery.

---

## 7. Crash Recovery — The State Primitive

### The Problem

The pipeline is a sequence of MQTT messages. If any primitive crashes between receiving a message and publishing the next stage, that article is in-flight with no record of its progress. On restart, no primitive will re-process it because no message is being published for it. The article is silently lost.

### Raw State Recording

The **state primitive** is a pure observer that subscribes to every pipeline topic and records the most recent raw payload per `(article_id, topic)` pair into the `pipeline_state` table. It stores bytes, not parsed structures — no message type dependencies, no schema coupling. When a new message arrives for an `(article_id, topic)` pair, it overwrites the previous one.

The schema is intentionally minimal:

```
pipeline_state(article_id TEXT, topic TEXT, payload JSONB, stored_at TIMESTAMPTZ)
PRIMARY KEY (article_id, topic)
```

### Replay on Trigger

When a trigger fires, the state primitive:
1. Queries `pipeline_state` for all articles that have no `ArticleComplete` entry.
2. For each such article, selects the row with the most advanced pipeline topic (ordered by stage: analyzed > extracted > raw).
3. Re-publishes the stored payload to that topic.

The result: each incomplete article re-enters the pipeline from its most recent successful stage. A primitive that crashed after analysis but before search will have its analyzed payload replayed, so the search primitives re-receive it.

### Why Separate from the Main Store

The state store uses a separate PostgreSQL database (or schema) from the main knowledge base. This separation prevents the crash-recovery state from competing with main store writes under load, and allows the state table to be truncated or reset without affecting the knowledge base.

It also enforces a clean conceptual separation: the main store is the long-term knowledge record; the state store is ephemeral operational state that should be empty in steady operation (every article that was started was also completed).

---

## 8. The Brief — Semantic Retrieval from a Personal Corpus

### Purpose

The brief service answers the question: given what the user has been reading (as represented by articles in the knowledge base), what existing material in the corpus is most relevant?

This is not search over external sources — it is retrieval over the accumulated knowledge base that has been built up by the pipeline. It is the system turning back on itself.

### Two Retrieval Modes

**Keyword Mode**: Constructs a full-text search query from the provided `ManifoldProfile` (a map of thematic labels to weights). Articles and works are ranked by PostgreSQL's `ts_rank` against their `tsvector` columns, with scores blended by profile weights. This mode does not require embeddings and works from day one of the corpus.

**Vector Mode**: Performs approximate nearest-neighbour (ANN) search using pgvector's cosine distance operator (`<=>`) on article and work embeddings. Takes as input `TrendEmbeddings` (vectors from the most relevant manifold chunks in the Journal system) and `UnexpectedEmbeddings` (vectors representing emergent conceptual territory). Results from both embedding sets are blended.

The blend weight between trend and unexpected results is influenced by the `SoulSpeed` scalar from Journal: higher soul speed biases toward unexpected (frontier) material; lower soul speed biases toward trend (consolidating) material.

### Why 4096-Dimensional Embeddings

The embedding model (`qwen3-embedding:8b` via Ollama) produces 4096-dimensional float32 vectors. This is the same model used by the Journal system, which is not a coincidence: for the Minerva→Journal query protocol to work, both the query vectors (produced by Journal) and the indexed vectors (produced by Minerva's analyzer) must be embeddings from the same model. A vector produced by one model is not comparable to a vector produced by another.

The dimension is fixed by the database schema (`vector(4096)` in pgvector). Changing the embedding model requires migrating all vector columns and recomputing all stored embeddings.

### Session Tracking

Each query generates a `session_id` (hex-encoded UnixNano). Results are stored in `brief_sessions`, `brief_session_articles`, and `brief_session_works` with scores and ranks. This allows:
- The consolidator to aggregate scores across multiple brief sessions.
- Feedback (read/skip) to be recorded per session for future quality assessment.
- Historical analysis of what has been surfaced over time.

---

## 9. Consolidation and Notification Deduplication

### The Aggregation Problem

A single brief query returns a ranked list of articles and works. But the goal is not to surface whatever is highest-ranked at one moment — it is to surface what has been consistently relevant across multiple queries over time. A work that scores 0.7 in three separate brief sessions is more reliably interesting than one that scores 0.9 in one session and 0.2 in another.

The **consolidator** addresses this by aggregating scores across all brief sessions within a configurable lookback window (default 24 hours). It computes a weighted aggregate score per work and selects the top-N (default 1) above a threshold.

### Notification Deduplication

The consolidator also maintains a `consolidator_surfaced` table that records every work that has been sent to the notifier, with a timestamp. Before publishing a `ConsolidatorDigest`, it checks whether the work has already been surfaced within the deduplication window (default 20 hours). If yes, it is skipped regardless of its current score.

This prevents the notifier from delivering the same recommendation on consecutive runs. A work that scores highly every day is suppressed after its first notification; it can reappear once the deduplication window expires.

### Why the Notifier Is a Stub

The notifier is a pure subscriber: it subscribes to `minerva/consolidator/digest`, formats the notification, and calls the ntfy API. It performs no database writes, no score computation, no filtering. All of that work is done upstream by the consolidator.

This means the notifier can be swapped for any other delivery mechanism (email, Slack, webhook) without touching any other component. The notification format is an implementation detail of the notifier, not a system concern.

---

## 10. Journal Integration — Externally-Directed Retrieval

### The Protocol

The Journal system (a separate project) can query Minerva for article and work recommendations grounded in the user's current intellectual trajectory. Journal publishes a `BriefQuery` to `minerva/query/brief` containing:

- `ManifoldProfile`: a map of standing-document slugs to GLF+soul-speed-weighted proximity scores, representing the current center of gravity of the user's thinking.
- `TrendEmbeddings`: raw 4096-dimensional vectors from the most semantically loaded chunks of the top-scoring standing documents. These are the actual embedding coordinates of the current intellectual territory.
- `UnexpectedEmbeddings`: vectors for concepts that have appeared in recent entries but are not close to any known standing document — the frontier of thinking.
- `SoulSpeed`: a scalar in [0, 1] representing the aliveness of current thinking (see Journal CONCEPTS.md §5).

Minerva responds on the `ResponseTopic` specified in the query with a ranked list of articles and works.

### Why Vectors Rather Than Labels

The `ManifoldProfile` map contains human-readable labels (`distributed-patterns`, `universe-design`). These are legible to a human but not directly useful for semantic retrieval — Minerva cannot search its corpus for "distributed-patterns" unless it has a standing document with that name. The `TrendEmbeddings`, by contrast, are actual vectors in the shared embedding space. Minerva can perform ANN search against these vectors regardless of what the standing documents are named or what concepts they represent.

This design decouples the Journal system's internal vocabulary from Minerva's retrieval mechanism. Journal can define as many standing documents as desired with any names; Minerva receives only the geometric consequence — where in embedding space the current thinking is concentrated.

---

## 11. Design Decisions and Roads Not Taken

### Separate Store vs. StateStore

Two distinct PostgreSQL stores are maintained: `internal/store/` (the knowledge base) and `internal/statestore/` (crash recovery state). The stores could have been merged into a single database with different tables. They are kept separate because:

- The knowledge base is long-term and queryable; the state store is ephemeral and should be empty in normal operation.
- They have different write patterns: the knowledge base has structured upserts; the state store has raw-payload inserts.
- Keeping them separate allows the state store to be wiped (for debugging or recovery) without touching the knowledge base.

### No Persistent MQTT Sessions

The `CleanSession=true` choice means Minerva has no reliable message delivery guarantee if primitives are not running. This was preferred over persistent sessions because persistent sessions require message ID management and add complexity to reconnection logic. The tradeoff is operational: all primitives must be started before triggering. This is acceptable for a system run by a single operator.

### Single Notifier Topic, Single Recipient

The notifier sends to a single ntfy topic. There is no fan-out, no per-user configuration, no subscription management. This is appropriate for a personal system. Adding multi-user support would require introducing a user identity concept that is intentionally absent from the current design.

### Embedding Soft Failures

Embedding calls are best-effort throughout. If Ollama is unavailable when an article is analyzed, a nil embedding is stored. The article proceeds through the pipeline and is available for keyword search. This decision prioritizes pipeline throughput over completeness of the vector index. A `reembed`-style tool exists in the Journal system for retroactive re-embedding; the equivalent for Minerva would follow the same pattern.

### Koha Ownership as a Filter, Not a Gate

Works found to be already owned in Koha are not dropped from the pipeline — they are moved to the `OwnedWorks` list in `CheckedWorks`. The store persists ownership information. The decision about whether to surface owned vs. unowned works is left to downstream consumers (currently: all owned works are effectively filtered at the notifier stage, but the data is preserved for potential future use).

---

## 12. Relationships Between Concepts

The concepts form a linear pipeline with two cross-cutting concerns:

```
Source Primitives (FreshRSS, Miniflux, Linkwarden)
    │
    │  ArticleID = SHA256(url)[:16]   [§3]
    │  Cross-source dedup via shared completed_at   [§6]
    │
    ▼
Extractor → Analyzer (3-pass LLM)   [§4]
    │  Keywords = phenomena ∪ concepts ∪ related_topics
    │  Embedding = Ollama(topic + keywords + concepts)
    │
    ├──────────────────────────────────────┐
    ▼                                      ▼
Search Primitives (parallel)           State Primitive   [§7]
OpenLibrary, arXiv, S2, OpenAlex       Records raw payloads per (article_id, topic)
    │                                  Replays on next trigger if incomplete
    │  Canonical ID dedup   [§5]
    ▼
Koha-Check (books only)
    │
    ▼
Store (pure observer)
    │  Persists articles, works, links
    │  Publishes ArticleComplete → sources skip on next trigger   [§6]
    │
    ▼
Brief (query-driven, not pipeline-driven)   [§8]
    │  Vector ANN or keyword search
    │  Session tracking
    │
    ▼
Consolidator   [§9]
    │  Score aggregation across sessions
    │  Notification dedup window
    │
    ▼
Notifier (pure observer)   [§9]
    │  ntfy push notification
    │  No DB writes
    │
    ▼
External delivery
```

The **State Primitive** is a cross-cutting concern: it observes every stage without participating in the main data flow, and activates only when the pipeline has failed partway through an article.

The **Journal Integration** (§10) is a lateral entry point: Journal publishes to `minerva/query/brief` independently of the main trigger cycle, receiving recommendations derived from the accumulated corpus.
