# Linklore — Implementation Plan

Local-first link/bookmark manager. Go + SQLite (WAL + FTS5 + BLOB embeddings) + HTMX UI + LLM (Ollama / LiteLLM+vLLM on DGX) for summary, auto-tags, and RAG chat over saved links and collections.

Reference: graphrag (`/Users/gabrielemastrapasqua/source/personal/graphrag/`) — reuse the LLM backend, SQLite, config patterns. **Do NOT reuse the graph layer**: linklore is FTS5 + vector cosine + plain SQL, no PPR/graph traversal.

---

## 1. Goals (and non-goals)

**Goals**
- Save URLs into user-defined collections (Todoist/Pocket-style).
- Auto-extract: title, description, og:image (link only), readable content as Markdown.
- LLM summary (TL;DR) per link, computed once, cached.
- Auto-tags from summary + user-editable tags.
- Hybrid search (FTS5 + cosine on embeddings) across links/collections.
- Streaming chatbot grounded on a collection (RAG over summaries + content).
- HTMX-only UI: light, fast, server-rendered.

**Non-goals**
- No knowledge graph, no PPR, no entity extraction pipeline.
- No image storage (links only; UI toggle to show/hide).
- No auth (single-user local app); add later if needed.
- No JS build chain. CDN-only.

---

## 2. Architecture

```
cmd/
  linklore/             # single binary: serve | ingest-url | reindex
internal/
  config/               # YAML + env override (copy graphrag/internal/config)
  storage/              # SQLite WAL + FTS5 + migrations
  llm/                  # Backend iface + ollama, litellm (copy graphrag/internal/llm)
  extract/              # HTTP fetch + readability + html→md
  summarize/            # LLM TL;DR + auto-tags
  embed/                # embedding service (BLOB) + cosine
  search/               # hybrid FTS5 + vector ranking
  chat/                 # RAG context builder + streaming endpoint
  server/               # http.ServeMux + handlers + html/template
  worker/               # background fetch/summary/embed queue
web/
  templates/            # base.html, collections.html, links.html, link_detail.html, chat.html, search.html
  static/               # pico.css (CDN ok), htmx (CDN), minimal app.css
configs/
  config.yaml
data/
  linklore.db           # SQLite (WAL)
```

**External libs**

| Concern | Library | Notes |
|---|---|---|
| SQLite | `github.com/mattn/go-sqlite3` | Same as graphrag. WAL + FTS5 already supported. |
| Readability | `github.com/go-shiori/go-readability` | Mozilla Readability port. |
| HTML→MD | `github.com/JohannesKaufmann/html-to-markdown/v2` | Cleaner than passing raw HTML to LLM. |
| HTML query (og:tags) | `github.com/PuerkitoBio/goquery` | For `<meta property="og:*">`. |
| MD→HTML (reader mode) | `github.com/gomarkdown/markdown` | Render `content_md` for reader view. |
| HTML sanitize (reader mode) | `github.com/microcosm-cc/bluemonday` | Strict policy on rendered MD. |
| RSS export (per collection) | `github.com/gorilla/feeds` | Phase 8, optional but cheap. |
| YAML | `gopkg.in/yaml.v3` | Same as graphrag. |
| HTTP router | stdlib `net/http.ServeMux` (Go 1.22+) | No chi/gin. Same as graphrag. |
| Templates | stdlib `html/template` | Same as graphrag. |

**No external services**: no Jina Reader, no third-party APIs. Everything runs against the local DGX (Ollama / LiteLLM+vLLM) or on-host. SPA/paywall fallback via optional `chromedp` only, off by default.

LLM/embeddings: copy `internal/llm/backend.go`, `internal/llm/ollama/ollama.go`, `internal/llm/litellm/litellm.go` from graphrag. Same `Backend` interface (`Generate`, `GenerateStream`, `Embed`).

---

## 3. SQLite schema

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;

CREATE TABLE collections (
  id          INTEGER PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT,
  created_at  INTEGER NOT NULL
);

CREATE TABLE links (
  id            INTEGER PRIMARY KEY,
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  url           TEXT NOT NULL,
  title         TEXT,
  description   TEXT,
  image_url     TEXT,            -- og:image, link only
  content_md    TEXT,            -- readability output as markdown
  content_lang  TEXT,            -- detected language (en, it, ...)
  summary       TEXT,            -- LLM TL;DR
  status        TEXT NOT NULL,   -- pending|fetched|summarized|failed
  read_at       INTEGER,         -- NULL = unread (Inbox view)
  fetch_error   TEXT,
  archive_path  TEXT,            -- optional gzipped HTML snapshot
  fetched_at    INTEGER,
  created_at    INTEGER NOT NULL,
  UNIQUE(collection_id, url)
);

-- Chunks: 1 link → N chunks. Embedding lives here, not on links.
CREATE TABLE chunks (
  id        INTEGER PRIMARY KEY,
  link_id   INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  ord       INTEGER NOT NULL,    -- 0..N within the link
  text      TEXT NOT NULL,
  embedding BLOB                 -- []float32 little-endian
);
CREATE INDEX idx_chunks_link ON chunks(link_id, ord);

CREATE TABLE tags (
  id   INTEGER PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL
);

CREATE TABLE link_tags (
  link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  source  TEXT NOT NULL,         -- 'auto' | 'user'
  PRIMARY KEY (link_id, tag_id)
);

CREATE VIRTUAL TABLE links_fts USING fts5(
  title, description, summary, content_md,
  content='links', content_rowid='id'
);
-- triggers to keep FTS in sync (insert/update/delete on links)

-- Chunk-level FTS (RAG retrieval over chunk text, not whole link)
CREATE VIRTUAL TABLE chunks_fts USING fts5(
  text, content='chunks', content_rowid='id'
);
-- triggers to keep chunks_fts in sync

CREATE TABLE chat_sessions (
  id            INTEGER PRIMARY KEY,
  collection_id INTEGER REFERENCES collections(id) ON DELETE SET NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE chat_messages (
  id          INTEGER PRIMARY KEY,
  session_id  INTEGER NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
  role        TEXT NOT NULL,     -- user|assistant|system
  content     TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE INDEX idx_links_collection ON links(collection_id);
CREATE INDEX idx_links_status     ON links(status);
CREATE INDEX idx_links_read_at    ON links(read_at);
CREATE INDEX idx_chat_msgs_session ON chat_messages(session_id, id);
```

Embeddings stored as BLOB (`[]float32` little-endian, same as graphrag `internal/vector/vector.go`). Cosine computed in Go — fine up to ~50k chunks on a laptop.

### Chunking strategy
- Split `content_md` by heading + paragraph; pack into ~800-token windows with ~100-token overlap.
- Skip tiny chunks (< 40 tokens). For very short links (tweet, snippet), 1 chunk = whole content.
- Chunks are the unit of retrieval for RAG; the link-level summary is still indexed in `links_fts` for direct hits.

---

## 4. Pipeline (link lifecycle)

1. **POST /c/:slug/links** with `url` → insert row `status=pending`, return HTMX row immediately. Save never blocks on LLM/embed reachability.
2. Worker picks pending → `extract.Fetch(url)` (HTTP GET, 15s timeout, UA "linklore/0.1").
3. `extract.Readable(html)` → readability article (title, byline, content_html).
4. `extract.OG(html)` → og:image, og:description, og:title fallback.
5. `extract.HTMLToMarkdown(content_html)` → `content_md`. If `content_md` < 200 chars and headless fallback enabled → retry via `chromedp` (off by default).
6. Optional: gzip raw HTML to `data/snapshots/<id>.html.gz`, persist `archive_path` (per-collection toggle).
7. Detect `content_lang` (Phase 7 lib).
8. Update row → `status=fetched`.
9. `summarize.Summarize(ctx, content_md, title, existingTags)` → LLM call. Prompt receives top-50 existing tag slugs and instruction to **prefer reuse**. Returns strict JSON `{tldr, tags[]}`. Max 5 tags/link.
10. Tag normalization: lowercase, slugify, drop trailing 's' lemma, dedupe vs existing slugs (Levenshtein ≤1 → reuse). Cap global active tags at 200; over the cap, surface "tag merge" suggestions in UI rather than blocking.
11. Chunk `content_md` (heading+paragraph, 800-token windows, 100-token overlap) → insert into `chunks`.
12. Batch-embed all new chunks via `llm.Backend.Embed` (batch size 32, errgroup limit) → write BLOB.
13. Insert tags + link_tags (source='auto'). Update row → `status=summarized`. FTS triggers fire.

**Failure**: `status=failed`, `fetch_error` populated. UI shows retry button → `POST /links/:id/refetch`. If only LLM/embed failed (extraction OK), status stays `fetched` and a separate "needs reindex" badge shows in UI; the worker retries with backoff.

**Concurrency**: worker pool, `errgroup.WithContext().SetLimit(N)` (copy graphrag pipeline pattern). Default N=4. LLM/embed batched at the Ollama level (batch size 32 for embeddings).

---

## 5. Search & chat

**Hybrid search** (`GET /search?q=...&collection=...`):
1. FTS5 `MATCH` on `links_fts` (link-level) AND `chunks_fts` (chunk-level) → union of top 50 by `bm25`.
2. Embed query → cosine vs chunk embeddings of candidates → re-rank.
3. Group chunks back to links, score = max(chunk_score), return top 20 links with best-matching snippet.
4. Live-search variant: HTMX 300ms debounce on the global top-bar input.

**RAG chat** (`POST /chat/stream`, SSE):
1. Embed last user message.
2. Retrieve top-K chunks (K=8) from current collection by hybrid (cosine + bm25 on `chunks_fts`).
3. Build prompt: `system` (linklore assistant, cite links by id) + retrieved snippets (each tagged with link id+title) + chat history + user message.
4. `llm.GenerateStream` → forward NDJSON chunks to client as SSE.
5. Persist `chat_messages` after stream completes.

Same streaming pattern as graphrag `internal/llm/ollama/ollama.go:183-206` (`json.NewDecoder` over `resp.Body`).

---

## 6. HTTP routes

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | List collections + Inbox shortcut + global search bar. |
| POST | `/collections` | Create collection (HTMX append). |
| GET | `/c/{slug}` | Links in collection + add-link form + tag filter (sidebar tag cloud). |
| POST | `/c/{slug}/links` | Enqueue URL (HTMX returns row). |
| GET | `/inbox` | Unread links across collections (last 24h, `read_at IS NULL`). |
| POST | `/links/{id}/read` | Mark as read (HTMX swap). |
| GET | `/links/{id}` | Detail: summary, content_md, tags, refetch. |
| GET | `/links/{id}/read` | **Reader mode**: render `content_md` as clean article (Pocket-style). |
| POST | `/links/{id}/tags` | Add user tag (HTMX). |
| DELETE | `/links/{id}/tags/{tag}` | Remove tag. |
| POST | `/links/{id}/refetch` | Re-run pipeline. |
| POST | `/links/{id}/reindex` | Re-run summary+embed only (extraction kept). |
| DELETE | `/links/{id}` | Delete link. |
| GET | `/search` | Hybrid search results page. |
| GET | `/search/live` | HTMX live-search fragment (300ms debounce on top bar). |
| GET | `/tags` | Tag cloud + merge suggestions when active tags > 200. |
| POST | `/tags/merge` | Merge tag A into tag B (admin action, HTMX). |
| GET | `/chat` | Chat UI. |
| POST | `/chat/stream` | SSE stream. |
| GET | `/c/{slug}/feed.xml` | RSS export of latest links in a collection (Phase 8). |
| GET | `/bookmarklet` | Page that hosts the drag-to-bookmark JS snippet. |
| POST | `/api/links` | Bookmarklet endpoint: `{url, collection?}`, returns 201 + minimal JSON. |
| GET | `/healthz` | Liveness. |
| GET | `/static/...` | Embedded static files (`embed.FS`). |

UI flags (cookie): `show_images=0|1`, `reader_font=serif|sans`, `reader_width=narrow|wide`.

**Bind to `127.0.0.1:8080` by default** (`server.addr` overridable). No auth, but no LAN exposure unless explicit.

---

## 7. Config (`configs/config.yaml`)

```yaml
server:
  addr: "127.0.0.1:8080"   # localhost-only by default

database:
  path: "./data/linklore.db"

llm:
  backend: "litellm"         # litellm | ollama
  litellm:
    base_url: "http://192.168.1.94:8000/v1"   # vLLM via LiteLLM proxy on DGX
    model: "qwen36-chat"
    embed_model: "nomic-embed"
    api_key: "$LITELLM_API_KEY"
    timeout_seconds: 600
  ollama:
    host: "http://192.168.1.94:11434"   # fallback
    model: "qwen3.6:35b"
    embed_model: "nomic-embed-text"

worker:
  concurrency: 4
  embed_batch_size: 32
  fetch_timeout_seconds: 15

extract:
  headless_fallback: false        # chromedp for SPA/paywall; off by default
  archive_html: false             # gzipped raw HTML snapshot per link
  min_readable_chars: 200         # below this, trigger headless fallback (if enabled)

chunking:
  target_tokens: 800
  overlap_tokens: 100
  min_tokens: 40

tags:
  max_per_link: 5
  active_cap: 200                 # over the cap, surface merge suggestions in /tags
  reuse_distance: 1               # Levenshtein on slug to dedupe near-duplicates

ui:
  show_images_default: false
  reader_font: "serif"
  reader_width: "narrow"
```

Env overrides (whitelist): `LINKLORE_DB_PATH`, `OLLAMA_HOST`, `LINKLORE_LLM_BACKEND`, `LITELLM_BASE_URL`, `LITELLM_API_KEY`, `LINKLORE_ADDR`. Pattern from graphrag `internal/config/config.go:244-310`.

---

## 8. TODO (phased)

### Phase 1 — Skeleton + storage (1-2 days)
- [ ] `go mod init github.com/gabrielemastrapasqua/linklore`, Makefile (build, test, run, fmt, vet, lint).
- [ ] `internal/config` with YAML + env overrides. Bind `127.0.0.1:8080` by default.
- [ ] `internal/storage`: open SQLite WAL, run migrations, basic CRUD (collections, links, **chunks**, tags). Tests.
- [ ] `cmd/linklore` subcommands: `serve`, `add <url> -c <slug>` (CLI add, free given the binary).

### Phase 2 — UI CRUD (1-2 days)
- [ ] `internal/server`: `ServeMux`, base layout, htmx + pico.css from CDN.
- [ ] Routes: list collections, create collection, list links in collection, add link, delete link.
- [ ] Embedded `web/static` and `web/templates` via `embed.FS`.
- [ ] Smoke test: add link → see row → delete.

### Phase 3 — Fetch + readability + html→md (1-2 days)
- [ ] `internal/extract`: `Fetch`, `Readable`, `OG`, `HTMLToMarkdown`.
- [ ] **Layered extraction**: readability primary; if `< min_readable_chars`, fall back to raw `<body>` cleaned + html→md; optional `chromedp` headless fallback behind config flag (off by default).
- [ ] **Manual paste**: textarea on add-link form to paste content when fetch fails entirely.
- [ ] **Optional HTML archive**: gzip raw HTML to `data/snapshots/<id>.html.gz` when `extract.archive_html=true`.
- [ ] HTML fixtures in `internal/extract/testdata/` (news article, blog post, tweet, GitHub README, paywall stub).
- [ ] Worker queue: pending → fetched. Status transitions visible in UI (HTMX poll on row).

### Phase 4 — LLM summary + auto-tags (1-2 days)
- [ ] Copy `internal/llm/{backend.go,ollama,litellm}` from graphrag, drop unused MLX/openai.
- [ ] `internal/summarize`: prompt template returning strict JSON `{tldr, tags[]}`. Inject top-50 existing tag slugs + "prefer reuse" instruction. Retry on parse fail (max 2).
- [ ] **Tag normalization** (`internal/tags`): slugify, lowercase, drop trailing 's', Levenshtein ≤1 dedupe vs existing slugs, cap at 5 tags/link, hard cap 200 active tags globally (over the cap → mark "needs merge", surface in `/tags`).
- [ ] `/tags` page + `POST /tags/merge` (HTMX).
- [ ] Tests with a fake Backend implementation; assert reuse + cap behaviour.

### Phase 5 — Chunking + embeddings + hybrid search (1-2 days)
- [ ] `internal/chunking`: heading+paragraph split, 800-token windows, 100-token overlap, drop < 40 tokens. Tweet/snippet → 1 chunk.
- [ ] `internal/embed`: BLOB encode/decode (`[]float32` LE), cosine. Batch via `Backend.Embed`.
- [ ] FTS5 triggers (`links_fts` + `chunks_fts`) + `internal/search.Hybrid` (link-level + chunk-level union, group by link, score = max(chunk_score)).
- [ ] `/search` route + results template with best-matching snippet per link.
- [ ] **Live-search** `/search/live` (HTMX 300ms debounce) wired to a top-bar input in the base layout.
- [ ] **Reindex-only** action: `POST /links/:id/reindex` re-runs summary+chunk+embed without re-fetching.
- [ ] **Resilience**: if LLM/embed unreachable at ingest time, link stays at `status=fetched`, worker retries with backoff. UI shows "needs reindex" badge.
- [ ] Test: ingest 5 fixture pages, search returns expected ranking; chunk recall on a long article > link-level recall.

### Phase 6 — Chat (RAG, streaming) (1-2 days)
- [ ] `internal/chat`: context builder (top-K hybrid retrieval over `chunks` within a collection).
- [ ] `/chat` HTMX page + `/chat/stream` SSE endpoint. Each retrieved snippet rendered with its link id+title; assistant prompt instructs to cite by id.
- [ ] Persist sessions + messages.
- [ ] Test: integration with fake streaming backend; verify SSE framing + citation rendering.

### Phase 7 — UX polish (1-2 days)
- [ ] **Reader mode** `/links/:id/read`: render `content_md` via `gomarkdown` → sanitize via `bluemonday` → narrow-column template. Cookie-controlled font/width.
- [ ] **Inbox view** `/inbox` (unread, last 24h, `read_at IS NULL`) + `POST /links/:id/read` to mark.
- [ ] **Tag cloud sidebar** in `/c/:slug` with click-to-filter; live counts.
- [ ] **Bookmarklet**: `/bookmarklet` page hosting the JS snippet (`javascript:fetch('/api/links',...)`); endpoint `POST /api/links` accepts `{url, collection?}`.
- [ ] Tag chips with user add/remove (HTMX).
- [ ] Show/hide images toggle (cookie).
- [ ] Refetch / retry button on failed links; reindex button on stale ones.
- [ ] Language detection on `content_md` (`pemistahl/lingua-go`), shown as a small badge in lists.

### Phase 8 — Ops & extras (optional)
- [ ] Dockerfile + docker-compose with Ollama on host network.
- [ ] Prometheus `/metrics` (request count + LLM latency, copy graphrag metric keys).
- [ ] Backups: `sqlite3 .backup` cron.
- [ ] **RSS export** `/c/:slug/feed.xml` (`gorilla/feeds`): subscribe to your own collection from any reader.

---

## 9. Test plan

| Layer | What to test | How |
|---|---|---|
| `internal/storage` | Migrations, CRUD on collections/links/chunks/tags, FTS triggers (links + chunks), BLOB roundtrip | `:memory:` SQLite + table-driven |
| `internal/extract` | Readability, OG parsing, HTML→MD, `min_readable_chars` fallback path | `testdata/*.html` golden files |
| `internal/chunking` | Heading/paragraph split, target/overlap windows, min-token drop | Table-driven on canned MD |
| `internal/tags` | Slugify, lemma drop-'s', Levenshtein dedupe, per-link cap, global cap → merge flag | Pure-func tests |
| `internal/llm` | Backend interface contract | Fake backend implementing all 3 methods |
| `internal/summarize` | JSON parsing, retry on bad JSON, existing-tag reuse in prompt | Fake backend returns malformed → valid |
| `internal/embed` | Encode/decode roundtrip, cosine bounds | Property tests |
| `internal/search` | FTS+cosine hybrid ranking, link grouping, chunk recall on long docs | Seed 10 fixtures, assert top-K |
| `internal/server` | Handlers return 2xx + correct HTMX fragments; reader mode HTML sanitized | `httptest.Server` |
| `internal/chat` | SSE framing, context builder respects K, citation tagging | `httptest` + fake stream |
| e2e (optional) | spin server, add fixture URL via local test server, expect summary + chunks + tags | local test http server serving HTML |

`make check` = `go fmt ./... && go vet ./... && golangci-lint run && go test -race ./...`

---

## 10. Open questions

1. **DGX endpoint**: confirm whether linklore should default to `litellm+vllm` (faster per user) or `ollama`. Plan supports both via config; default = `litellm` if available, fallback `ollama`.
2. **Content_md size cap**: truncate to ~16k chars before sending to LLM for the *summary* call (qwen3:4b at 8k num_ctx). Head+tail strategy. Chunks bypass this — they're already ≤800 tokens each.
3. **Deletion**: hard delete or soft? Hard for v1 (FK cascades). Snapshot files cleaned by a periodic GC sweep matching missing `archive_path`.
4. **Headless fallback**: keep `chromedp` opt-in (off by default). Enable manually when a specific source needs it; revisit if too many manual pastes pile up.

---

## 11. Raindrop.io — feature parity roadmap (single-user, MIT, no SaaS)

Goal: bring linklore to feature-parity with what makes Raindrop pleasant to use, **stripped of every SaaS/team/account concern**. Public pages, members, cloud backups, mobile push, paid gating — all out. Local file is the source of truth; sync is the user's problem (Syncthing, iCloud Drive, etc.).

### 11.1 What we already have (skip)

Collections + tags + drag-drop reorder, FTS5 search, embedding-based semantic search, RAG chat (≈ Stella's "find by meaning"), per-link TL;DR + auto-tags, RSS feed import, HTML→MD extraction, image lightbox + sources sidebar, srcset/lazy-load + tracker filter, link rename/move, bulk select/move/delete, collection delete with cascade, sidebar live updates.

### 11.2 What to add — core view & organization

Pure UI/CSS work, mostly. Highest leverage for "feels like Raindrop".

- [ ] **Four view modes per collection**: List / Grid / Headlines / Moodboard. Single `layout` enum on `collections`; CSS class on `#links-list`; `<select>` in collection header. Moodboard is masonry on `column-count`.
- [ ] **Density toggles** in collection header: hide/show titles, tags, summary, badges. Local-storage on client; no round-trip.
- [ ] **Right-pane preview drawer** ("instant preview"): selecting a row opens reader-mode markdown in a side drawer instead of navigating to `/links/:id`. The link-detail page stays as the deep-link target. Toggleable.
- [ ] **Article reader controls**: font-size, line-width, theme (already partial). Remember per-link or per-collection in a cookie.
- [ ] **Type classifier on ingest**: Article / Video / Image / Audio / Document / Book based on MIME + `og:type` + URL host (youtube/vimeo → Video). One small classifier, written into a new `type` column. Drives a colored corner-icon on cards and a sidebar filter.
- [ ] **Type-tinted icons** in the sidebar filter, line-style (Lucide), 1.5–2 px stroke.
- [ ] **Collection icon picker**: ship a Lucide subset (one JSON file, ~80 KB), search-by-keyword. New nullable `icon` column on `collections`.
- [ ] **Sort all collections by name** + **collapse all** sidebar buttons.
- [ ] **Sidebar fuzzy filter** (client-side `<input>` over the existing collections list — no backend round-trip).
- [ ] **Nested collections** (`parent_id INTEGER REFERENCES collections(id)`): keep the flat slug for routing but render a tree in the sidebar. Drag onto another collection to nest. **Decision point**: do this *or* keep collections flat and rely on tags for hierarchy. Lean toward keeping flat unless the user has >20 collections.

### 11.3 What to add — capture

- [ ] **Bookmarklet** that POSTs to `/api/links` (already wired) + opens a tiny popup with collection picker + AI-suggested tags. The "real" extension is Phase 2.
- [ ] **Saved-page indicator**: `HEAD /api/exists?url=…` returns 200/404. Bookmarklet flips its icon when current URL is in the library.
- [ ] **Per-collection drop-zone**: drag a URL anywhere on `/c/:slug` to add it. Already most of the way there with HTMX `hx-post`.
- [ ] **AI-suggested collection** in the bookmarklet popup: classify URL → existing collection. Reuses summarize backend.

### 11.4 What to add — search & filters

- [ ] **Filter sidebar** on the collection page (right or below sidebar), AND-stacked: type, Favorites, Has note, Has highlights, Without tags, broken, duplicates, by month, custom date range.
- [ ] **Filter logic toggle** `match:OR` — switch the AND stack to OR. One radio in the filter header.
- [ ] **Exclude prefix** (`-tag:foo` or `-bar`) in the search bar: small lexer that splits include/exclude terms before composing the FTS5 query.
- [ ] **`Without tags` filter** — `WHERE NOT EXISTS (SELECT 1 FROM link_tags WHERE link_id = links.id)`.
- [ ] **Date filter** — calendar popover, two `date` inputs, applied as `created_at BETWEEN ?`.
- [ ] **Saved searches** (cheap): bookmark the URL with `?q=…&filter=…`. Don't bother with a separate model.

### 11.5 What to add — housekeeping

- [ ] **Duplicates view**: URL normalization (lowercase host, drop `www.`, drop trailing `/`, drop `#fragment`, drop `utm_*` / `fbclid` / `gclid` querystring keys), then `GROUP BY normalized_url HAVING COUNT(*) > 1`. One-click "merge → keep newest" or "delete others".
- [ ] **Broken-links checker** with Basic / Default / Strict modes:
  - Basic: HTTP 4xx/5xx (404, 410)
  - Default: + DNS / connection failures
  - Strict: + slow (> 10s) + redirects > 3
  - Background worker every N hours, `head_check_at` + `last_status` columns. Sidebar filter "Broken".
- [ ] **Tag manager UI** at `/tags`: rename, merge, delete, hide-from-suggestions. Backend already supports rename + merge.
- [ ] **Tag rename propagates** (already true at storage level); expose `/tags/:slug/rename`.
- [ ] **Delete-all-empty-collections** action button.
- [ ] **Refresh preview** right-click action on any link → re-runs extraction.

### 11.6 What to add — content & enrichment

- [ ] **File uploads** (PDF, EPUB, TXT, MD, JPG/PNG/GIF/WEBP, MP3/MP4): sha256-on-disk under `data/uploads/`, mime sniff, store as `links` rows with `file_path` instead of (or alongside) `url`. Text extraction for FTS via `pdftotext` / `epub` lib.
- [ ] **Web archive (local)** — store a single-file HTML snapshot at `data/archive/<id>.html.gz` (using `monolith` or `single-file-cli` invoked as a child process; no JS bindings). Per-collection toggle `archive_html`. Already a Phase 3 stub in `extract.archive_html`.
- [ ] **Wayback fallback** when archive_html=false: store a Wayback URL on ingest (`http://web.archive.org/save/<url>` returns the snapshot URL). One HTTP call per save.
- [ ] **Highlights**: `highlights(id, link_id, range_json, color, note_md, created_at)`. Drag-to-select in the reader pane writes via `POST /links/:id/highlights`. Sidebar filter "Highlights".
- [ ] **Per-link Markdown notes** (already partial via `note` column) → expand into a full editor; render with the same MD pipeline used for the reader.
- [ ] **Reminders** (local desktop only): nullable `remind_at` column + small in-process scheduler that fires `Notification` via the Web Notifications API on the next page load after `now > remind_at`. No push, no email.
- [ ] **Custom thumbnail override**: `cover_url` column. Click thumbnail → paste-URL or upload.

### 11.7 What to add — import / export / interop

- [ ] **Import: Netscape HTML** (covers Chrome / Firefox / Safari / Edge / Pocket / Pinboard).
- [ ] **Import: CSV** (Raindrop / Diigo / Mymind shape).
- [ ] **Import: JSON** (Raindrop / Anybox / Omnivore).
- [ ] **Import: ENEX** (Evernote XML).
- [ ] **Export: Netscape HTML** (universal).
- [ ] **Export: JSON** (the round-trippable one).
- [ ] **REST API** beyond `/api/links`: `GET /api/links?q=…`, `POST /api/links/bulk`, `GET /api/collections`, `GET /api/tags`. Document `curl` recipes; this is the IFTTT/Apple-Shortcuts/Raycast/Alfred hook.
- [ ] **MCP server** as a subcommand: `linklore mcp` over stdio. Exposes `search_bookmarks`, `get_bookmark`, `add_bookmark`, `summarize_collection`, `list_collections`. One implementation reaches Claude Desktop / Cursor / Zed / VS Code / Windsurf.

### 11.8 What to add — Stella-equivalent AI

We already have RAG chat. Bring it closer to what Raindrop's "Stella" does as a first-class organizer.

- [ ] **Right-click "Ask about this"** on a link → opens chat pre-seeded with that link as the only context.
- [ ] **Canned-prompt buttons** in chat header: "Summarize this collection", "What topics do I save the most?", "Suggest tag merges", "Find broken or stale links".
- [ ] **Organize mode** (agentic with confirmation): chat answers "merge tags X and Y" / "delete N broken links" / "split this collection by topic" with a *diff preview*; user clicks Apply to execute. Every action goes through existing handlers.
- [ ] **Indexing-pending badge** on freshly-ingested links so the user knows chat can't see them yet.
- [ ] **Fresh-load shortcut**: `?ask=…` querystring on `/chat` so the bookmarklet can deep-link a question.

### 11.9 What to skip explicitly (call out in copy)

- Cross-device sync, daily backups to Dropbox/GDrive/OneDrive — replaced by "the SQLite file IS the backup; sync it yourself".
- Members / collaboration / invite links / roles.
- Public pages at `username.raindrop.page` and embed widgets.
- Account / 2FA / "log out from all devices".
- Mobile share-sheet integrations and native mobile apps. (PWA share-target may be revisited later.)
- Webhook auto-save (X, YouTube) — needs a public URL.

### 11.10 Visual / brand notes (for when we do a UI pass)

Reference points from the Raindrop product, adapted to a dark-first Mac terminal aesthetic the user already prefers:

- **Typography**: Inter (variable) for UI, JetBrains Mono for URL/code; system fallback. No serifs except as opt-in for the article reader.
- **Accent**: keep the current `--accent` blue (`#2563eb` light / `#7bb3ff` dark) — close enough to Raindrop's signature blue. Reserve **purple/violet** (`#a855f7`-ish) for AI affordances (chat, suggested-tags chips, sparkle glyph "✦").
- **Type icons**: Lucide line icons, 1.5 px stroke, color-tinted by category (article=neutral, video=red, image=teal, audio=violet, document=amber, book=brown).
- **Cards**: 8–12 px radius, faint elevation in light mode (`0 1px 2px rgba(0,0,0,.06)`), border-only in dark mode. Already mostly there.
- **Layouts**:
  - Grid → uniform 1:1 thumbnail crop, title under the image.
  - Moodboard → `column-count: 3` masonry, no crop, image-first.
  - Headlines → text-only, dense, single-line truncation, favicon at the leading edge.
  - List → existing layout, basically unchanged.
- **Sparkle icon** "✦" for every AI-touched UI element (suggested tags chip, "Ask about this" right-click, organize-mode buttons).
- **Reading pane**: serif option (Charter / Source Serif), max-width 68 ch, three font sizes (S/M/L), three themes (light / sepia / dark).

### 11.11 Phasing

Don't do this all at once. Order:

1. **Phase 9 — Views & filters** (11.2 + 11.4): biggest perceived improvement, no schema churn beyond `layout` and `type` columns.
2. **Phase 10 — Housekeeping** (11.5): duplicates + broken-links + tag manager. Each is 1-2 hours.
3. **Phase 11 — Capture polish** (11.3): bookmarklet popup + saved-indicator + AI-suggested collection.
4. **Phase 12 — Imports/exports** (11.7): Netscape HTML reader+writer first; CSV/JSON/ENEX next; REST API & MCP last.
5. **Phase 13 — Highlights + reminders + uploads** (parts of 11.6).
6. **Phase 14 — Web archive** (11.6): `monolith` integration is the biggest single chunk of work in this section; do it last.
7. **Phase 15 — Stella-equivalent organize mode** (11.8) once everything else is stable.

---

## 12. LLM-optionality + hardcoded-config audit (do before Phase 9)

Linklore must run cleanly with **no LLM at all**, and must not bake the user's DGX endpoints into the binary. Today most of this works (worker probes health, search has BM25 fallback, chat handler returns 503 cleanly, link_detail shows a friendly banner when LLM is down) — but several rough edges remain.

### 12.1 Make the LLM truly optional

- [ ] **Worker should still drain `pending → fetched` when the LLM is unconfigured.** Today `cmd/linklore/main.go:115-128` skips worker startup entirely when `newLLMBackend` errors, which means *fetch+extract also stops*. Instantiate `worker.New(store, nil, fetcher, …)` regardless; the worker already handles `w.llm == nil` in `probeHealth` and `processIndex` early-returns when unhealthy.
- [ ] **Cold-boot health flag**. Initialise `Worker.llmHealthy = false` (`internal/worker/worker.go:101`) so the very first probe gates LLM calls. Today it's optimistically `true` whenever `backend != nil`, which means the first link processed on a cold boot with a dead gateway still attempts a `Generate`.
- [ ] **Render `handleChatPage` as a proper "chat unavailable" HTML page** when `s.chat == nil`, instead of a bare `http.Error(w, …, 503)` (`internal/server/server.go:1353-1357`). Reuse the chat template with `Disabled=true` and the same "configure LLM" copy used on link_detail.
- [ ] **Sanitise SSE chat error events**. Don't leak raw `litellm chat: status 401: …` to the browser — emit a user-safe "LLM unavailable" while still logging the original (`internal/server/server.go:1411-1416, 1441-1444`).
- [ ] **Branch the "LLM not configured" hint on `cfg.LLM.Backend`** so Ollama users don't see "set LITELLM_API_KEY" — they should see "set OLLAMA_HOST / llm.ollama.host" (`internal/server/server.go:401-413`).
- [ ] **Add `none` / `disabled` as a valid `llm.backend` value** in `Validate()` (`internal/config/config.go:200-211`); have the rest of the stack treat it as nil-backend so users can opt out cleanly without setting env vars to provoke an error.

### 12.2 Strip DGX-specific defaults from the binary

These are baked into `config.Default()` and shipped with `go build`. A fresh `go install` of linklore on someone else's machine currently auto-targets `192.168.1.94`.

- [ ] `internal/config/config.go:98` — `BaseURL: "http://192.168.1.94:8000/v1"` → default `""`. Force the user to set `llm.litellm.base_url` (or `LITELLM_BASE_URL`); empty value → backend disabled.
- [ ] `internal/config/config.go:99` — `Model: "qwen36-chat"` → default `""`.
- [ ] `internal/config/config.go:100` — `EmbedModel: "nomic-embed"` → default `""`.
- [ ] `internal/config/config.go:101, 165-167` — `APIKey: "sk-local"` magic default duplicated in two places. Drop the default; only set it inside the `Backend == "litellm"` branch as a single named constant (`const litellmDefaultAPIKey = "sk-local"`) referenced once.
- [ ] `internal/config/config.go:105` — `Host: "http://192.168.1.94:11434"` → `http://localhost:11434` (matches `CLAUDE.md`).
- [ ] `internal/config/config.go:106` — `Model: "qwen3.6:35b"` → default `""`.
- [ ] `internal/config/config.go:107` — `EmbedModel: "nomic-embed-text"` → default `""`.
- [ ] `configs/config.yaml:14-28` — match the new neutral defaults; keep DGX values commented out as `# example for vLLM on a remote box:`.

### 12.3 Lift remaining hardcoded tuning into config

Not blocking, but easy wins for users running a different model/backend.

- [ ] `internal/chat/chat.go:43` — `TopK: 8, HistoryTurns: 6` → config `chat.top_k`, `chat.history_turns`.
- [ ] `internal/chat/chat.go:110` — `truncate(h.Chunk.Text, 1200)` → `chat.snippet_chars`.
- [ ] `internal/chat/chat.go:128` — `const maxPriorUserTurns = 2` → `chat.retrieval_prior_turns`.
- [ ] `internal/chat/chat.go:186` — `Temperature: 0.3` → `llm.chat.temperature`.
- [ ] `internal/summarize/summarize.go:43` — `MaxRetries: 2, MaxBodyChars: 16000` → `summarize.max_retries`, `summarize.max_body_chars`.
- [ ] `internal/summarize/summarize.go:79` — `Temperature: 0.2` → `llm.summarize.temperature`.
- [ ] `internal/summarize/summarize.go:120` — `const maxInject = 50` → `tags.inject_cap`.
- [ ] `internal/search/search.go:109,134,149,223` — `tagSyntheticBM25 = -2.0`, `topForRerank = 30`, BM25-norm divisor `30`, blend weights `0.5/0.5` → `search.cosine_rerank_top_n`, `search.bm25_norm_divisor`, `search.cosine_blend_weight`, `search.tag_synthetic_bm25`.

### 12.4 Backend coverage

Ollama is implemented (Generate / GenerateStream / Embed / Healthcheck against `/api/generate` and `/api/embed`), backend-agnostic in worker + chat + search, and selectable via `llm.backend: ollama`. ✓ The remaining gaps are cosmetic + ergonomic:

- [ ] **Symmetric startup log for Ollama**. `cmd/linklore/main.go:100-104` only logs the litellm gateway; add the Ollama variant when `cfg.LLM.Backend == "ollama"`.
- [ ] **Move "litellm" / "ollama" sentinel strings to constants** (`const (BackendLitellm = "litellm"; BackendOllama = "ollama"; BackendNone = "none")` in `internal/llm`) — they're sprinkled across `cmd/main.go`, `server.go`, `config.go`. Eight+ sites today.
- [ ] **Env overrides for model selection**: `LINKLORE_LLM_MODEL`, `LINKLORE_LLM_EMBED_MODEL` apply to whichever backend is active (`internal/config/config.go:174-198`).
- [ ] **Wrap litellm config errors with a hint**: `litellm.New` returning `"base_url required"` should be wrapped at the call site as `"litellm backend disabled: configure llm.litellm.base_url or set LITELLM_BASE_URL"` so the user knows where to look (`internal/llm/litellm/litellm.go:34-37` + caller).
- [ ] **Ollama version note**: `internal/llm/ollama/ollama.go:60-62` uses the new `/api/embed` (Ollama 0.2+) batch shape. Older servers expose `/api/embeddings` (singular). Either document the minimum Ollama version in README, or fall back to per-prompt loop on 404.

### 12.5 Acceptance for "LLM-optional"

These should all hold in a `LINKLORE_LLM_BACKEND=none` (or LLM offline) run:

1. `linklore serve` boots and binds, no panics.
2. `/` lists collections; `/c/:slug` lists links.
3. `POST /c/:slug/links` with a URL ingests it: row appears, status moves `pending → fetched` (no `summarized`).
4. Search works against title/URL/content via FTS5 only; semantic-search results are simply absent (not an error).
5. `/chat` shows a friendly "chat unavailable — configure an LLM backend" page.
6. The link detail page shows the orange "no summary yet, configure LLM" banner with the actual probe error.
7. No DGX-specific URL or API key appears in any default config dump.
