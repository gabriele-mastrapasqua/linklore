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
  backend: "ollama"          # ollama | litellm
  ollama:
    host: "http://192.168.1.93:11434"   # DGX
    model: "qwen3:4b"
    embed_model: "nomic-embed-text"
    num_ctx: 8192
    timeout_seconds: 120
  litellm:
    base_url: "http://192.168.1.93:4000/v1"   # vLLM via LiteLLM proxy
    model: "qwen3-4b"
    api_key: "$LITELLM_API_KEY"

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
