# Plan: Add Linkwarden Source Primitive
## Created: 2026-02-26
## Complexity: opus
## Recommended implementation model: haiku

## Context

Add Linkwarden as a third article source alongside FreshRSS and Miniflux. Linkwarden is a
self-hosted bookmark manager. Unlike the RSS sources (which have a "starred" concept), Linkwarden
fetches ALL bookmarks — the user curates via collections but wants the full set in Minerva.

Auth: `Authorization: Bearer <token>` header. Same pattern as Miniflux.
Endpoint: `GET /api/v1/links` — returns `{ "response": [...] }` wrapper (confirmed from
user's existing LW client error handling which reads `errResp.Response`).
Pagination: Linkwarden uses cursor-based pagination. Query param `cursor=<last_link_id>&limit=25`.
Fetch in a loop until response array is empty. User has 100+ bookmarks so pagination is required.

Link fields available: `id` (int), `name` (string — title), `url` (string),
`description` (string), `collection` (object), `tags` (array), `createdAt` (string).

Dedup and completion behaviour is identical to Miniflux — `internal/state/` SQLite DB per source,
re-publish pending articles on startup, mark complete on `ArticleComplete` message.

All handler callbacks must use the goroutine dispatch pattern (see CLAUDE.md) — this is a hard
correctness requirement in paho.

## Prerequisites

- [ ] Linkwarden instance running and accessible at configured base URL
- [ ] API token generated in Linkwarden (Settings → API Keys)

## Tasks

### 1. Add LinkwardenConfig to internal/config/config.go

- [x] **File**: `internal/config/config.go`
- **Action**: Add `LinkwardenConfig` struct and wire it into `Config` and `Load()`.
- **Pattern**: Follow `FreshRSSConfig` / `MinifluxConfig` style — BaseURL, APIKey, Timeout.
  ```go
  type LinkwardenConfig struct {
      BaseURL string `json:"base_url" env:"LINKWARDEN_BASE_URL"`
      APIKey  string `json:"api_key"  env:"LINKWARDEN_API_KEY"`
      Timeout int    `json:"timeout"  env:"LINKWARDEN_TIMEOUT" default:"30"`
  }
  ```
  Add `Linkwarden LinkwardenConfig` field to `Config` struct.
  Add loading in `Load()` using `getEnv` / `getEnvInt` helpers — follow the Miniflux block pattern
  in `Load()`.
- **Test**: `go build ./internal/config/` passes.
- **Notes**: No default for BaseURL or APIKey — leave empty, caller validates.

### 2. [x] Create internal/services/linkwarden.go

- **File**: `internal/services/linkwarden.go`
- **Action**: Implement HTTP client for Linkwarden REST API.
- **Pattern**: Follow `internal/services/miniflux.go` structure exactly — same struct layout,
  same `SetLogger`, same error wrapping.
- **Implementation**:
  ```go
  type LinkwardenConfig = config.LinkwardenConfig  // or inline — follow miniflux pattern

  type LinkwardenItem struct {
      ID    int    // Linkwarden link ID — used as pagination cursor
      URL   string
      Title string // mapped from "name" field in JSON
  }

  // Internal response types
  type linkwardenLink struct {
      ID          int    `json:"id"`
      Name        string `json:"name"`
      URL         string `json:"url"`
      Description string `json:"description"`
  }

  type linkwardenResponse struct {
      Response []linkwardenLink `json:"response"`
  }

  func (l *Linkwarden) GetAllLinks() ([]LinkwardenItem, error)
  ```
  `GetAllLinks` must paginate: loop with `cursor` starting at 0, fetch `limit=25` per page,
  append results, set cursor to last link's ID, stop when response is empty.
  Auth header: `Authorization: Bearer <APIKey>`.
  URL: `<BaseURL>/api/v1/links?cursor=<cursor>&limit=25`.
- **Test**: Manual — point at real LW instance, verify item count matches expected.
- **Notes**: The `name` field in LW JSON is the link title. `url` is the article URL. If `name`
  is empty, use URL as fallback title (matches extractor behaviour which can recover titles).

### 3. [x] Create cmd/source-linkwarden/main.go

- **File**: `cmd/source-linkwarden/main.go`
- **Action**: Implement the source primitive binary.
- **Pattern**: Copy `cmd/source-miniflux/main.go` structure exactly. Replace Miniflux service
  with Linkwarden, update client ID to `"minerva-source-linkwarden"`, source string to
  `"linkwarden"`, state DB path default to `"./data/linkwarden-state.db"`.
- **Key differences from Miniflux**:
  - Config loaded via `config.Load()` (like FreshRSS) — not inline getEnv for service config,
    since we added it to Config struct.
  - Call `linkwarden.GetAllLinks()` instead of `miniflux.GetStarredEntries()`.
  - `LinkwardenItem` has no `PublishedAt` — pass empty string or `time.Now()` for state DB title.
  - All three subscribe callbacks (completion, trigger, pending re-publish on startup) must use
    goroutine dispatch pattern: `go func() { ... }()` with payload copy.
- **Test**: Run against real LW instance, trigger pipeline, verify logs show "Fetched N links"
  and articles published to `minerva/articles/raw`.
- **Notes**: `fetchAndPublish` calls `mqttClient.Publish` inside a goroutine — this is correct
  and required. Do not call Publish inside a blocking Subscribe callback.

### 4. [x] Add Makefile targets

- **File**: `Makefile`
- **Action**: Add `source-linkwarden` to the build targets and add run target.
- **Pattern**: Follow existing `source-miniflux` entries exactly.
  - In `build-primitives` target: add `go build -gcflags="all=-N -l" -o $(BUILD_DIR)/source-linkwarden ./cmd/source-linkwarden/`
  - In production `build-primitives` block: add `go build -o $(BUILD_DIR)/source-linkwarden ./cmd/source-linkwarden/`
  - Add to the list of primitives in the `run` target echo output
  - Add run target:
    ```makefile
    run-source-linkwarden: build-primitives ## Run Linkwarden source primitive
        $(BUILD_DIR)/source-linkwarden -config .env.dev
    ```
- **Test**: `make build-primitives` succeeds, `make run-source-linkwarden` starts the process.

### 5. [x] Add env vars to .env.example

- **File**: `.env.example`
- **Action**: Add Linkwarden config block.
  ```
  # Linkwarden Configuration
  LINKWARDEN_BASE_URL=https://your-linkwarden.example.com
  LINKWARDEN_API_KEY=your-api-token-here
  LINKWARDEN_TIMEOUT=30
  ```
- **Test**: Visual check.
- **Notes**: This is documentation only — actual values go in `.env.dev` which is gitignored.

## Completion Criteria

- `go build ./...` passes with no errors
- `make build-primitives` produces `./build/source-linkwarden` binary
- Running `make run-source-linkwarden` with valid `.env.dev` connects to broker and logs
  "Linkwarden source primitive ready — waiting for trigger"
- On `make trigger`, logs show "Fetched N links from Linkwarden" and N "Published message"
  entries on `minerva/articles/raw`
- Second trigger skips already-published links (dedup working)
- Full pipeline produces book recommendations for at least one Linkwarden article

## New Constraints or Gotchas

- Linkwarden pagination is cursor-based using the integer link `id` field as cursor, not an
  offset. Cursor starts at 0 (fetch from beginning). Each page's cursor for the next request
  is the `id` of the last item in the current response.
- The link title field in the JSON response is `name`, not `title`. Mapping to `Title` in the
  item struct is required.
- Linkwarden API returns `{ "response": [...] }` wrapper for both success and error responses.
  On error the `response` field is a string message, not an array — handle non-200 status codes
  before attempting to unmarshal as link array.
