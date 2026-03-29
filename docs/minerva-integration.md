# Minerva Integration — Journal Brief Query

This document describes the MQTT protocol between Journal's `brief-assemble` service and Minerva.
It covers what gets sent, why, and what Minerva is expected to do with it.

---

## Overview

Once per brief cycle (typically morning), `brief-assemble` publishes a query to Minerva containing:

1. A scalar profile of recent work across semantic territories (**trend signal**)
2. Embedding vectors for the top trend manifolds (**for ANN search**)
3. Embedding vectors for unexpected concepts — thinking that doesn't fit any known territory (**surprise signal**)
4. A soul-speed scalar indicating how "alive" recent thinking is (**query modulation**)

Minerva finds the most relevant unread article for the current moment and publishes one result back.

---

## MQTT Topics

| Direction | Topic | Description |
|-----------|-------|-------------|
| Journal → Minerva | `minerva/query/brief` | Journal publishes a query per brief cycle |
| Minerva → Journal | `journal/brief/minerva-response` | Minerva publishes one result per session |

The response topic is also carried in the query payload as `response_topic` — use that field, not a hardcoded string.

---

## Query Payload (`minerva/query/brief`)

```json
{
  "session_id": "1a2b3c4d5e6f",
  "manifold_profile": {
    "deep-learning": 0.82,
    "systems-thinking": 0.61,
    "philosophy-of-mind": 0.34,
    "soul-speed": 0.71
  },
  "trend_embeddings": [
    [0.012, -0.034, ...],
    [0.089, 0.001, ...]
  ],
  "unexpected_embeddings": [
    [-0.041, 0.067, ...],
    [0.023, -0.019, ...]
  ],
  "soul_speed": 0.71,
  "top_k": 5,
  "response_topic": "journal/brief/minerva-response"
}
```

### Fields

**`session_id`** `string`
Opaque hex identifier for this brief cycle. Echo it back verbatim in the response — Journal uses it
to match responses to pending sessions.

**`manifold_profile`** `object: slug → float32`
Human-readable scalar proximity scores, one per standing document. Values are in [0, 1]:
- 1.0 = recent journal entries are semantically very close to this territory
- 0.0 = no recent affinity at all

These are GLF-recency-weighted (recent entries count more) and soul-speed-modulated (entries
written in a more "alive" state contribute more). Use for logging, ranking fallback, or as
importance weights if ANN search isn't available yet.

**`trend_embeddings`** `[][]float32` (may be absent)
Flat list of 4096-dimensional embedding vectors. These are representative chunk vectors from the
top-scoring manifolds — the geometric center of where recent journal work lives in embedding space.
Use for ANN nearest-neighbour search against Minerva's article corpus.

- All vectors are 4096-dimensional (qwen3-embedding:8b)
- Typically 3–9 vectors (3 manifolds × up to 3 chunks each, configurable)
- May be absent if Ollama was unavailable at query time — fall back to `manifold_profile`

**`unexpected_embeddings`** `[][]float32` (may be absent)
Embeddings of concept strings that appeared in journal entries which don't fit any known standing
document territory. These are phase-transition signals — thinking that is rising outside established
categories.

- Same 4096-dimensional space as `trend_embeddings`
- Typically 1–5 vectors (configurable)
- May be absent if no unexpected concepts were found or Ollama was unavailable

**`soul_speed`** `float32`
GLF-weighted proximity to the soul-speed standing document [0, 1]. Indicates how much recent work
feels generative and alive vs consolidating and routine.

- High (>0.6): recent thinking is exploratory and energised — surface more novel/challenging material
- Low (<0.4): recent thinking is consolidating — surface reinforcing/clarifying material
- Use this to modulate ranking, not as a search vector

**`top_k`** `int`
Maximum number of candidate articles to consider internally. Currently always 5.

**`response_topic`** `string`
The MQTT topic to publish the response to. Always `journal/brief/minerva-response` currently,
but use this field rather than hardcoding.

---

## Response Payload (`journal/brief/minerva-response`)

```json
{
  "session_id": "1a2b3c4d5e6f",
  "article_url": "https://example.com/article",
  "article_title": "The title of the article",
  "score": 0.87
}
```

**`session_id`**: echo the session_id from the query exactly.

**`article_url`**: URL of the most relevant unread article. Required if found.

**`article_title`**: Display title. Required if found.

**`score`**: Relevance score [0, 1] as judged by Minerva. Journal applies its own threshold
(`BRIEF_RELEVANCE_THRESHOLD`, default 0.6) — below threshold the result is silenced. Send your
best candidate even if confidence is low; Journal decides whether to surface it.

If no candidate meets Minerva's internal bar, still publish a response with `score: 0` and
empty URL/title. Journal will silence it. **Do not time out silently** — Journal waits up to
30 seconds (`BRIEF_TIMEOUT`) before marking the session as timed out.

---

## Embedding Model

Both Journal and Minerva should use **`qwen3-embedding:8b`** via Ollama.

- Embed endpoint: `POST /api/embed` with body `{"model": "qwen3-embedding:8b", "input": "text"}`
- Output dimension: **4096**
- Similarity metric: **cosine similarity** (vectors are not normalised — compute dot product / (|a| × |b|))

Using the same model on both sides means the embedding spaces are identical. A journal entry
vector and an article vector at cosine distance 0 would be semantically identical text. In
practice, expect distances of 0.1–0.4 for strong topical match across different writing contexts.

You don't need formal taxonomy agreement. If a journal entry and a Minerva article both discuss
similar ideas in different words, qwen3-embedding will place them nearby regardless of vocabulary.

---

## Suggested Query Strategy

### Phase 1: Scalar-only (no Minerva corpus embeddings yet)

Use `manifold_profile` slugs as text queries. Embed the slug names or their standing document
titles and search the article corpus. Rank by proximity score as importance weight.

```
for slug, proximity in manifold_profile (sorted descending):
    candidates = search_articles_by_text(slug, top_k=5)
    for candidate in candidates:
        candidate.score *= proximity
return best_candidate
```

### Phase 2: Vector ANN search (once article corpus is embedded)

1. **Trend search**: ANN search using each vector in `trend_embeddings`. Aggregate results
   (e.g. union with max score). These vectors represent where recent work *is*.

2. **Unexpected search**: ANN search using each vector in `unexpected_embeddings`. These vectors
   represent where recent work is *heading outside known territory* — a strong match here is
   a surprise worth surfacing.

3. **Merge and rank**:
   - Prefer unexpected matches when soul_speed is high (exploratory phase)
   - Prefer trend matches when soul_speed is low (consolidating phase)
   - A simple blending: `final_score = trend_score * (1 - soul_speed * 0.4) + unexpected_score * (soul_speed * 0.4)`

4. **Return top result** with its score.

### Fallback

If `trend_embeddings` and `unexpected_embeddings` are absent (Ollama unavailable on Journal side),
fall back to Phase 1 using `manifold_profile`. The scalar profile is always present.

---

## Configurable Parameters (Journal side)

These env vars control what Journal sends. Minerva doesn't need to configure them, but knowing
the defaults helps calibrate expectations:

| Env var | Default | Meaning |
|---------|---------|---------|
| `BRIEF_TREND_MANIFOLDS` | 3 | Top-N manifolds by proximity to extract vectors from |
| `BRIEF_TREND_CHUNKS` | 3 | Chunks per manifold (highest L2-norm selected) |
| `BRIEF_UNEXPECTED_VECTORS` | 5 | Number of unexpected concept strings to embed |

At defaults: `trend_embeddings` has up to 9 vectors, `unexpected_embeddings` has up to 5.

---

## Open Questions

- **Trajectory vs snapshot**: the profile is a snapshot. "This manifold just appeared this week"
  vs "I've been here for 3 weeks" carry different meaning. A `delta_7d` field may be added to
  `manifold_profile` values in the future.

- **Unexpected bypass**: if Minerva finds no strong article match for unexpected concepts,
  the surprising concept itself might be worth surfacing directly (not an article). This would
  require a different response type. Deferred for now.
